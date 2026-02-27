package app

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go-agent/internal/agent"
	"go-agent/internal/config"
	ctxbuild "go-agent/internal/context"
	"go-agent/internal/llm"
	"go-agent/internal/message"
	"go-agent/internal/permission"
	"go-agent/internal/session"
	"go-agent/internal/tool"
)

type TurnRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Input     string `json:"input"`
	Agent     string `json:"agent,omitempty"`
}

type TurnResponse struct {
	SessionID        string            `json:"session_id"`
	Agent            string            `json:"agent"`
	Output           string            `json:"output,omitempty"`
	ApprovalRequired *session.Approval `json:"approval_required,omitempty"`
}

type ApprovalPrompt struct {
	SessionID string
	TurnID    string
	ToolName  string
	Args      json.RawMessage
	Rule      string
}

type Approver interface {
	Approve(ctx context.Context, p ApprovalPrompt) (bool, error)
}

type App struct {
	cfg         config.Config
	agents      *agent.Manager
	sessions    *session.Manager
	context     *ctxbuild.Builder
	permissions *permission.Engine
	tools       *tool.Registry
	llm         llm.Provider
	retriever   Retriever
}

const (
	maxConsecutiveInvalidModelOutputs = 8
	maxConsecutiveSameToolCalls       = 12
)

type Retriever interface {
	Retrieve(ctx context.Context, query string) (string, error)
}

func New(
	cfg config.Config,
	agents *agent.Manager,
	sessions *session.Manager,
	contextBuilder *ctxbuild.Builder,
	permissions *permission.Engine,
	tools *tool.Registry,
	llmProvider llm.Provider,
) *App {
	return &App{
		cfg:         cfg,
		agents:      agents,
		sessions:    sessions,
		context:     contextBuilder,
		permissions: permissions,
		tools:       tools,
		llm:         llmProvider,
	}
}

func (a *App) SetRetriever(r Retriever) {
	a.retriever = r
}

func (a *App) CreateSession(ctx context.Context) (session.Session, error) {
	return a.sessions.CreateSession(ctx)
}

func (a *App) GetSession(ctx context.Context, sessionID string) (session.Session, error) {
	return a.sessions.GetSession(ctx, sessionID)
}

func (a *App) ListSessionIDs(ctx context.Context, limit int) ([]string, error) {
	return a.sessions.ListSessionIDs(ctx, limit)
}

func (a *App) ResolveApproval(ctx context.Context, approvalID string, approved bool) (session.Approval, error) {
	return a.sessions.ResolveApproval(ctx, approvalID, approved)
}

func (a *App) HandleTurn(ctx context.Context, req TurnRequest, approver Approver) (TurnResponse, error) {
	input := strings.TrimSpace(req.Input)
	if input == "" {
		return TurnResponse{}, errors.New("input is empty")
	}

	var sess session.Session
	var err error
	if strings.TrimSpace(req.SessionID) == "" {
		sess, err = a.sessions.CreateSession(ctx)
		if err != nil {
			return TurnResponse{}, err
		}
	} else {
		sess, err = a.sessions.GetSession(ctx, req.SessionID)
		if err != nil {
			return TurnResponse{}, err
		}
	}

	agentName, cleanedInput := a.agents.ParseAtAgent(input, req.Agent)
	if cleanedInput == "" {
		cleanedInput = input
	}
	def, err := a.agents.Resolve(agentName)
	if err != nil {
		return TurnResponse{}, err
	}

	turnID := buildID("turn", fmt.Sprintf("%s|%s|%d", sess.ID, cleanedInput, time.Now().UnixNano()))
	current := []message.Message{
		{
			Role:      message.RoleUser,
			Content:   cleanedInput,
			CreatedAt: time.Now().UTC(),
		},
	}

	turn := message.Turn{
		ID:        turnID,
		Agent:     def.Name,
		UserInput: cleanedInput,
		CreatedAt: time.Now().UTC(),
	}
	toolSchemas := a.tools.Schemas(def.Tools)
	finalOutput := ""
	consecutiveInvalid := 0
	lastToolSig := ""
	sameToolCalls := 0
	extraSystem := make([]string, 0, 1)
	if a.retriever != nil {
		retrieved, retrieveErr := a.retriever.Retrieve(ctx, cleanedInput)
		if retrieveErr != nil {
			turn.Audit = append(turn.Audit, message.AuditEvent{
				Type:      "retrieval_error",
				Detail:    retrieveErr.Error(),
				CreatedAt: time.Now().UTC(),
			})
		} else if strings.TrimSpace(retrieved) != "" {
			extraSystem = append(extraSystem, "Retrieved local code context:\n"+retrieved)
			turn.Audit = append(turn.Audit, message.AuditEvent{
				Type:      "retrieval_context_added",
				Detail:    fmt.Sprintf("chars=%d", len([]rune(retrieved))),
				CreatedAt: time.Now().UTC(),
			})
		}
	}

	for step := 1; ; step++ {
		msgs := a.context.BuildWithExtras(def.Name, sess, current, toolSchemas, extraSystem)
		resp, llmErr := a.chatWithRateLimitFallback(ctx, def, msgs, &turn)
		if llmErr != nil {
			a.persistFailure(ctx, sess.ID, &turn, current, "llm_error", llmErr.Error())
			if isRateLimited(llmErr) {
				return TurnResponse{}, fmt.Errorf(
					"llm step %d failed: provider rate-limited (HTTP 429). wait and retry, or lower max_tokens",
					step,
				)
			}
			return TurnResponse{}, fmt.Errorf("llm step %d failed: %w", step, llmErr)
		}

		action, parseErr := parseModelAction(resp.Content)
		if parseErr != nil {
			consecutiveInvalid++
			current = append(current, message.Message{
				Role:      message.RoleAssistant,
				Content:   "Invalid structured output, retrying.\n" + clip(resp.Content, 1200),
				CreatedAt: time.Now().UTC(),
			})
			turn.Audit = append(turn.Audit, message.AuditEvent{
				Type:      "invalid_model_output",
				Detail:    parseErr.Error(),
				CreatedAt: time.Now().UTC(),
			})
			if consecutiveInvalid >= maxConsecutiveInvalidModelOutputs {
				err := fmt.Errorf("model returned invalid structured output %d times", consecutiveInvalid)
				a.persistFailure(ctx, sess.ID, &turn, current, "invalid_model_output_loop", err.Error())
				return TurnResponse{}, err
			}
			continue
		}
		consecutiveInvalid = 0

		if action.Type == "final" {
			finalOutput = strings.TrimSpace(action.Content)
			if finalOutput == "" {
				finalOutput = "(empty final response)"
			}
			current = append(current, message.Message{
				Role:      message.RoleAssistant,
				Content:   finalOutput,
				CreatedAt: time.Now().UTC(),
			})
			break
		}

		if action.Type != "tool_call" || strings.TrimSpace(action.Tool.Name) == "" {
			consecutiveInvalid++
			current = append(current, message.Message{
				Role:      message.RoleAssistant,
				Content:   "Model returned unsupported action, retrying.",
				CreatedAt: time.Now().UTC(),
			})
			turn.Audit = append(turn.Audit, message.AuditEvent{
				Type:      "invalid_action",
				Detail:    clip(resp.Content, 1000),
				CreatedAt: time.Now().UTC(),
			})
			if consecutiveInvalid >= maxConsecutiveInvalidModelOutputs {
				err := fmt.Errorf("model returned unsupported action %d times", consecutiveInvalid)
				a.persistFailure(ctx, sess.ID, &turn, current, "invalid_action_loop", err.Error())
				return TurnResponse{}, err
			}
			continue
		}
		consecutiveInvalid = 0

		toolName := strings.ToLower(strings.TrimSpace(action.Tool.Name))
		callID := buildID("call", fmt.Sprintf("%s|%s|%d", turnID, toolName, step))
		toolSig := toolName + "|" + strings.TrimSpace(string(action.Tool.Args))
		if toolSig == lastToolSig {
			sameToolCalls++
		} else {
			lastToolSig = toolSig
			sameToolCalls = 1
		}
		if sameToolCalls > maxConsecutiveSameToolCalls {
			err := fmt.Errorf("detected repetitive tool-call loop (%d times): %s", sameToolCalls, toolName)
			turn.Audit = append(turn.Audit, message.AuditEvent{
				Type:      "tool_loop_detected",
				ToolName:  toolName,
				Detail:    err.Error(),
				CreatedAt: time.Now().UTC(),
			})
			a.persistFailure(ctx, sess.ID, &turn, current, "tool_loop_detected", err.Error())
			return TurnResponse{}, err
		}
		current = append(current, message.Message{
			Role:      message.RoleAssistant,
			Content:   fmt.Sprintf(`{"type":"tool_call","tool":{"name":"%s","args":%s}}`, toolName, string(action.Tool.Args)),
			ToolCalls: []message.ToolCall{{ID: callID, Name: toolName, Args: action.Tool.Args}},
			CreatedAt: time.Now().UTC(),
		})

		if !contains(def.Tools, toolName) {
			turn.Audit = append(turn.Audit, message.AuditEvent{
				Type:      "tool_not_enabled",
				ToolName:  toolName,
				Detail:    "tool is not in current agent tool list",
				CreatedAt: time.Now().UTC(),
			})
			result := message.ToolResult{
				ID:      callID,
				Name:    toolName,
				Error:   "tool not enabled for this agent",
				Allowed: string(permission.DecisionDeny),
			}
			turn.Results = append(turn.Results, result)
			current = append(current, message.Message{
				Role:       message.RoleTool,
				ToolCallID: callID,
				Content:    toResultJSON(result),
				CreatedAt:  time.Now().UTC(),
			})
			continue
		}

		decision, matched := a.permissions.Decide(toolName, def.Permissions)
		turn.Audit = append(turn.Audit, message.AuditEvent{
			Type:      "permission_decision",
			ToolName:  toolName,
			Decision:  string(decision),
			Detail:    "matched rule: " + matched,
			CreatedAt: time.Now().UTC(),
		})

		if decision == permission.DecisionAsk {
			if approver != nil {
				approved, approveErr := approver.Approve(ctx, ApprovalPrompt{
					SessionID: sess.ID,
					TurnID:    turnID,
					ToolName:  toolName,
					Args:      action.Tool.Args,
					Rule:      matched,
				})
				if approveErr != nil {
					a.persistFailure(ctx, sess.ID, &turn, current, "approval_error", approveErr.Error())
					return TurnResponse{}, approveErr
				}
				if approved {
					decision = permission.DecisionAllow
				} else {
					decision = permission.DecisionDeny
				}
			} else {
				apr, aprErr := a.sessions.CreateApproval(ctx, sess.ID, turnID, toolName, string(action.Tool.Args))
				if aprErr != nil {
					a.persistFailure(ctx, sess.ID, &turn, current, "approval_create_error", aprErr.Error())
					return TurnResponse{}, aprErr
				}
				turn.Messages = append(turn.Messages, current...)
				if _, appendErr := a.sessions.AppendTurn(ctx, sess.ID, turn); appendErr != nil {
					return TurnResponse{}, appendErr
				}
				return TurnResponse{
					SessionID:        sess.ID,
					Agent:            def.Name,
					Output:           "approval required",
					ApprovalRequired: &apr,
				}, nil
			}
		}

		if decision == permission.DecisionDeny {
			result := message.ToolResult{
				ID:      callID,
				Name:    toolName,
				Error:   "tool call denied by permission engine",
				Allowed: string(permission.DecisionDeny),
			}
			turn.Results = append(turn.Results, result)
			current = append(current, message.Message{
				Role:       message.RoleTool,
				ToolCallID: callID,
				Content:    toResultJSON(result),
				CreatedAt:  time.Now().UTC(),
			})
			continue
		}

		toolRes, runErr := a.tools.Run(ctx, toolName, action.Tool.Args)
		result := message.ToolResult{
			ID:      callID,
			Name:    toolName,
			Output:  clip(toolRes.Output, a.cfg.ToolOutputLimit),
			Allowed: string(permission.DecisionAllow),
		}
		if runErr != nil {
			result.Error = runErr.Error()
		}
		turn.Results = append(turn.Results, result)
		current = append(current, message.Message{
			Role:       message.RoleTool,
			ToolCallID: callID,
			Content:    toResultJSON(result),
			CreatedAt:  time.Now().UTC(),
		})
	}

	turn.Messages = append(turn.Messages, current...)
	if _, err := a.sessions.AppendTurn(ctx, sess.ID, turn); err != nil {
		return TurnResponse{}, err
	}
	return TurnResponse{
		SessionID: sess.ID,
		Agent:     def.Name,
		Output:    finalOutput,
	}, nil
}

func (a *App) persistFailure(ctx context.Context, sessionID string, turn *message.Turn, current []message.Message, eventType, detail string) {
	if turn == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	turn.Audit = append(turn.Audit, message.AuditEvent{
		Type:      eventType,
		Detail:    detail,
		CreatedAt: time.Now().UTC(),
	})
	turn.Messages = append(turn.Messages, current...)
	_, _ = a.sessions.AppendTurn(ctx, sessionID, *turn)
}

func (a *App) chatWithRateLimitFallback(
	ctx context.Context,
	def agent.Definition,
	msgs []llm.ChatMessage,
	turn *message.Turn,
) (llm.ChatResponse, error) {
	req := llm.ChatRequest{
		Model:       def.Model,
		Messages:    msgs,
		Temperature: def.Temperature,
		MaxTokens:   def.MaxTokens,
	}
	resp, err := a.llm.Chat(ctx, req)
	if err == nil {
		return resp, nil
	}
	if !isRateLimited(err) {
		return llm.ChatResponse{}, err
	}

	// One degrade-pass retry: lower token budget and disable thinking/stream
	// to reduce TPM/RPM pressure under provider rate limiting.
	maxTok := req.MaxTokens
	if maxTok > 1024 {
		maxTok = 1024
	}
	if maxTok < 256 {
		maxTok = 256
	}
	stream := false
	enableThinking := false
	clearThinking := true
	fallbackReq := req
	fallbackReq.MaxTokens = maxTok
	fallbackReq.Stream = &stream
	fallbackReq.EnableThinking = &enableThinking
	fallbackReq.ClearThinking = &clearThinking

	if turn != nil {
		turn.Audit = append(turn.Audit, message.AuditEvent{
			Type:      "rate_limit_fallback",
			Detail:    fmt.Sprintf("fallback with max_tokens=%d, stream=false, enable_thinking=false", maxTok),
			CreatedAt: time.Now().UTC(),
		})
	}
	return a.llm.Chat(ctx, fallbackReq)
}

func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "http 429") || strings.Contains(lower, "rate limit")
}

type modelAction struct {
	Type      string   `json:"type"`
	Content   string   `json:"content,omitempty"`
	Thinking  string   `json:"thinking,omitempty"`
	NextSteps []string `json:"next_steps,omitempty"`
	Tool      struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"tool,omitempty"`
}

func parseModelAction(raw string) (modelAction, error) {
	obj, err := extractJSONObject(raw)
	if err != nil {
		return modelAction{}, err
	}
	var act modelAction
	if err := json.Unmarshal([]byte(obj), &act); err != nil {
		return modelAction{}, err
	}
	act.Type = strings.ToLower(strings.TrimSpace(act.Type))
	return act, nil
}

func extractJSONObject(s string) (string, error) {
	start := strings.Index(s, "{")
	if start < 0 {
		return "", errors.New("json object start not found")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", errors.New("json object end not found")
}

func toResultJSON(r message.ToolResult) string {
	b, _ := json.Marshal(r)
	return string(b)
}

func contains(list []string, item string) bool {
	item = strings.ToLower(strings.TrimSpace(item))
	for _, v := range list {
		if strings.ToLower(strings.TrimSpace(v)) == item {
			return true
		}
	}
	return false
}

func clip(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + fmt.Sprintf("\n...[truncated %d chars]", len(r)-max)
}

func buildID(prefix, seed string) string {
	sum := sha1.Sum([]byte(seed))
	return prefix + "_" + hex.EncodeToString(sum[:8])
}
