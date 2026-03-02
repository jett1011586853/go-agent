package app

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go-agent/internal/agent"
	"go-agent/internal/config"
	ctxbuild "go-agent/internal/context"
	"go-agent/internal/llm"
	"go-agent/internal/message"
	"go-agent/internal/permission"
	"go-agent/internal/session"
	skillset "go-agent/internal/skills"
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
	skills      *skillset.Service
	metrics     *runtimeMetrics
}

const (
	maxConsecutiveInvalidModelOutputs = 8
	maxConsecutiveSameToolCalls       = 12
	maxConsecutiveToolFailures        = 8
	maxPlannerTimeoutRecoveries       = 3
	failureFingerprintWindow          = 20
	failureFingerprintReplanThreshold = 3
	failureFingerprintAbortThreshold  = 6
	maxContinuationRounds             = 8
	continuationTailLines             = 50
	continuationTailChars             = 6000
	plannerTokenCap                   = 16383
)

const plannerControlMessage = `Planner phase rules:
- Return only one compact JSON action.
- Allowed action types: "tool_call" | "final" | "ask_clarification" | "no_tool".
- Keep content concise (<= 240 chars) and focused on decision.
- If detailed final response is needed, set final.content to "__NEED_WRITER__" and put key points in next_steps.
 - Use structured tools for filesystem mutations: "mkdir", "write_file", "edit", "patch", "apply_diff".
- Do not use "bash" to write/delete files; use "bash" for command execution only.
- When task is phased, complete one phase at a time and call verify(scope="project") at each phase boundary.
- Do not output long patches in planner phase.`

const plannerRepairMessage = `Planner repair rules:
- Output exactly one valid JSON action object and nothing else.
- Allowed top-level "type" values: "tool_call", "final", "ask_clarification", or "no_tool".
- Preserve intent of previous planner output.
- Do not include markdown, code fences, or explanations.`

const plannerFewShotMessage = `Planner tool-selection examples:
User: "How should we approach a multi-step refactor?"
Action: {"type":"tool_call","tool":{"name":"skill","args":{"action":"load","name":"writing-plans"}}}

User: "Polish this short paragraph for clarity."
Action: {"type":"no_tool","content":"<direct improved text>"}

User: "Deploy this project" (missing target platform/account details)
Action: {"type":"ask_clarification","content":"Please confirm deployment target and credentials scope first."}`

const writerControlMessage = `Writer phase rules:
- Produce the final user-facing output now; do not output JSON.
- Do not output planning notes, meta commentary, or chain-of-thought.
- Be complete and concrete.
- If returning a patch, wrap it with "*** BEGIN PATCH" and "*** END PATCH".`

type Retriever interface {
	Retrieve(ctx context.Context, query string) (string, error)
}

type RetrieverNoticeSource interface {
	ConsumeNotices() []string
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
		metrics:     newRuntimeMetrics(),
	}
}

func (a *App) SetRetriever(r Retriever) {
	a.retriever = r
}

func (a *App) SetSkills(s *skillset.Service) {
	a.skills = s
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
	consecutiveToolFailures := 0
	consecutivePlannerTimeouts := 0
	timeoutRecoveryLevel := 0
	lastToolFailureHint := ""
	sameFailureSigCount := 0
	failureSigWindow := make([]string, 0, failureFingerprintWindow)
	failureSigCounts := map[string]int{}
	lastToolSig := ""
	sameToolCalls := 0
	toolErrorSeen := false
	toolRecoveryCounted := false
	plannerSteps := 0
	extraSystem := make([]string, 0, 2)
	dynamicSkillsContext := ""
	dynamicRetrievedContext := ""
	lastSkillsQuery := ""
	lastRetrievalQuery := ""
	lastWorkingHint := "."
	activeRoot := normalizeRelPath(sess.ActiveRoot)
	initialActiveRoot := deriveActiveRootFromInput(cleanedInput, a.cfg.WorkspaceRoot)
	if initialActiveRoot == "" {
		initialActiveRoot = activeRoot
	}
	phaseCtl := newPhaseController(cleanedInput, a.cfg.PhaseMaxFiles)
	if a.metrics != nil {
		a.metrics.turnsTotal.Add(1)
	}
	if phaseCtl.enabled {
		emitEvent(ctx, Event{
			Type: "planner",
			Text: fmt.Sprintf("phase governance enabled: total=%d current=%d limit=%d", phaseCtl.total, phaseCtl.current, phaseCtl.maxFilesPerPhase),
		})
		turn.Audit = append(turn.Audit, message.AuditEvent{
			Type:      "phase_governance_enabled",
			Detail:    fmt.Sprintf("total=%d max_files=%d", phaseCtl.total, phaseCtl.maxFilesPerPhase),
			CreatedAt: time.Now().UTC(),
		})
	}
	recordFailureSig := func(toolName, errText, output string) int {
		sig := buildFailureSignature(toolName, errText, output)
		if strings.TrimSpace(sig) == "" {
			return 0
		}
		if len(failureSigWindow) >= failureFingerprintWindow {
			oldest := failureSigWindow[0]
			failureSigWindow = failureSigWindow[1:]
			if c := failureSigCounts[oldest]; c <= 1 {
				delete(failureSigCounts, oldest)
			} else {
				failureSigCounts[oldest] = c - 1
			}
		}
		failureSigWindow = append(failureSigWindow, sig)
		failureSigCounts[sig] = failureSigCounts[sig] + 1
		return failureSigCounts[sig]
	}
	setActiveRoot := func(candidate, source string) {
		candidate = normalizeRelPath(candidate)
		if candidate == "." {
			candidate = ""
		}
		if candidate == activeRoot {
			return
		}
		activeRoot = candidate
		if strings.TrimSpace(activeRoot) != "" {
			emitEvent(ctx, Event{Type: "meta", Text: "active root set: " + candidate + " (" + source + ")"})
		} else {
			emitEvent(ctx, Event{Type: "meta", Text: "active root cleared (" + source + ")"})
		}
		if persisted, persistErr := a.sessions.UpdateActiveRoot(ctx, sess.ID, activeRoot); persistErr == nil {
			sess = persisted
		} else {
			emitEvent(ctx, Event{Type: "meta", Text: "active root persist failed: " + persistErr.Error()})
		}
		turn.Audit = append(turn.Audit, message.AuditEvent{
			Type:      "active_root_set",
			Detail:    fmt.Sprintf("source=%s root=%s", source, candidate),
			CreatedAt: time.Now().UTC(),
		})
	}
	setPhaseStage := func(next phaseStage, reason string) {
		if !phaseCtl.enabled {
			return
		}
		if !phaseCtl.setStage(next) {
			return
		}
		stage := string(phaseCtl.stageOrDefault())
		emitEvent(ctx, Event{Type: "stage", Text: fmt.Sprintf("stage=%s reason=%s", stage, strings.TrimSpace(reason))})
		turn.Audit = append(turn.Audit, message.AuditEvent{
			Type:      "phase_stage_transition",
			Detail:    fmt.Sprintf("stage=%s reason=%s", stage, strings.TrimSpace(reason)),
			CreatedAt: time.Now().UTC(),
		})
	}
	emitPhaseStage := func(reason string) {
		if !phaseCtl.enabled {
			return
		}
		stage := string(phaseCtl.stageOrDefault())
		emitEvent(ctx, Event{Type: "stage", Text: fmt.Sprintf("stage=%s reason=%s", stage, strings.TrimSpace(reason))})
		turn.Audit = append(turn.Audit, message.AuditEvent{
			Type:      "phase_stage_transition",
			Detail:    fmt.Sprintf("stage=%s reason=%s", stage, strings.TrimSpace(reason)),
			CreatedAt: time.Now().UTC(),
		})
	}
	if phaseCtl.enabled {
		setPhaseStage(phaseStagePlan, "turn_start")
	}
	if initialActiveRoot != "" {
		setActiveRoot(initialActiveRoot, "user_input")
	}
	emitEvent(ctx, Event{
		Type: "planner",
		Text: "planner phase started: " + clip(cleanedInput, 160),
	})

	for step := 1; ; step++ {
		if a.skills != nil {
			skillsQuery := buildDynamicRetrieveQuery(cleanedInput, lastToolFailureHint, lastWorkingHint, phaseCtl.changedFiles(), activeRoot)
			skillCtx, skillErr := a.skills.BuildContext(ctx, skillsQuery)
			if skillErr != nil {
				if step == 1 || strings.TrimSpace(lastSkillsQuery) != strings.TrimSpace(skillsQuery) {
					emitEvent(ctx, Event{Type: "meta", Text: "skills refresh failed: " + skillErr.Error()})
					turn.Audit = append(turn.Audit, message.AuditEvent{
						Type:      "skills_context_error",
						Detail:    skillErr.Error(),
						CreatedAt: time.Now().UTC(),
					})
				}
			} else {
				capped := ""
				if strings.TrimSpace(skillCtx.Text) != "" {
					capped = capSystemContext(skillCtx.Text, runtimeSkillsContextCap(a.cfg.SkillsMaxCtx, timeoutRecoveryLevel))
				}
				if step == 1 || capped != dynamicSkillsContext {
					emitEvent(ctx, Event{Type: "skills", Text: summarizeLoadedSkills(skillCtx.Loaded)})
					turn.Audit = append(turn.Audit, message.AuditEvent{
						Type:      "skills_context_added",
						Detail:    fmt.Sprintf("step=%d skills=%d chars=%d", step, len(skillCtx.Loaded), len([]rune(strings.TrimSpace(capped)))),
						CreatedAt: time.Now().UTC(),
					})
				}
				if step == 1 && strings.TrimSpace(capped) != "" && a.metrics != nil {
					a.metrics.turnsWithSkills.Add(1)
				}
				dynamicSkillsContext = capped
				lastSkillsQuery = skillsQuery
			}
		}
		if a.retriever != nil {
			retrieveQuery := buildDynamicRetrieveQuery(cleanedInput, lastToolFailureHint, lastWorkingHint, phaseCtl.changedFiles(), activeRoot)
			retrieved, retrieveErr := a.retriever.Retrieve(ctx, retrieveQuery)
			if notifier, ok := a.retriever.(RetrieverNoticeSource); ok {
				for _, notice := range notifier.ConsumeNotices() {
					notice = strings.TrimSpace(notice)
					if notice == "" {
						continue
					}
					emitEvent(ctx, Event{Type: "meta", Text: "[retrieval] " + notice})
					turn.Audit = append(turn.Audit, message.AuditEvent{
						Type:      "retrieval_notice",
						Detail:    notice,
						CreatedAt: time.Now().UTC(),
					})
				}
			}
			if retrieveErr != nil {
				if step == 1 || strings.TrimSpace(lastRetrievalQuery) != strings.TrimSpace(retrieveQuery) {
					emitEvent(ctx, Event{Type: "meta", Text: "retrieval refresh failed: " + retrieveErr.Error()})
					turn.Audit = append(turn.Audit, message.AuditEvent{
						Type:      "retrieval_error",
						Detail:    retrieveErr.Error(),
						CreatedAt: time.Now().UTC(),
					})
				}
			} else {
				capped := ""
				if strings.TrimSpace(retrieved) != "" {
					capped = capSystemContext("Retrieved local code context:\n"+retrieved, runtimeRetrievalContextCap(a.cfg.EmbeddingMaxCtx, timeoutRecoveryLevel))
				}
				if step == 1 || capped != dynamicRetrievedContext {
					emitEvent(ctx, Event{Type: "retrieval", Text: summarizeRetrievedContext(retrieved)})
					turn.Audit = append(turn.Audit, message.AuditEvent{
						Type:      "retrieval_context_added",
						Detail:    fmt.Sprintf("step=%d chars=%d", step, len([]rune(strings.TrimSpace(capped)))),
						CreatedAt: time.Now().UTC(),
					})
				}
				dynamicRetrievedContext = capped
				lastRetrievalQuery = retrieveQuery
			}
		}
		loopExtra := append([]string(nil), extraSystem...)
		if strings.TrimSpace(dynamicSkillsContext) != "" {
			loopExtra = append(loopExtra, dynamicSkillsContext)
		}
		if strings.TrimSpace(dynamicRetrievedContext) != "" {
			loopExtra = append(loopExtra, dynamicRetrievedContext)
		}
		if phaseCtl.enabled {
			if phaseHint := strings.TrimSpace(phaseCtl.systemHint()); phaseHint != "" {
				loopExtra = append(loopExtra, phaseHint)
			}
		}
		if activeRoot != "" {
			loopExtra = append(loopExtra, "Active project root: "+activeRoot+"\nPrefer files under this path unless explicitly required otherwise.")
		}
		if strings.TrimSpace(lastToolFailureHint) != "" {
			loopExtra = append(loopExtra, lastToolFailureHint)
		}
		msgs := a.context.BuildWithExtras(def.Name, sess, current, toolSchemas, loopExtra)
		plannerSteps++
		if step == 1 {
			emitEvent(ctx, Event{
				Type: "planner",
				Text: fmt.Sprintf("planner step #%d: plan next action for current request", step),
			})
		} else {
			emitEvent(ctx, Event{
				Type: "planner",
				Text: fmt.Sprintf("planner step #%d: deciding next action (tool_call/final/clarify/no_tool)", step),
			})
		}
		resp, llmErr := a.chatPlanner(ctx, def, msgs, &turn)
		if llmErr != nil {
			if isTimeoutError(llmErr) {
				consecutivePlannerTimeouts++
				if timeoutRecoveryLevel < maxPlannerTimeoutRecoveries {
					timeoutRecoveryLevel++
				}
				emitEvent(ctx, Event{
					Type: "planner",
					Text: fmt.Sprintf(
						"planner timeout detected at step #%d; recovering with slimmer context (attempt %d/%d)",
						step,
						consecutivePlannerTimeouts,
						maxPlannerTimeoutRecoveries,
					),
				})
				turn.Audit = append(turn.Audit, message.AuditEvent{
					Type:      "planner_timeout_recovery",
					Detail:    fmt.Sprintf("step=%d attempt=%d err=%s", step, consecutivePlannerTimeouts, clip(llmErr.Error(), 280)),
					CreatedAt: time.Now().UTC(),
				})
				lastToolFailureHint = buildToolFailureHint(
					"planner_timeout",
					fmt.Sprintf("planner step timeout: %s", clip(llmErr.Error(), 320)),
				)
				current = compactPlannerMessagesForTimeout(current)
				if consecutivePlannerTimeouts <= maxPlannerTimeoutRecoveries {
					continue
				}
			}
			a.persistFailure(ctx, sess.ID, &turn, current, "llm_error", llmErr.Error())
			if isRateLimited(llmErr) {
				return TurnResponse{}, fmt.Errorf(
					"llm step %d failed: provider rate-limited (HTTP 429). wait and retry, or lower max_tokens",
					step,
				)
			}
			return TurnResponse{}, fmt.Errorf("llm step %d failed: %w", step, llmErr)
		}
		consecutivePlannerTimeouts = 0
		timeoutRecoveryLevel = 0

		action, parseErr := parseModelAction(resp.Content)
		if parseErr != nil {
			repairedAction, repaired, repairErr := a.tryRepairPlannerAction(ctx, def, msgs, resp.Content, &turn)
			if repairErr == nil && repaired {
				action = repairedAction
				consecutiveInvalid = 0
				emitEvent(ctx, Event{
					Type: "planner",
					Text: "planner output repaired into valid structured action",
				})
				turn.Audit = append(turn.Audit, message.AuditEvent{
					Type:      "planner_output_repaired",
					Detail:    "used repair sub-call after parse failure",
					CreatedAt: time.Now().UTC(),
				})
				goto parsedAction
			}
			consecutiveInvalid++
			if a.metrics != nil {
				a.metrics.invalidModelOutputs.Add(1)
			}
			current = append(current, message.Message{
				Role:      message.RoleAssistant,
				Content:   "Invalid structured output, retrying.\n" + clip(resp.Content, 1200),
				CreatedAt: time.Now().UTC(),
			})
			parseDetail := parseErr.Error()
			if repairErr != nil {
				parseDetail = parseDetail + "; repair_err=" + clip(repairErr.Error(), 240)
			}
			turn.Audit = append(turn.Audit, message.AuditEvent{
				Type:      "invalid_model_output",
				Detail:    parseDetail,
				CreatedAt: time.Now().UTC(),
			})
			emitEvent(ctx, Event{
				Type: "planner",
				Text: "planner output invalid JSON; retrying with stricter format",
			})
			if consecutiveInvalid >= maxConsecutiveInvalidModelOutputs {
				err := fmt.Errorf("model returned invalid structured output %d times", consecutiveInvalid)
				a.persistFailure(ctx, sess.ID, &turn, current, "invalid_model_output_loop", err.Error())
				return TurnResponse{}, err
			}
			continue
		}
	parsedAction:
		consecutiveInvalid = 0
		title := summarizePlannerTitle(action)
		if title != "" {
			emitEvent(ctx, Event{
				Type: "planner",
				Text: "planner title: " + title,
			})
		}
		intent := summarizePlannerIntent(action)
		if intent != "" {
			emitEvent(ctx, Event{
				Type: "planner",
				Text: "planner intent: " + intent,
			})
		}
		emitEvent(ctx, Event{
			Type: "planner",
			Text: "planner decision: " + summarizePlannerAction(action),
		})

		if phaseCtl.enabled && (action.Type == "final" || action.Type == "no_tool") {
			if phaseCtl.pendingProjectCheck {
				setPhaseStage(phaseStageVerify, "finalization_gate")
				emitEvent(ctx, Event{
					Type: "planner",
					Text: fmt.Sprintf("phase gate: enforce verify(scope=project) for phase %d/%d before finalizing", phaseCtl.current, phaseCtl.total),
				})
				verifyResult, verifyErr := a.autoVerifyProject(ctx, phaseCtl.changedFiles(), activeRoot)
				verifyCallID := buildID("call", fmt.Sprintf("%s|phase-verify|%d", turnID, step))
				verifyMsg := message.ToolResult{
					ID:      verifyCallID,
					Name:    "verify",
					Output:  clip(verifyResult.Output, a.cfg.ToolOutputModel),
					Allowed: string(permission.DecisionAllow),
				}
				verifyDisplay := clip(verifyResult.Output, a.cfg.ToolOutputDisplay)
				if verifyErr != nil {
					setPhaseStage(phaseStagePlan, "project_verify_failed")
					verifyMsg.Error = verifyErr.Error()
					if strings.TrimSpace(verifyDisplay) != "" {
						emitEvent(ctx, Event{Type: "tool", Text: "phase verify output (trimmed):\n" + verifyDisplay})
					}
					phaseFailureSummary := buildPhaseVerifyFailureSummary(
						phaseCtl.current,
						phaseCtl.total,
						verifyErr,
						verifyMsg.Output,
						phaseCtl.changedFiles(),
					)
					emitEvent(ctx, Event{Type: "tool", Text: "phase verify failed:\n" + phaseFailureSummary})
					toolErrorSeen = true
					if !toolRecoveryCounted && a.metrics != nil {
						a.metrics.recoveryAttempts.Add(1)
						toolRecoveryCounted = true
					}
					consecutiveToolFailures++
					lastToolFailureHint = buildToolFailureHint("verify", phaseFailureSummary)
					sameFailureSigCount = recordFailureSig("verify", verifyErr.Error(), verifyMsg.Output)
					turn.Results = append(turn.Results, verifyMsg)
					current = append(current, message.Message{
						Role:       message.RoleTool,
						ToolCallID: verifyCallID,
						Content:    toResultJSON(verifyMsg),
						CreatedAt:  time.Now().UTC(),
					})
					current = append(current, message.Message{
						Role:      message.RoleAssistant,
						Content:   phaseFailureSummary,
						CreatedAt: time.Now().UTC(),
					})
					if sameFailureSigCount >= failureFingerprintAbortThreshold {
						err := fmt.Errorf("repetitive failing phase verify pattern detected (%d)", sameFailureSigCount)
						a.persistFailure(ctx, sess.ID, &turn, current, "repetitive_phase_verify_failure", err.Error())
						return TurnResponse{}, err
					}
					if sameFailureSigCount >= failureFingerprintReplanThreshold {
						current = append(current, message.Message{
							Role:      message.RoleAssistant,
							Content:   "Repeated verify failure pattern detected. Switch strategy: inspect exact failing files/errors, then apply a smaller corrective patch before retrying.",
							CreatedAt: time.Now().UTC(),
						})
					}
					if consecutiveToolFailures >= maxConsecutiveToolFailures {
						err := fmt.Errorf("tool failed %d times in a row (%s)", consecutiveToolFailures, "verify")
						a.persistFailure(ctx, sess.ID, &turn, current, "tool_failure_loop", err.Error())
						return TurnResponse{}, err
					}
					continue
				}
				setPhaseStage(phaseStageSummary, "project_verify_passed")
				if strings.TrimSpace(verifyDisplay) != "" {
					emitEvent(ctx, Event{Type: "tool", Text: "phase verify output (trimmed):\n" + verifyDisplay})
				}
				turn.Results = append(turn.Results, verifyMsg)
				current = append(current, message.Message{
					Role:       message.RoleTool,
					ToolCallID: verifyCallID,
					Content:    toResultJSON(verifyMsg),
					CreatedAt:  time.Now().UTC(),
				})
				phaseFiles := phaseCtl.changedFiles()
				completedPhase := phaseCtl.current
				summaryRes, decisionLogPath, summaryErr := a.appendPhaseDecisionLog(ctx, completedPhase, phaseCtl.total, phaseFiles, verifyMsg.Output)
				summaryCallID := buildID("call", fmt.Sprintf("%s|phase-log|%d", turnID, step))
				summaryMsg := message.ToolResult{
					ID:      summaryCallID,
					Name:    "write_file",
					Output:  clip(summaryRes.Output, a.cfg.ToolOutputModel),
					Allowed: string(permission.DecisionAllow),
					Writes:  toMessageWrites(summaryRes.Writes),
				}
				if summaryErr != nil {
					summaryMsg.Error = summaryErr.Error()
					emitEvent(ctx, Event{Type: "meta", Text: "phase decision-log append failed: " + summaryErr.Error()})
				} else {
					emitEvent(ctx, Event{
						Type: "meta",
						Text: fmt.Sprintf("phase summary appended: phase %d/%d -> %s", completedPhase, phaseCtl.total, decisionLogPath),
					})
				}
				turn.Results = append(turn.Results, summaryMsg)
				current = append(current, message.Message{
					Role:       message.RoleTool,
					ToolCallID: summaryCallID,
					Content:    toResultJSON(summaryMsg),
					CreatedAt:  time.Now().UTC(),
				})
				phaseCtl.closeCurrentPhase()
				emitPhaseStage("phase_closed")
				turn.Audit = append(turn.Audit, message.AuditEvent{
					Type:      "phase_completed",
					Detail:    fmt.Sprintf("phase=%d/%d", completedPhase, phaseCtl.total),
					CreatedAt: time.Now().UTC(),
				})
				emitEvent(ctx, Event{
					Type: "planner",
					Text: fmt.Sprintf("phase %d/%d completed", completedPhase, phaseCtl.total),
				})
				consecutiveToolFailures = 0
				lastToolFailureHint = ""
			}
			if phaseCtl.hasRemainingPhases() {
				msg := fmt.Sprintf("phase %d/%d still in progress. Continue with tool_call actions for this phase; do not finalize yet.", phaseCtl.current, phaseCtl.total)
				emitEvent(ctx, Event{Type: "planner", Text: "phase gate: " + msg})
				current = append(current, message.Message{
					Role:      message.RoleAssistant,
					Content:   msg,
					CreatedAt: time.Now().UTC(),
				})
				continue
			}
		}

		if action.Type == "ask_clarification" {
			finalOutput = strings.TrimSpace(action.Content)
			if strings.TrimSpace(action.Question) != "" {
				finalOutput = strings.TrimSpace(action.Question)
			}
			if finalOutput == "" {
				finalOutput = "Need more details before proceeding. Please clarify your request."
			}
			current = append(current, message.Message{
				Role:      message.RoleAssistant,
				Content:   finalOutput,
				CreatedAt: time.Now().UTC(),
			})
			break
		}

		if action.Type == "no_tool" {
			finalOutput = strings.TrimSpace(action.Content)
			if finalOutput == "" {
				finalOutput = "No tool needed for this request."
			}
			current = append(current, message.Message{
				Role:      message.RoleAssistant,
				Content:   finalOutput,
				CreatedAt: time.Now().UTC(),
			})
			break
		}

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
			if a.metrics != nil {
				a.metrics.invalidModelOutputs.Add(1)
			}
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
		if normalizedArgs, converted := normalizeToolArgsToWorkspaceRel(action.Tool.Args, a.cfg.WorkspaceRoot); len(converted) > 0 {
			action.Tool.Args = normalizedArgs
			detail := strings.Join(converted, "; ")
			emitEvent(ctx, Event{Type: "meta", Text: "normalized absolute tool paths: " + detail})
			turn.Audit = append(turn.Audit, message.AuditEvent{
				Type:      "path_normalized",
				ToolName:  toolName,
				Detail:    detail,
				CreatedAt: time.Now().UTC(),
			})
		}
		if phaseCtl.enabled {
			if toolName == "verify" && plannedVerifyScope(action.Tool.Args) == "project" {
				setPhaseStage(phaseStageVerify, "tool_call.verify_project")
			} else {
				setPhaseStage(phaseStageImplement, "tool_call."+toolName)
			}
		}
		if hint := inferWorkingHintFromToolArgs(toolName, action.Tool.Args); hint != "" {
			lastWorkingHint = hint
			if root := inferActiveRootFromPath(hint); root != "" {
				setActiveRoot(root, "tool_args")
			}
		}
		if a.metrics != nil {
			a.metrics.toolCalls.Add(1)
		}
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
			if a.metrics != nil {
				a.metrics.toolErrors.Add(1)
			}
			toolErrorSeen = true
			if !toolRecoveryCounted && a.metrics != nil {
				a.metrics.recoveryAttempts.Add(1)
				toolRecoveryCounted = true
			}
			consecutiveToolFailures++
			lastToolFailureHint = buildToolFailureHint(toolName, "tool is not enabled for this agent")
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
			if consecutiveToolFailures >= maxConsecutiveToolFailures {
				err := fmt.Errorf("tool failed %d times in a row (%s)", consecutiveToolFailures, toolName)
				a.persistFailure(ctx, sess.ID, &turn, current, "tool_failure_loop", err.Error())
				return TurnResponse{}, err
			}
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
			if a.metrics != nil {
				a.metrics.toolErrors.Add(1)
			}
			toolErrorSeen = true
			if !toolRecoveryCounted && a.metrics != nil {
				a.metrics.recoveryAttempts.Add(1)
				toolRecoveryCounted = true
			}
			consecutiveToolFailures++
			lastToolFailureHint = buildToolFailureHint(toolName, "tool call denied by permission engine")
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
			if consecutiveToolFailures >= maxConsecutiveToolFailures {
				err := fmt.Errorf("tool failed %d times in a row (%s)", consecutiveToolFailures, toolName)
				a.persistFailure(ctx, sess.ID, &turn, current, "tool_failure_loop", err.Error())
				return TurnResponse{}, err
			}
			continue
		}

		toolRes, runErr := a.tools.Run(ctx, toolName, action.Tool.Args)
		emitEvent(ctx, Event{Type: "tool", Text: "running tool: " + toolName})
		result := message.ToolResult{
			ID:      callID,
			Name:    toolName,
			Output:  clip(toolRes.Output, a.cfg.ToolOutputModel),
			Allowed: string(permission.DecisionAllow),
			Writes:  toMessageWrites(toolRes.Writes),
		}
		displayToolOutput := clip(toolRes.Output, a.cfg.ToolOutputDisplay)
		if runErr != nil {
			result.Error = runErr.Error()
			emitEvent(ctx, Event{Type: "tool", Text: "tool failed: " + runErr.Error()})
			if strings.TrimSpace(displayToolOutput) != "" {
				emitEvent(ctx, Event{Type: "tool", Text: "tool output (trimmed):\n" + displayToolOutput})
			}
			if a.metrics != nil {
				a.metrics.toolErrors.Add(1)
			}
			toolErrorSeen = true
			if !toolRecoveryCounted && a.metrics != nil {
				a.metrics.recoveryAttempts.Add(1)
				toolRecoveryCounted = true
			}
			consecutiveToolFailures++
			lastToolFailureHint = buildToolFailureHint(toolName, runErr.Error()+"\n"+clip(result.Output, 600))
			sameFailureSigCount = recordFailureSig(toolName, runErr.Error(), result.Output)
		} else if strings.TrimSpace(displayToolOutput) != "" {
			emitEvent(ctx, Event{Type: "tool", Text: "tool output (trimmed):\n" + displayToolOutput})
			consecutiveToolFailures = 0
			lastToolFailureHint = ""
		} else {
			consecutiveToolFailures = 0
			lastToolFailureHint = ""
		}
		turn.Results = append(turn.Results, result)
		current = append(current, message.Message{
			Role:       message.RoleTool,
			ToolCallID: callID,
			Content:    toResultJSON(result),
			CreatedAt:  time.Now().UTC(),
		})

		if runErr == nil && phaseCtl.enabled && len(result.Writes) > 0 {
			if root := inferActiveRootFromWrites(result.Writes); root != "" {
				setActiveRoot(root, "tool_writes")
			}
			stageBeforeWrites := phaseCtl.stageOrDefault()
			if phaseErr := phaseCtl.addWrites(result.Writes); phaseErr != nil {
				setPhaseStage(phaseStagePlan, "phase_guard_failed")
				if a.metrics != nil {
					a.metrics.toolErrors.Add(1)
				}
				toolErrorSeen = true
				if !toolRecoveryCounted && a.metrics != nil {
					a.metrics.recoveryAttempts.Add(1)
					toolRecoveryCounted = true
				}
				consecutiveToolFailures++
				lastToolFailureHint = buildToolFailureHint("phase_guard", phaseErr.Error())
				sameFailureSigCount = recordFailureSig("phase_guard", phaseErr.Error(), "")
				emitEvent(ctx, Event{Type: "planner", Text: "phase guard: " + phaseErr.Error()})
				current = append(current, message.Message{
					Role:      message.RoleAssistant,
					Content:   "Phase constraint violated. Re-plan and split the work into smaller batches before continuing.",
					CreatedAt: time.Now().UTC(),
				})
				if sameFailureSigCount >= failureFingerprintAbortThreshold {
					err := fmt.Errorf("repetitive phase constraint failure (%d)", sameFailureSigCount)
					a.persistFailure(ctx, sess.ID, &turn, current, "repetitive_phase_constraint_failure", err.Error())
					return TurnResponse{}, err
				}
				if sameFailureSigCount >= failureFingerprintReplanThreshold {
					current = append(current, message.Message{
						Role:      message.RoleAssistant,
						Content:   "Repeated phase-constraint failures detected. Reduce touched files this step and use a narrower patch.",
						CreatedAt: time.Now().UTC(),
					})
				}
				if consecutiveToolFailures >= maxConsecutiveToolFailures {
					err := fmt.Errorf("tool failed %d times in a row (%s)", consecutiveToolFailures, "phase_guard")
					a.persistFailure(ctx, sess.ID, &turn, current, "tool_failure_loop", err.Error())
					return TurnResponse{}, err
				}
				continue
			}
			if stageAfterWrites := phaseCtl.stageOrDefault(); stageAfterWrites != stageBeforeWrites {
				emitPhaseStage("writes_detected")
			}
		}
		if runErr != nil {
			setPhaseStage(phaseStagePlan, "tool_failed")
			if sameFailureSigCount >= failureFingerprintAbortThreshold {
				err := fmt.Errorf("repetitive failing tool pattern detected (%d): %s", sameFailureSigCount, toolName)
				a.persistFailure(ctx, sess.ID, &turn, current, "repetitive_tool_failure", err.Error())
				return TurnResponse{}, err
			}
			if phaseCtl.enabled && toolName == "verify" && plannedVerifyScope(action.Tool.Args) == "project" {
				current = append(current, message.Message{
					Role: message.RoleAssistant,
					Content: buildPhaseVerifyFailureSummary(
						phaseCtl.current,
						phaseCtl.total,
						runErr,
						result.Output,
						phaseCtl.changedFiles(),
					),
					CreatedAt: time.Now().UTC(),
				})
			} else {
				current = append(current, message.Message{
					Role:      message.RoleAssistant,
					Content:   "Tool failed. Re-plan with corrected args, alternate tool, or ask_clarification.",
					CreatedAt: time.Now().UTC(),
				})
			}
			if sameFailureSigCount >= failureFingerprintReplanThreshold {
				current = append(current, message.Message{
					Role:      message.RoleAssistant,
					Content:   "Repeated failing pattern detected. Do not retry the same command shape. Read the failing file/error context first, then switch tool or patch strategy.",
					CreatedAt: time.Now().UTC(),
				})
				lastToolFailureHint = buildToolFailureHint(toolName, "Repeated failure pattern detected. You must change strategy/tool/arguments before next attempt.\n"+lastToolFailureHint)
			}
			if consecutiveToolFailures >= maxConsecutiveToolFailures {
				err := fmt.Errorf("tool failed %d times in a row (%s)", consecutiveToolFailures, toolName)
				a.persistFailure(ctx, sess.ID, &turn, current, "tool_failure_loop", err.Error())
				return TurnResponse{}, err
			}
			continue
		}

		if phaseCtl.enabled && toolName == "verify" && plannedVerifyScope(action.Tool.Args) == "project" && phaseCtl.pendingProjectCheck {
			setPhaseStage(phaseStageSummary, "project_verify_passed")
			phaseFiles := phaseCtl.changedFiles()
			completedPhase := phaseCtl.current
			summaryRes, decisionLogPath, summaryErr := a.appendPhaseDecisionLog(ctx, completedPhase, phaseCtl.total, phaseFiles, result.Output)
			summaryCallID := buildID("call", fmt.Sprintf("%s|phase-log|%d", turnID, step))
			summaryMsg := message.ToolResult{
				ID:      summaryCallID,
				Name:    "write_file",
				Output:  clip(summaryRes.Output, a.cfg.ToolOutputModel),
				Allowed: string(permission.DecisionAllow),
				Writes:  toMessageWrites(summaryRes.Writes),
			}
			if summaryErr != nil {
				summaryMsg.Error = summaryErr.Error()
				emitEvent(ctx, Event{Type: "meta", Text: "phase decision-log append failed: " + summaryErr.Error()})
			} else {
				emitEvent(ctx, Event{
					Type: "meta",
					Text: fmt.Sprintf("phase summary appended: phase %d/%d -> %s", completedPhase, phaseCtl.total, decisionLogPath),
				})
			}
			turn.Results = append(turn.Results, summaryMsg)
			current = append(current, message.Message{
				Role:       message.RoleTool,
				ToolCallID: summaryCallID,
				Content:    toResultJSON(summaryMsg),
				CreatedAt:  time.Now().UTC(),
			})
			phaseCtl.closeCurrentPhase()
			emitPhaseStage("phase_closed")
			turn.Audit = append(turn.Audit, message.AuditEvent{
				Type:      "phase_completed",
				Detail:    fmt.Sprintf("phase=%d/%d", completedPhase, phaseCtl.total),
				CreatedAt: time.Now().UTC(),
			})
			emitEvent(ctx, Event{
				Type: "planner",
				Text: fmt.Sprintf("phase %d/%d completed", completedPhase, phaseCtl.total),
			})
		}

		if toolName != "verify" && len(result.Writes) > 0 {
			if !shouldAutoVerifyWrites(toolName, result.Writes) {
				setPhaseStage(phaseStageImplement, "auto_verify_skipped_directory_only")
				emitEvent(ctx, Event{Type: "meta", Text: "auto verify skipped for directory-only writes"})
				continue
			}
			verifyResult, verifyErr := a.autoVerifyAfterWrites(ctx, result.Writes, activeRoot)
			verifyCallID := buildID("call", fmt.Sprintf("%s|verify|%d", turnID, step))
			verifyMsg := message.ToolResult{
				ID:      verifyCallID,
				Name:    "verify",
				Output:  clip(verifyResult.Output, a.cfg.ToolOutputModel),
				Allowed: string(permission.DecisionAllow),
			}
			verifyDisplay := clip(verifyResult.Output, a.cfg.ToolOutputDisplay)
			if verifyErr != nil {
				setPhaseStage(phaseStagePlan, "auto_verify_failed")
				verifyMsg.Error = verifyErr.Error()
				if strings.TrimSpace(verifyDisplay) != "" {
					emitEvent(ctx, Event{Type: "tool", Text: "verify output (trimmed):\n" + verifyDisplay})
				}
				emitEvent(ctx, Event{Type: "tool", Text: "verify failed: " + verifyErr.Error()})
				toolErrorSeen = true
				consecutiveToolFailures++
				lastToolFailureHint = buildToolFailureHint("verify", verifyErr.Error()+"\n"+clip(verifyMsg.Output, 600))
				sameFailureSigCount = recordFailureSig("verify", verifyErr.Error(), verifyMsg.Output)
				turn.Results = append(turn.Results, verifyMsg)
				current = append(current, message.Message{
					Role:       message.RoleTool,
					ToolCallID: verifyCallID,
					Content:    toResultJSON(verifyMsg),
					CreatedAt:  time.Now().UTC(),
				})
				current = append(current, message.Message{
					Role:      message.RoleAssistant,
					Content:   "Auto verify failed after file writes. Re-plan with a minimal corrective patch before continuing.",
					CreatedAt: time.Now().UTC(),
				})
				if sameFailureSigCount >= failureFingerprintAbortThreshold {
					err := fmt.Errorf("repetitive failing verify pattern detected (%d)", sameFailureSigCount)
					a.persistFailure(ctx, sess.ID, &turn, current, "repetitive_verify_failure", err.Error())
					return TurnResponse{}, err
				}
				if sameFailureSigCount >= failureFingerprintReplanThreshold {
					current = append(current, message.Message{
						Role:      message.RoleAssistant,
						Content:   "Repeated verify failures detected. Narrow the fix to the reported compile/test errors and avoid broad rewrites.",
						CreatedAt: time.Now().UTC(),
					})
					lastToolFailureHint = buildToolFailureHint("verify", "Repeated verify failure pattern detected. Change approach before retrying.\n"+lastToolFailureHint)
				}
				if consecutiveToolFailures >= maxConsecutiveToolFailures {
					err := fmt.Errorf("tool failed %d times in a row (%s)", consecutiveToolFailures, "verify")
					a.persistFailure(ctx, sess.ID, &turn, current, "tool_failure_loop", err.Error())
					return TurnResponse{}, err
				}
				continue
			}
			setPhaseStage(phaseStageImplement, "auto_verify_passed")
			if strings.TrimSpace(verifyDisplay) != "" {
				emitEvent(ctx, Event{Type: "tool", Text: "verify output (trimmed):\n" + verifyDisplay})
			}
			emitEvent(ctx, Event{Type: "tool", Text: "verify passed after file writes"})
			consecutiveToolFailures = 0
			lastToolFailureHint = ""
			turn.Results = append(turn.Results, verifyMsg)
			current = append(current, message.Message{
				Role:       message.RoleTool,
				ToolCallID: verifyCallID,
				Content:    toResultJSON(verifyMsg),
				CreatedAt:  time.Now().UTC(),
			})
		}
	}

	if a.metrics != nil {
		a.metrics.totalPlannerSteps.Add(int64(plannerSteps))
		if toolErrorSeen && strings.TrimSpace(finalOutput) != "" {
			a.metrics.recoverySuccesses.Add(1)
		}
		emitEvent(ctx, Event{Type: "meta", Text: a.metrics.snapshot().String()})
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
	if isRateLimited(err) {
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

	if !isTimeoutError(err) || ctx.Err() != nil {
		return llm.ChatResponse{}, err
	}

	stream := false
	enableThinking := false
	clearThinking := true
	fallbackReq := req
	fallbackReq.Stream = &stream
	fallbackReq.EnableThinking = &enableThinking
	fallbackReq.ClearThinking = &clearThinking
	beforeChars := totalMessageChars(req.Messages)
	compactedMsgs, compacted := compactMessagesForTimeoutRetry(req.Messages)
	fallbackReq.Messages = compactedMsgs
	afterChars := totalMessageChars(fallbackReq.Messages)
	emitEvent(ctx, Event{
		Type: "meta",
		Text: fmt.Sprintf("llm timeout detected; retrying with compact context (%d->%d chars)", beforeChars, afterChars),
	})
	if turn != nil {
		turn.Audit = append(turn.Audit, message.AuditEvent{
			Type:      "timeout_fallback",
			Detail:    fmt.Sprintf("stream=false enable_thinking=false compacted=%t prompt_chars=%d->%d", compacted, beforeChars, afterChars),
			CreatedAt: time.Now().UTC(),
		})
	}
	resp2, err2 := a.llm.Chat(ctx, fallbackReq)
	if err2 == nil {
		return resp2, nil
	}
	return llm.ChatResponse{}, fmt.Errorf("chat timeout fallback failed: %w (initial error: %v)", err2, err)
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
	planMsgs = append(planMsgs, llm.ChatMessage{
		Role:    "system",
		Content: plannerFewShotMessage,
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

func (a *App) tryRepairPlannerAction(
	ctx context.Context,
	def agent.Definition,
	msgs []llm.ChatMessage,
	rawPlannerOutput string,
	turn *message.Turn,
) (modelAction, bool, error) {
	rawPlannerOutput = strings.TrimSpace(rawPlannerOutput)
	if rawPlannerOutput == "" {
		return modelAction{}, false, nil
	}
	repairMsgs := append([]llm.ChatMessage{}, msgs...)
	repairMsgs = append(repairMsgs,
		llm.ChatMessage{
			Role:    "system",
			Content: plannerRepairMessage,
		},
		llm.ChatMessage{
			Role: "user",
			Content: "Reformat the previous planner output into one valid action JSON.\n" +
				"Previous planner output:\n" + clip(rawPlannerOutput, 4000),
		},
	)
	stream := false
	maxTokens := 1024
	if def.MaxTokens > 0 && def.MaxTokens < maxTokens {
		maxTokens = def.MaxTokens
	}
	if maxTokens < 256 {
		maxTokens = 256
	}
	req := llm.ChatRequest{
		Model:       def.Model,
		Messages:    repairMsgs,
		Temperature: 0,
		MaxTokens:   maxTokens,
		Stream:      &stream,
	}
	resp, err := a.chatWithRateLimitFallbackReq(ctx, req, turn)
	if err != nil {
		return modelAction{}, false, err
	}
	action, parseErr := parseModelAction(resp.Content)
	if parseErr != nil {
		return modelAction{}, false, parseErr
	}
	return action, true, nil
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

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "deadline exceeded") ||
		strings.Contains(lower, "client.timeout exceeded") ||
		strings.Contains(lower, "i/o timeout")
}

func compactMessagesForTimeoutRetry(msgs []llm.ChatMessage) ([]llm.ChatMessage, bool) {
	if len(msgs) == 0 {
		return msgs, false
	}
	const (
		baseSystemKeep = 4
		tailKeep       = 10
	)
	out := make([]llm.ChatMessage, 0, baseSystemKeep+tailKeep)
	changed := false

	front := 0
	for i := 0; i < len(msgs) && len(out) < baseSystemKeep; i++ {
		if i > 0 && msgs[i].Role != "system" {
			break
		}
		trimmed, altered := trimMessageForTimeoutRetry(msgs[i], true)
		out = append(out, trimmed)
		if altered {
			changed = true
		}
		front = i + 1
	}
	if front == 0 {
		trimmed, altered := trimMessageForTimeoutRetry(msgs[0], true)
		out = append(out, trimmed)
		if altered {
			changed = true
		}
		front = 1
	}

	tailStart := len(msgs) - tailKeep
	if tailStart < front {
		tailStart = front
	}
	for i := tailStart; i < len(msgs); i++ {
		trimmed, altered := trimMessageForTimeoutRetry(msgs[i], false)
		out = append(out, trimmed)
		if altered {
			changed = true
		}
	}

	if totalMessageChars(out) < totalMessageChars(msgs) {
		changed = true
	}
	return out, changed
}

func trimMessageForTimeoutRetry(msg llm.ChatMessage, leadingSystem bool) (llm.ChatMessage, bool) {
	original := msg.Content
	content := strings.TrimSpace(original)
	limit := 2000
	role := strings.ToLower(strings.TrimSpace(msg.Role))

	switch role {
	case "system":
		if strings.HasPrefix(content, "Retrieved local code context:") {
			content = compactRetrievedContext(content, 6, 2800)
			msg.Content = content
			return msg, content != original
		}
		if strings.HasPrefix(content, "Loaded local workflow skills (SKILL.md):") {
			limit = 2800
		} else if leadingSystem {
			limit = 3500
		} else {
			limit = 2200
		}
	case "tool":
		limit = 1200
	case "assistant":
		limit = 1800
	case "user":
		limit = 2000
	default:
		limit = 1800
	}
	content = clip(content, limit)
	msg.Content = content
	return msg, content != original
}

func compactRetrievedContext(content string, maxRefs int, maxChars int) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, maxRefs+2)
	hasHeader := false
	refCount := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Retrieved local code context:") {
			if !hasHeader {
				kept = append(kept, "Retrieved local code context:")
				hasHeader = true
			}
			continue
		}
		if strings.HasPrefix(line, "[CONTEXT ") {
			if refCount >= maxRefs {
				break
			}
			kept = append(kept, line)
			refCount++
		}
	}
	if len(kept) == 0 {
		return clip(strings.TrimSpace(content), maxChars)
	}
	return clip(strings.Join(kept, "\n"), maxChars)
}

func totalMessageChars(msgs []llm.ChatMessage) int {
	total := 0
	for _, m := range msgs {
		total += len([]rune(strings.TrimSpace(m.Content)))
	}
	return total
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

func buildToolFailureHint(toolName, errText string) string {
	toolName = strings.TrimSpace(toolName)
	errText = clip(strings.TrimSpace(errText), 240)
	return strings.TrimSpace(
		"Tool failure feedback:\n" +
			"- Last tool: " + fallbackText(toolName, "<unknown>") + "\n" +
			"- Error: " + fallbackText(errText, "<none>") + "\n" +
			"- Re-plan now: fix args, choose a better tool, or return ask_clarification/no_tool.\n" +
			"- Do not repeat the same failing tool call with unchanged args.",
	)
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

func summarizeLoadedSkills(items []skillset.Summary) string {
	if len(items) == 0 {
		return "skills context attached"
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
		if len(names) >= 6 {
			break
		}
	}
	if len(names) == 0 {
		return "skills context attached"
	}
	return "loaded skills: " + strings.Join(names, ", ")
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
			return "call tool (unknown tool name)"
		}
		args := map[string]any{}
		_ = json.Unmarshal(action.Tool.Args, &args)
		return summarizeToolCallForUser(toolName, args)
	case "final":
		c := strings.TrimSpace(action.Content)
		if strings.EqualFold(c, "__NEED_WRITER__") {
			return "enter writer phase for final response"
		}
		if c == "" {
			return "direct final answer (empty)"
		}
		return "direct final answer: " + clip(c, 120)
	case "ask_clarification":
		q := strings.TrimSpace(action.Question)
		if q == "" {
			q = strings.TrimSpace(action.Content)
		}
		if q == "" {
			return "ask user for missing details"
		}
		return "ask user for clarification: " + clip(q, 120)
	case "no_tool":
		c := strings.TrimSpace(action.Content)
		if c == "" {
			return "answer directly without tools"
		}
		return "direct no-tool answer: " + clip(c, 120)
	default:
		return "unknown action"
	}
}

func summarizePlannerTitle(action modelAction) string {
	thinking := shortPlannerSentence(action.Thinking, 120)
	if thinking != "" {
		return thinking
	}
	switch action.Type {
	case "tool_call":
		toolName := strings.ToLower(strings.TrimSpace(action.Tool.Name))
		if toolName == "" {
			return "prepare to call tool"
		}
		return "prepare to call tool: " + toolName
	case "final":
		if shouldUseWriter(action) {
			return "prepare writer phase for final response"
		}
		return "prepare direct final answer"
	case "ask_clarification":
		return "prepare clarification question"
	case "no_tool":
		return "prepare direct no-tool answer"
	default:
		return "re-plan next step"
	}
}

func summarizePlannerIntent(action modelAction) string {
	switch action.Type {
	case "tool_call":
		toolName := strings.ToLower(strings.TrimSpace(action.Tool.Name))
		args := map[string]any{}
		_ = json.Unmarshal(action.Tool.Args, &args)
		if toolName == "" {
			return ""
		}
		return summarizeToolCallForUser(toolName, args)
	case "final":
		content := strings.TrimSpace(action.Content)
		if strings.EqualFold(content, "__NEED_WRITER__") {
			if len(action.NextSteps) == 0 {
				return "writer will generate complete final output"
			}
			return "writer must cover: " + summarizeNextSteps(action.NextSteps, 3)
		}
		if content == "" {
			return ""
		}
		return "final output preview: " + clip(content, 140)
	case "ask_clarification":
		q := strings.TrimSpace(action.Question)
		if q == "" {
			q = strings.TrimSpace(action.Content)
		}
		if q == "" {
			return "clarification required"
		}
		return "needs user clarification: " + clip(q, 140)
	case "no_tool":
		content := strings.TrimSpace(action.Content)
		if content == "" {
			return "direct response without tools"
		}
		return "no-tool response preview: " + clip(content, 140)
	default:
		return ""
	}
}

func summarizeNextSteps(steps []string, maxItems int) string {
	if len(steps) == 0 {
		return ""
	}
	if maxItems <= 0 {
		maxItems = 3
	}
	items := make([]string, 0, maxItems)
	for _, step := range steps {
		step = shortPlannerSentence(step, 80)
		if step == "" {
			continue
		}
		items = append(items, step)
		if len(items) >= maxItems {
			break
		}
	}
	return strings.Join(items, "; ")
}

func shortPlannerSentence(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if idx := strings.Index(s, "\n"); idx >= 0 {
		s = s[:idx]
	}
	seps := []string{". ", "! ", "? ", "; ", ": ", ", ", "\n"}
	cut := len(s)
	for _, sep := range seps {
		if i := strings.Index(s, sep); i >= 0 && i < cut {
			cut = i + len(strings.TrimSpace(sep))
		}
	}
	if cut < len(s) {
		s = strings.TrimSpace(s[:cut])
	}
	return clip(s, max)
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
			return fmt.Sprintf("read file: %s (lines %d-%d)", fallbackText(path, "."), start, end)
		}
		return fmt.Sprintf("read file: %s", fallbackText(path, "."))
	case "grep":
		query := argString(args, "query")
		path := argString(args, "path")
		return fmt.Sprintf("search text: %s (scope: %s)", fallbackText(query, "<empty>"), fallbackText(path, "."))
	case "glob":
		pattern := argString(args, "pattern")
		path := argString(args, "path")
		return fmt.Sprintf("find by pattern: %s (scope: %s)", fallbackText(pattern, "*"), fallbackText(path, "."))
	case "list":
		path := argString(args, "path")
		return fmt.Sprintf("list directory: %s", fallbackText(path, "."))
	case "edit":
		path := argString(args, "path")
		return fmt.Sprintf("edit file: %s", fallbackText(path, "."))
	case "write_file":
		path := argString(args, "path")
		mode := argString(args, "mode")
		if strings.TrimSpace(mode) == "" {
			mode = "overwrite"
		}
		return fmt.Sprintf("write file: %s (mode: %s)", fallbackText(path, "."), mode)
	case "patch":
		path := argString(args, "path")
		return fmt.Sprintf("patch file: %s", fallbackText(path, "."))
	case "apply_diff":
		diff := argString(args, "diff")
		changed := estimateChangedFilesFromDiff(diff)
		if changed > 0 {
			return fmt.Sprintf("apply unified diff (estimated files: %d)", changed)
		}
		return "apply unified diff patch"
	case "mkdir":
		path := argString(args, "path")
		return fmt.Sprintf("create directory: %s", fallbackText(path, "."))
	case "bash":
		cmd := clip(argString(args, "cmd"), 120)
		cwd := argString(args, "cwd")
		if strings.TrimSpace(cwd) == "" {
			cwd = "."
		}
		return fmt.Sprintf("run command: %s (cwd: %s)", fallbackText(cmd, "<empty>"), cwd)
	case "verify":
		scope := argString(args, "scope")
		path := argString(args, "path")
		if strings.TrimSpace(scope) == "" {
			scope = "project"
		}
		if strings.TrimSpace(path) == "" {
			path = "."
		}
		return fmt.Sprintf("verify changes: scope=%s (path: %s)", scope, path)
	case "webfetch":
		url := argString(args, "url")
		return fmt.Sprintf("fetch url: %s", fallbackText(url, "<empty>"))
	case "websearch":
		query := argString(args, "query")
		return fmt.Sprintf("web search: %s", fallbackText(query, "<empty>"))
	case "skill":
		action := argString(args, "action")
		name := argString(args, "name")
		switch strings.ToLower(action) {
		case "list":
			return "list available skills"
		case "load":
			return fmt.Sprintf("load skill: %s", fallbackText(name, "<empty>"))
		default:
			return fmt.Sprintf("call skill tool: action=%s", fallbackText(action, "<empty>"))
		}
	default:
		if path := argString(args, "path"); strings.TrimSpace(path) != "" {
			return fmt.Sprintf("call tool: %s (target: %s)", toolName, path)
		}
		return fmt.Sprintf("call tool: %s", toolName)
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
	Question  string   `json:"question,omitempty"`
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

func toMessageWrites(items []tool.WriteRecord) []message.WriteRecord {
	if len(items) == 0 {
		return nil
	}
	out := make([]message.WriteRecord, 0, len(items))
	for _, it := range items {
		path := strings.TrimSpace(it.Path)
		if path == "" {
			continue
		}
		op := strings.ToLower(strings.TrimSpace(it.Op))
		if op == "" {
			op = "modify"
		}
		out = append(out, message.WriteRecord{Path: path, Op: op})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *App) autoVerifyAfterWrites(ctx context.Context, writes []message.WriteRecord, activeRoot string) (tool.Result, error) {
	verifyTool, err := a.tools.Get("verify")
	if err != nil {
		return tool.Result{}, fmt.Errorf("verify tool unavailable: %w", err)
	}
	paths := make([]string, 0, len(writes))
	for _, w := range writes {
		p := strings.TrimSpace(w.Path)
		if p == "" {
			continue
		}
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	basePath := verifyBasePath(activeRoot)
	args, _ := json.Marshal(map[string]any{
		"scope":         "changed_files",
		"path":          basePath,
		"changed_files": paths,
	})
	return verifyTool.Run(ctx, args)
}

func (a *App) autoVerifyProject(ctx context.Context, changedFiles []string, activeRoot string) (tool.Result, error) {
	verifyTool, err := a.tools.Get("verify")
	if err != nil {
		return tool.Result{}, fmt.Errorf("verify tool unavailable: %w", err)
	}
	basePath := verifyBasePath(activeRoot)
	args, _ := json.Marshal(map[string]any{
		"scope":         "project",
		"path":          basePath,
		"changed_files": changedFiles,
	})
	return verifyTool.Run(ctx, args)
}

func verifyBasePath(activeRoot string) string {
	root := normalizeRelPath(activeRoot)
	if root == "" || root == "." {
		return "."
	}
	return root
}

func shouldAutoVerifyWrites(toolName string, writes []message.WriteRecord) bool {
	if strings.EqualFold(strings.TrimSpace(toolName), "mkdir") {
		return false
	}
	for _, w := range writes {
		op := strings.ToLower(strings.TrimSpace(w.Op))
		if op == "" {
			op = "modify"
		}
		if op != "mkdir" {
			return true
		}
	}
	return false
}

func (a *App) appendPhaseDecisionLog(
	ctx context.Context,
	phaseNumber int,
	totalPhases int,
	phaseFiles []string,
	verifyOutput string,
) (tool.Result, string, error) {
	writeTool, err := a.tools.Get("write_file")
	if err != nil {
		return tool.Result{}, "", fmt.Errorf("write_file tool unavailable: %w", err)
	}
	path := inferDecisionLogPathFromWrites(phaseFiles)
	content := buildPhaseSummaryEntry(phaseNumber, totalPhases, phaseFiles, verifyOutput)
	args, _ := json.Marshal(map[string]any{
		"path":    path,
		"mode":    "append",
		"content": content,
	})
	res, runErr := writeTool.Run(ctx, args)
	return res, path, runErr
}

func buildPhaseSummaryEntry(phaseNumber int, totalPhases int, phaseFiles []string, verifyOutput string) string {
	var b strings.Builder
	b.WriteString("\n## Phase ")
	b.WriteString(fmt.Sprintf("%d/%d", phaseNumber, totalPhases))
	b.WriteString(" - ")
	b.WriteString(time.Now().UTC().Format(time.RFC3339))
	b.WriteString("\n")
	if len(phaseFiles) == 0 {
		b.WriteString("- Files: (none)\n")
	} else {
		b.WriteString(fmt.Sprintf("- Files (%d):\n", len(phaseFiles)))
		for _, file := range phaseFiles {
			b.WriteString("  - ")
			b.WriteString(normalizeRelPath(file))
			b.WriteString("\n")
		}
	}
	verifySummary := strings.TrimSpace(strings.ReplaceAll(verifyOutput, "\r\n", "\n"))
	if verifySummary == "" {
		verifySummary = "(empty)"
	}
	verifySummary = clip(verifySummary, 500)
	b.WriteString("- Verify(project):\n")
	for _, line := range strings.Split(verifySummary, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		b.WriteString("  - ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func buildPhaseVerifyFailureSummary(
	phaseNumber int,
	totalPhases int,
	verifyErr error,
	verifyOutput string,
	phaseFiles []string,
) string {
	var b strings.Builder
	rootErr := "(unknown)"
	if verifyErr != nil {
		rootErr = strings.TrimSpace(verifyErr.Error())
	}
	keyLines := strings.TrimSpace(extractErrorLikeLines(verifyOutput, 6))
	if keyLines == "" {
		keyLines = strings.TrimSpace(verifyOutput)
	}
	if keyLines == "" {
		keyLines = "(no diagnostic lines from verifier)"
	}
	keyLines = clip(strings.ReplaceAll(keyLines, "\r\n", "\n"), 700)

	b.WriteString(fmt.Sprintf("Phase %d/%d verify(project) failed.\n", phaseNumber, totalPhases))
	b.WriteString("Root error: ")
	b.WriteString(rootErr)
	b.WriteString("\n")
	b.WriteString("Changed files in this phase: ")
	if len(phaseFiles) == 0 {
		b.WriteString("(none)")
	} else {
		items := make([]string, 0, len(phaseFiles))
		for _, file := range phaseFiles {
			file = normalizeRelPath(file)
			if file == "" {
				continue
			}
			items = append(items, file)
		}
		if len(items) == 0 {
			b.WriteString("(none)")
		} else {
			if len(items) > 8 {
				b.WriteString(strings.Join(items[:8], ", "))
				b.WriteString(fmt.Sprintf(" (+%d more)", len(items)-8))
			} else {
				b.WriteString(strings.Join(items, ", "))
			}
		}
	}
	b.WriteString("\n")
	b.WriteString("Key diagnostics:\n")
	for _, line := range strings.Split(keyLines, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("Next action: apply a minimal corrective patch for this phase, then rerun verify(scope=\"project\").")
	return b.String()
}

func runtimeSkillsContextCap(cfgMax int, recoveryLevel int) int {
	target := 4000
	if cfgMax > 0 && cfgMax < target {
		target = cfgMax
	}
	if target < 1200 {
		target = 1200
	}
	if recoveryLevel > 0 {
		target = target / (recoveryLevel + 1)
		if target < 900 {
			target = 900
		}
	}
	return target
}

func runtimeRetrievalContextCap(cfgMax int, recoveryLevel int) int {
	target := 10000
	if cfgMax > 0 && cfgMax < target {
		target = cfgMax
	}
	if target < 4000 {
		target = 4000
	}
	if recoveryLevel > 0 {
		target = target / (recoveryLevel + 1)
		if target < 2200 {
			target = 2200
		}
	}
	return target
}

func compactPlannerMessagesForTimeout(current []message.Message) []message.Message {
	if len(current) <= 12 {
		return current
	}
	const (
		keepHead = 1
		keepTail = 10
	)
	out := make([]message.Message, 0, keepHead+keepTail)
	out = append(out, current[:keepHead]...)
	start := len(current) - keepTail
	if start < keepHead {
		start = keepHead
	}
	out = append(out, current[start:]...)
	for i := range out {
		out[i].Content = clip(out[i].Content, 1600)
	}
	return out
}

func capSystemContext(text string, maxChars int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if maxChars <= 0 {
		return text
	}
	return clip(text, maxChars)
}

func buildDynamicRetrieveQuery(goal, lastFailureHint, workingHint string, changedFiles []string, activeRoot string) string {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return ""
	}
	parts := []string{goal}
	activeRoot = normalizeRelPath(activeRoot)
	if activeRoot != "" && activeRoot != "." {
		parts = append(parts, "active_root: "+activeRoot)
	}
	workingHint = strings.TrimSpace(workingHint)
	if workingHint != "" && workingHint != "." {
		parts = append(parts, "working_path: "+workingHint)
	}
	failure := strings.TrimSpace(lastFailureHint)
	if failure != "" {
		failure = extractErrorLikeLines(failure, 4)
		failure = strings.TrimSpace(clip(strings.ReplaceAll(failure, "\r\n", "\n"), 280))
		if failure != "" {
			parts = append(parts, "recent_error: "+failure)
		}
	}
	if len(changedFiles) > 0 {
		items := make([]string, 0, min(8, len(changedFiles)))
		for _, f := range changedFiles {
			f = normalizeRelPath(f)
			if f == "" {
				continue
			}
			items = append(items, f)
			if len(items) >= 8 {
				break
			}
		}
		if len(items) > 0 {
			parts = append(parts, "recent_changed_files: "+strings.Join(items, ", "))
		}
	}
	return strings.Join(parts, "\n")
}

func inferWorkingHintFromToolArgs(toolName string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "bash":
		cwd := normalizeRelPath(argString(args, "cwd"))
		if cwd == "" {
			return "."
		}
		return cwd
	default:
		path := normalizeRelPath(argString(args, "path"))
		if path == "" {
			return ""
		}
		base := filepath.Base(path)
		// Heuristic: if target looks like a file, focus retrieval on its parent directory.
		if strings.Contains(base, ".") && !strings.HasSuffix(path, "/") {
			dir := normalizeRelPath(filepath.Dir(path))
			if dir != "" {
				return dir
			}
		}
		return path
	}
}

func normalizeToolArgsToWorkspaceRel(raw json.RawMessage, workspaceRoot string) (json.RawMessage, []string) {
	if len(raw) == 0 {
		return raw, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return raw, nil
	}
	rootAbs, err := filepath.Abs(strings.TrimSpace(workspaceRoot))
	if err != nil {
		return raw, nil
	}
	changed := false
	converted := make([]string, 0, 2)
	for _, key := range []string{"path", "cwd"} {
		value, ok := args[key]
		if !ok {
			continue
		}
		s, ok := value.(string)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(s)
		if trimmed == "" || !filepath.IsAbs(trimmed) {
			continue
		}
		abs := filepath.Clean(trimmed)
		rel, relErr := filepath.Rel(rootAbs, abs)
		if relErr != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		rel = normalizeRelPath(rel)
		if rel == "" {
			rel = "."
		}
		args[key] = rel
		changed = true
		converted = append(converted, fmt.Sprintf("%s: %s -> %s", key, trimmed, rel))
	}
	if !changed {
		return raw, nil
	}
	updated, err := json.Marshal(args)
	if err != nil {
		return raw, nil
	}
	return json.RawMessage(updated), converted
}

func deriveActiveRootFromInput(input, workspaceRoot string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	rootAbs, err := filepath.Abs(strings.TrimSpace(workspaceRoot))
	if err != nil {
		return ""
	}
	candidates := extractPathCandidates(input)
	for _, cand := range candidates {
		cand = strings.TrimSpace(cand)
		if cand == "" {
			continue
		}
		cand = strings.Trim(cand, "\"'`[](){}<>,.;:")
		cand = strings.ReplaceAll(cand, "/", string(filepath.Separator))
		var abs string
		if filepath.IsAbs(cand) {
			abs = filepath.Clean(cand)
		} else {
			abs = filepath.Clean(filepath.Join(rootAbs, cand))
		}
		rel, relErr := filepath.Rel(rootAbs, abs)
		if relErr != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		normalized := normalizeRelPath(rel)
		if normalized == "" || normalized == "." {
			continue
		}
		if strings.Contains(filepath.Base(normalized), ".") {
			normalized = normalizeRelPath(filepath.Dir(normalized))
		}
		if root := inferActiveRootFromPath(normalized); root != "" {
			return root
		}
	}
	return ""
}

func extractPathCandidates(input string) []string {
	// Pick common path-like tokens from user requests, including Windows absolute and workspace-relative paths.
	re := regexp.MustCompile("(?i)([a-z]:[\\\\/][^\\s\"'`,;\\)\\]\\}]+|\\.?[\\\\/][^\\s\"'`,;\\)\\]\\}]+|projects[\\\\/][^\\s\"'`,;\\)\\]\\}]+)")
	matches := re.FindAllString(input, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	seen := map[string]struct{}{}
	for _, m := range matches {
		key := strings.ToLower(strings.TrimSpace(m))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, m)
	}
	return out
}

func inferActiveRootFromWrites(writes []message.WriteRecord) string {
	for _, w := range writes {
		if root := inferActiveRootFromPath(w.Path); root != "" {
			return root
		}
	}
	return ""
}

func inferActiveRootFromPath(path string) string {
	path = normalizeRelPath(path)
	if path == "" || path == "." {
		return ""
	}
	if strings.Contains(filepath.Base(path), ".") {
		path = normalizeRelPath(filepath.Dir(path))
	}
	if path == "" || path == "." {
		return ""
	}
	parts := strings.Split(path, "/")
	if len(parts) >= 2 && strings.EqualFold(parts[0], "projects") {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

func estimateChangedFilesFromDiff(diff string) int {
	diff = strings.ReplaceAll(diff, "\r\n", "\n")
	if diff == "" {
		return 0
	}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(diff, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "+++ ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			path = strings.TrimPrefix(path, "b/")
			if path == "" || path == "/dev/null" {
				continue
			}
			seen[path] = struct{}{}
		}
	}
	return len(seen)
}

func plannedVerifyScope(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	scope, _ := args["scope"].(string)
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		scope = "project"
	}
	return scope
}

var (
	reFailureNormalizeQuoted = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	reFailureNormalizeDigits = regexp.MustCompile(`\d+`)
	reFailureNormalizeWS     = regexp.MustCompile(`\s+`)
)

func buildFailureSignature(toolName, errText, output string) string {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	errText = normalizeFailureText(errText)
	output = normalizeFailureText(extractErrorLikeLines(output, 3))
	return toolName + "|" + errText + "|" + output
}

func normalizeFailureText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	s = reFailureNormalizeQuoted.ReplaceAllString(s, "")
	s = reFailureNormalizeDigits.ReplaceAllString(s, "#")
	s = reFailureNormalizeWS.ReplaceAllString(s, " ")
	if len(s) > 240 {
		return strings.TrimSpace(s[:240])
	}
	return s
}

func extractErrorLikeLines(s string, max int) string {
	if max <= 0 {
		max = 3
	}
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	keep := make([]string, 0, max)
	for _, line := range lines {
		ll := strings.ToLower(strings.TrimSpace(line))
		if ll == "" {
			continue
		}
		if strings.Contains(ll, "error") || strings.Contains(ll, "failed") || strings.Contains(ll, "panic") || strings.Contains(ll, ":") {
			keep = append(keep, ll)
		}
		if len(keep) >= max {
			break
		}
	}
	if len(keep) == 0 {
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			keep = append(keep, line)
			if len(keep) >= max {
				break
			}
		}
	}
	return strings.Join(keep, "\n")
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
