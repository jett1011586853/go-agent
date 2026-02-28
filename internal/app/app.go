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
	maxContinuationRounds             = 8
	continuationTailLines             = 50
	continuationTailChars             = 6000
	plannerTokenCap                   = 16383
)

const plannerControlMessage = `Planner phase rules:
- Return only one compact JSON action (tool_call or final).
- Keep final.content concise (<= 240 chars) and focused on decision.
- If detailed final response is needed, set final.content to "__NEED_WRITER__" and put key points in next_steps.
- Do not output long patches in planner phase.`

const writerControlMessage = `Writer phase rules:
- Produce the final user-facing output now; do not output JSON.
- Do not output planning notes, meta commentary, or chain-of-thought.
- Be complete and concrete.
- If returning a patch, wrap it with "*** BEGIN PATCH" and "*** END PATCH".`

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
			emitEvent(ctx, Event{Type: "meta", Text: "retrieval failed: " + retrieveErr.Error()})
			turn.Audit = append(turn.Audit, message.AuditEvent{
				Type:      "retrieval_error",
				Detail:    retrieveErr.Error(),
				CreatedAt: time.Now().UTC(),
			})
		} else if strings.TrimSpace(retrieved) != "" {
			extraSystem = append(extraSystem, "Retrieved local code context:\n"+retrieved)
			emitEvent(ctx, Event{Type: "retrieval", Text: summarizeRetrievedContext(retrieved)})
			turn.Audit = append(turn.Audit, message.AuditEvent{
				Type:      "retrieval_context_added",
				Detail:    fmt.Sprintf("chars=%d", len([]rune(retrieved))),
				CreatedAt: time.Now().UTC(),
			})
		}
	}

	for step := 1; ; step++ {
		msgs := a.context.BuildWithExtras(def.Name, sess, current, toolSchemas, extraSystem)
		emitEvent(ctx, Event{
			Type: "planner",
			Text: fmt.Sprintf("planner step #%d: deciding next action (tool_call or final)", step),
		})
		resp, llmErr := a.chatPlanner(ctx, def, msgs, &turn)
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
		emitEvent(ctx, Event{
			Type: "planner",
			Text: "planner decision: " + summarizePlannerAction(action),
		})

		if action.Type == "final" {
			if shouldUseWriter(action) {
				emitEvent(ctx, Event{Type: "writer", Text: "writer phase: generating final response/patch"})
				writerResp, writerErr := a.chatWriter(ctx, def, msgs, action, &turn)
				if writerErr != nil {
					a.persistFailure(ctx, sess.ID, &turn, current, "writer_error", writerErr.Error())
					return TurnResponse{}, fmt.Errorf("writer failed: %w", writerErr)
				}
				finalOutput = strings.TrimSpace(writerResp.Content)
				turn.Audit = append(turn.Audit, message.AuditEvent{
					Type:      "writer_used",
					Detail:    fmt.Sprintf("finish_reason=%s", strings.TrimSpace(writerResp.FinishReason)),
					CreatedAt: time.Now().UTC(),
				})
			} else {
				finalOutput = strings.TrimSpace(action.Content)
			}
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
		emitEvent(ctx, Event{Type: "tool", Text: "running tool: " + toolName})
		result := message.ToolResult{
			ID:      callID,
			Name:    toolName,
			Output:  clip(toolRes.Output, a.cfg.ToolOutputLimit),
			Allowed: string(permission.DecisionAllow),
		}
		if runErr != nil {
			result.Error = runErr.Error()
			emitEvent(ctx, Event{Type: "tool", Text: "tool failed: " + runErr.Error()})
		} else if strings.TrimSpace(result.Output) != "" {
			emitEvent(ctx, Event{Type: "tool", Text: "tool output (trimmed):\n" + clip(result.Output, 1200)})
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
	maxTokens := adaptiveMaxTokens(def, msgs)
	req := llm.ChatRequest{
		Model:       def.Model,
		Messages:    msgs,
		Temperature: def.Temperature,
		MaxTokens:   maxTokens,
	}
	return a.chatWithRateLimitFallbackReq(ctx, req, turn)
}

func (a *App) chatWithRateLimitFallbackReq(
	ctx context.Context,
	req llm.ChatRequest,
	turn *message.Turn,
) (llm.ChatResponse, error) {
	if turn != nil {
		turn.Audit = append(turn.Audit, message.AuditEvent{
			Type:      "adaptive_max_tokens",
			Detail:    fmt.Sprintf("adaptive=%d prompt_est_tokens=%d", req.MaxTokens, estimatePromptTokens(req.Messages)),
			CreatedAt: time.Now().UTC(),
		})
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

func (a *App) chatPlanner(
	ctx context.Context,
	def agent.Definition,
	msgs []llm.ChatMessage,
	turn *message.Turn,
) (llm.ChatResponse, error) {
	planMsgs := append([]llm.ChatMessage{}, msgs...)
	planMsgs = append(planMsgs, llm.ChatMessage{
		Role:    "system",
		Content: plannerControlMessage,
	})
	maxTokens := adaptiveMaxTokens(def, planMsgs)
	if maxTokens > plannerTokenCap {
		maxTokens = plannerTokenCap
	}
	if maxTokens < 512 {
		maxTokens = 512
	}
	stream := false
	req := llm.ChatRequest{
		Model:       def.Model,
		Messages:    planMsgs,
		Temperature: def.Temperature,
		MaxTokens:   maxTokens,
		Stream:      &stream,
	}
	return a.chatWithContinuationReq(ctx, req, turn)
}

func (a *App) chatWriter(
	ctx context.Context,
	def agent.Definition,
	msgs []llm.ChatMessage,
	action modelAction,
	turn *message.Turn,
) (llm.ChatResponse, error) {
	writerMsgs := append([]llm.ChatMessage{}, msgs...)
	writerMsgs = append(writerMsgs, llm.ChatMessage{
		Role:    "system",
		Content: writerControlMessage,
	})
	writerMsgs = append(writerMsgs, llm.ChatMessage{
		Role:    "user",
		Content: buildWriterPrompt(action),
	})
	maxTokens := adaptiveMaxTokens(def, writerMsgs)
	if maxTokens < 2048 {
		maxTokens = 2048
	}
	stream := true
	req := llm.ChatRequest{
		Model:       def.Model,
		Messages:    writerMsgs,
		Temperature: def.Temperature,
		MaxTokens:   maxTokens,
		Stream:      &stream,
	}
	return a.chatWithContinuationReq(ctx, req, turn)
}

func (a *App) chatWithContinuationReq(
	ctx context.Context,
	req llm.ChatRequest,
	turn *message.Turn,
) (llm.ChatResponse, error) {
	baseReq := req
	resp, err := a.chatWithRateLimitFallbackReq(ctx, baseReq, turn)
	if err != nil {
		return llm.ChatResponse{}, err
	}

	combined := resp.Content
	finish := strings.ToLower(strings.TrimSpace(resp.FinishReason))
	eventType := "writer"
	if baseReq.Stream != nil && !*baseReq.Stream {
		eventType = "planner"
	}
	for round := 1; round <= maxContinuationRounds; round++ {
		if !needsContinuation(finish, combined) {
			break
		}
		emitEvent(ctx, Event{
			Type: eventType,
			Text: fmt.Sprintf("output may be truncated; continuing (round %d, finish_reason=%s)", round, finish),
		})
		tail := lastNLines(combined, continuationTailLines)
		tail = clip(tail, continuationTailChars)
		contPrompt := buildContinuationPrompt(tail)

		contReq := baseReq
		contReq.Messages = append([]llm.ChatMessage{}, baseReq.Messages...)
		contReq.Messages = append(contReq.Messages,
			llm.ChatMessage{
				Role:    "assistant",
				Content: combined,
			},
			llm.ChatMessage{
				Role:    "user",
				Content: contPrompt,
			},
		)
		// Stream only the first response; continuation rounds are kept non-stream
		// to avoid duplicate/jittery output when overlap de-duplication kicks in.
		stream := false
		contReq.Stream = &stream
		nextResp, nextErr := a.chatWithRateLimitFallbackReq(ctx, contReq, turn)
		if nextErr != nil {
			return llm.ChatResponse{}, nextErr
		}
		combined = mergeContinuation(combined, nextResp.Content)
		finish = strings.ToLower(strings.TrimSpace(nextResp.FinishReason))
		resp.Reasoning += nextResp.Reasoning
		if turn != nil {
			turn.Audit = append(turn.Audit, message.AuditEvent{
				Type:      "output_continuation",
				Detail:    fmt.Sprintf("round=%d finish_reason=%s", round, finish),
				CreatedAt: time.Now().UTC(),
			})
		}
	}
	resp.Content = combined
	resp.FinishReason = finish
	return resp, nil
}

func (a *App) chatWithContinuation(
	ctx context.Context,
	def agent.Definition,
	msgs []llm.ChatMessage,
	turn *message.Turn,
) (llm.ChatResponse, error) {
	req := llm.ChatRequest{
		Model:       def.Model,
		Messages:    append([]llm.ChatMessage(nil), msgs...),
		Temperature: def.Temperature,
		MaxTokens:   adaptiveMaxTokens(def, msgs),
	}
	return a.chatWithContinuationReq(ctx, req, turn)
}

func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "http 429") || strings.Contains(lower, "rate limit")
}

func needsContinuation(finishReason, content string) bool {
	finishReason = strings.ToLower(strings.TrimSpace(finishReason))
	hasPatchBegin := hasStandalonePatchMarker(content, "*** BEGIN PATCH")
	hasPatchEnd := hasStandalonePatchMarker(content, "*** END PATCH")
	if hasPatchBegin && !hasPatchEnd {
		return true
	}
	if hasUnclosedCodeFence(content) {
		return true
	}
	if finishReason == "length" || finishReason == "max_tokens" {
		if hasPatchBegin && hasPatchEnd {
			return false
		}
		if _, err := parseModelAction(content); err == nil {
			return false
		}
		return true
	}
	return false
}

func hasUnclosedCodeFence(content string) bool {
	return strings.Count(content, "```")%2 == 1
}

func hasStandalonePatchMarker(content, marker string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == marker {
			return true
		}
	}
	return false
}

func buildContinuationPrompt(tail string) string {
	return strings.TrimSpace(`Your previous output was cut off.
Continue from the exact previous last line and output only the missing remainder.
Do not restart from the beginning.
Do not repeat any text that was already sent.
Do not explain what you are doing.
Do not wrap output in JSON.
If you are producing a patch, continue until "*** END PATCH" appears on its own line.

Previous output tail:
` + tail)
}

func mergeContinuation(previous, next string) string {
	if next == "" {
		return previous
	}
	if previous == "" {
		return next
	}
	maxOverlap := len(previous)
	if len(next) < maxOverlap {
		maxOverlap = len(next)
	}
	if maxOverlap > 2000 {
		maxOverlap = 2000
	}
	for n := maxOverlap; n >= 32; n-- {
		if strings.HasSuffix(previous, next[:n]) {
			return previous + next[n:]
		}
	}
	return previous + next
}

func lastNLines(text string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) <= n {
		return text
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func adaptiveMaxTokens(def agent.Definition, msgs []llm.ChatMessage) int {
	maxTok := def.MaxTokens
	if maxTok <= 0 {
		maxTok = 4096
	}
	name := strings.ToLower(strings.TrimSpace(def.Name))
	if name == "plan" && maxTok > 16383 {
		maxTok = 16383
	}
	if name == "build" && maxTok > 16383 {
		maxTok = 16383
	}

	if maxTok < 512 {
		maxTok = 512
	}
	return maxTok
}

func estimatePromptTokens(msgs []llm.ChatMessage) int {
	totalRunes := 0
	for _, m := range msgs {
		totalRunes += len([]rune(m.Role))
		totalRunes += len([]rune(m.Content))
	}
	if totalRunes <= 0 {
		return 0
	}
	return (totalRunes + 3) / 4
}

func summarizeRetrievedContext(ctxText string) string {
	lines := strings.Split(strings.ReplaceAll(ctxText, "\r\n", "\n"), "\n")
	var refs []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[CONTEXT ") {
			refs = append(refs, line)
		}
		if len(refs) >= 6 {
			break
		}
	}
	if len(refs) == 0 {
		return "retrieved code context attached"
	}
	return "retrieved snippets:\n" + strings.Join(refs, "\n")
}

func shouldUseWriter(action modelAction) bool {
	content := strings.TrimSpace(action.Content)
	if strings.EqualFold(content, "__NEED_WRITER__") {
		return true
	}
	if strings.Contains(content, "*** BEGIN PATCH") {
		return true
	}
	if len([]rune(content)) > 320 {
		return true
	}
	// If planner has structured key points, writer can turn them into polished output.
	return len(action.NextSteps) > 0
}

func buildWriterPrompt(action modelAction) string {
	var b strings.Builder
	b.WriteString("Generate the final response now based on planner decision.\n")
	content := strings.TrimSpace(action.Content)
	if content != "" && !strings.EqualFold(content, "__NEED_WRITER__") {
		b.WriteString("Planner brief: ")
		b.WriteString(content)
		b.WriteString("\n")
	}
	if len(action.NextSteps) > 0 {
		b.WriteString("Must cover these points:\n")
		for _, step := range action.NextSteps {
			step = strings.TrimSpace(step)
			if step == "" {
				continue
			}
			b.WriteString("- ")
			b.WriteString(step)
			b.WriteString("\n")
		}
	}
	b.WriteString("Output plain text only (not JSON).")
	return b.String()
}

func summarizePlannerAction(action modelAction) string {
	switch action.Type {
	case "tool_call":
		toolName := strings.ToLower(strings.TrimSpace(action.Tool.Name))
		if toolName == "" {
			return "调用工具（未知工具名）"
		}
		args := map[string]any{}
		_ = json.Unmarshal(action.Tool.Args, &args)
		return summarizeToolCallForUser(toolName, args)
	case "final":
		c := strings.TrimSpace(action.Content)
		if strings.EqualFold(c, "__NEED_WRITER__") {
			return "需要进入写作阶段生成完整输出"
		}
		if c == "" {
			return "直接回答（空）"
		}
		return "直接回答：" + clip(c, 120)
	default:
		return "未知动作"
	}
}

func summarizeToolCallForUser(toolName string, args map[string]any) string {
	switch toolName {
	case "read":
		path := argString(args, "path")
		start, hasStart := argInt(args, "start_line")
		end, hasEnd := argInt(args, "end_line")
		if hasStart || hasEnd {
			if !hasStart {
				start = 1
			}
			if !hasEnd {
				end = start
			}
			return fmt.Sprintf("读取文件：%s（第 %d-%d 行）", fallbackText(path, "."), start, end)
		}
		return fmt.Sprintf("读取文件：%s", fallbackText(path, "."))
	case "grep":
		query := argString(args, "query")
		path := argString(args, "path")
		return fmt.Sprintf("全文搜索：%s（范围：%s）", fallbackText(query, "<empty>"), fallbackText(path, "."))
	case "glob":
		pattern := argString(args, "pattern")
		path := argString(args, "path")
		return fmt.Sprintf("按模式查找：%s（范围：%s）", fallbackText(pattern, "*"), fallbackText(path, "."))
	case "list":
		path := argString(args, "path")
		return fmt.Sprintf("列目录：%s", fallbackText(path, "."))
	case "edit":
		path := argString(args, "path")
		return fmt.Sprintf("编辑文件：%s", fallbackText(path, "."))
	case "patch":
		path := argString(args, "path")
		return fmt.Sprintf("补丁替换：%s", fallbackText(path, "."))
	case "bash":
		cmd := clip(argString(args, "cmd"), 120)
		cwd := argString(args, "cwd")
		if strings.TrimSpace(cwd) == "" {
			cwd = "."
		}
		return fmt.Sprintf("执行命令：%s（目录：%s）", fallbackText(cmd, "<empty>"), cwd)
	case "webfetch":
		url := argString(args, "url")
		return fmt.Sprintf("抓取网页：%s", fallbackText(url, "<empty>"))
	case "websearch":
		query := argString(args, "query")
		return fmt.Sprintf("联网搜索：%s", fallbackText(query, "<empty>"))
	default:
		if path := argString(args, "path"); strings.TrimSpace(path) != "" {
			return fmt.Sprintf("调用工具：%s（目标：%s）", toolName, path)
		}
		return fmt.Sprintf("调用工具：%s", toolName)
	}
}

func argString(args map[string]any, key string) string {
	if len(args) == 0 {
		return ""
	}
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	switch vv := v.(type) {
	case string:
		return strings.TrimSpace(vv)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", vv))
	}
}

func argInt(args map[string]any, key string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	v, ok := args[key]
	if !ok || v == nil {
		return 0, false
	}
	switch vv := v.(type) {
	case float64:
		return int(vv), true
	case int:
		return vv, true
	case int64:
		return int(vv), true
	default:
		return 0, false
	}
}

func fallbackText(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
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
