package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-agent/internal/agent"
	"go-agent/internal/config"
	ctxbuild "go-agent/internal/context"
	"go-agent/internal/llm"
	"go-agent/internal/permission"
	"go-agent/internal/session"
	"go-agent/internal/storage"
	"go-agent/internal/tool"
)

type stubProvider struct {
	responses []string
	idx       int
}

func (s *stubProvider) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	if s.idx >= len(s.responses) {
		return llm.ChatResponse{Content: `{"type":"final","content":"stub-final"}`}, nil
	}
	out := s.responses[s.idx]
	s.idx++
	return llm.ChatResponse{Content: out}, nil
}

func (*stubProvider) ChatStream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	return nil, io.EOF
}

type rateLimitThenSuccessProvider struct {
	calls int
}

func (p *rateLimitThenSuccessProvider) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		return llm.ChatResponse{}, errors.New("chat request HTTP 429: too many requests")
	}
	if req.MaxTokens > 1024 {
		return llm.ChatResponse{}, errors.New("fallback max_tokens was not applied")
	}
	if req.EnableThinking == nil || *req.EnableThinking {
		return llm.ChatResponse{}, errors.New("fallback should disable thinking")
	}
	if req.Stream == nil || *req.Stream {
		return llm.ChatResponse{}, errors.New("fallback should disable stream")
	}
	return llm.ChatResponse{Content: `{"type":"final","content":"ok-after-fallback"}`}, nil
}

func (*rateLimitThenSuccessProvider) ChatStream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	return nil, io.EOF
}

type timeoutThenSuccessProvider struct {
	calls             int
	firstPromptChars  int
	secondPromptChars int
}

func (p *timeoutThenSuccessProvider) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		p.firstPromptChars = testPromptChars(req.Messages)
		return llm.ChatResponse{}, context.DeadlineExceeded
	}
	p.secondPromptChars = testPromptChars(req.Messages)
	if req.EnableThinking == nil || *req.EnableThinking {
		return llm.ChatResponse{}, errors.New("timeout fallback should disable thinking")
	}
	if req.Stream == nil || *req.Stream {
		return llm.ChatResponse{}, errors.New("timeout fallback should disable stream")
	}
	return llm.ChatResponse{Content: `{"type":"final","content":"ok-after-timeout-fallback"}`}, nil
}

func (*timeoutThenSuccessProvider) ChatStream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	return nil, io.EOF
}

type plannerTimeoutRecoverProvider struct {
	calls int
}

func (p *plannerTimeoutRecoverProvider) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	p.calls++
	if p.calls <= 2 {
		return llm.ChatResponse{}, context.DeadlineExceeded
	}
	return llm.ChatResponse{Content: `{"type":"final","content":"recovered-after-planner-timeout"}`}, nil
}

func (*plannerTimeoutRecoverProvider) ChatStream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	return nil, io.EOF
}

type alwaysInvalidProvider struct{}

func (alwaysInvalidProvider) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{Content: "not a json object"}, nil
}

func (alwaysInvalidProvider) ChatStream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	return nil, io.EOF
}

type invalidThenRepairProvider struct {
	calls int
}

func (p *invalidThenRepairProvider) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		return llm.ChatResponse{Content: "this is not valid JSON action"}, nil
	}
	return llm.ChatResponse{Content: `{"type":"final","content":"repaired-final"}`}, nil
}

func (*invalidThenRepairProvider) ChatStream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	return nil, io.EOF
}

type repeatToolProvider struct{}

func (repeatToolProvider) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{Content: `{"type":"tool_call","tool":{"name":"read","args":{"path":"a.txt"}}}`}, nil
}

func (repeatToolProvider) ChatStream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	return nil, io.EOF
}

type contextAwareProvider struct {
	foundRetrievedContext bool
}

func (p *contextAwareProvider) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	for _, m := range req.Messages {
		if m.Role != "system" {
			continue
		}
		if strings.Contains(m.Content, "Retrieved local code context:") &&
			strings.Contains(m.Content, "[CONTEXT 1]") {
			p.foundRetrievedContext = true
			break
		}
	}
	return llm.ChatResponse{Content: `{"type":"final","content":"ok-with-retrieval"}`}, nil
}

func (*contextAwareProvider) ChatStream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	return nil, io.EOF
}

type fixedRetriever struct {
	value string
}

func (r fixedRetriever) Retrieve(context.Context, string) (string, error) {
	return r.value, nil
}

type trackingRetriever struct {
	value   string
	calls   int
	queries []string
}

func (r *trackingRetriever) Retrieve(_ context.Context, query string) (string, error) {
	r.calls++
	r.queries = append(r.queries, query)
	return r.value, nil
}

type continuationProvider struct {
	calls int
}

func (p *continuationProvider) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		return llm.ChatResponse{
			Content: `{"type":"final","content":"__NEED_WRITER__","next_steps":["continue final output"]}`,
		}, nil
	}
	if p.calls == 2 {
		return llm.ChatResponse{
			Content:      "continued",
			FinishReason: "length",
		}, nil
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" || !strings.Contains(strings.ToLower(last.Content), "cut off") {
		return llm.ChatResponse{}, errors.New("missing continuation prompt")
	}
	return llm.ChatResponse{
		Content:      "-ok",
		FinishReason: "stop",
	}, nil
}

func (*continuationProvider) ChatStream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	return nil, io.EOF
}

type plannerWriterProvider struct {
	calls      int
	streams    []bool
	maxTokens  []int
	lastPrompt []llm.ChatMessage
}

func (p *plannerWriterProvider) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	p.calls++
	p.maxTokens = append(p.maxTokens, req.MaxTokens)
	stream := false
	if req.Stream != nil {
		stream = *req.Stream
	}
	p.streams = append(p.streams, stream)
	p.lastPrompt = append([]llm.ChatMessage(nil), req.Messages...)
	if p.calls == 1 {
		return llm.ChatResponse{
			Content: `{"type":"final","content":"__NEED_WRITER__","next_steps":["summarize findings","provide concrete next steps"]}`,
		}, nil
	}
	return llm.ChatResponse{
		Content:      "detailed final output",
		FinishReason: "stop",
	}, nil
}

func (*plannerWriterProvider) ChatStream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	return nil, io.EOF
}

type toolFailureRecoveryProvider struct {
	calls   int
	sawHint bool
}

func (p *toolFailureRecoveryProvider) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		return llm.ChatResponse{Content: `{"type":"tool_call","tool":{"name":"read","args":{"path":"missing.txt"}}}`}, nil
	}
	for _, m := range req.Messages {
		if m.Role != "system" {
			continue
		}
		if strings.Contains(m.Content, "Tool failure feedback:") {
			p.sawHint = true
			break
		}
	}
	return llm.ChatResponse{Content: `{"type":"no_tool","content":"recovered-after-tool-error"}`}, nil
}

func (*toolFailureRecoveryProvider) ChatStream(context.Context, llm.ChatRequest) (llm.Stream, error) {
	return nil, io.EOF
}

type fixedApprover bool

func (f fixedApprover) Approve(context.Context, ApprovalPrompt) (bool, error) {
	return bool(f), nil
}

func setupTestApp(t *testing.T, provider llm.Provider) (*App, string) {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "agent.db")
	store, err := storage.NewBoltStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := config.Config{
		Agents: map[string]config.AgentConfig{
			"build": {
				Mode:        "primary",
				Model:       "stub-model",
				Tools:       []string{"list", "glob", "grep", "read", "edit", "patch", "apply_diff", "bash", "mkdir", "write_file", "verify"},
				Permissions: map[string]string{"*": "allow"},
				Temperature: 0.1,
				MaxTokens:   512,
			},
			"plan": {
				Mode:        "primary",
				Model:       "stub-model",
				Tools:       []string{"list", "glob", "grep", "read", "edit", "patch", "apply_diff", "bash", "mkdir", "write_file", "verify"},
				Permissions: map[string]string{"edit": "ask", "patch": "ask", "bash": "ask", "*": "allow"},
				Temperature: 0.1,
				MaxTokens:   512,
			},
		},
		DefaultAgent:      "build",
		WorkspaceRoot:     root,
		RulesFile:         filepath.Join(root, "AGENTS.md"),
		StoragePath:       dbPath,
		CompactionTurns:   5,
		CompactionTokens:  0,
		ContextTurnWindow: 5,
		ToolOutputLimit:   4096,
		ToolOutputModel:   4096,
		ToolOutputDisplay: 8192,
		RequestTimeout:    30 * time.Second,
		LLMMaxRetries:     0,
	}
	if err := osWrite(filepath.Join(root, "AGENTS.md"), "test-rules"); err != nil {
		t.Fatal(err)
	}
	if err := osWrite(filepath.Join(root, "a.txt"), "hello"); err != nil {
		t.Fatal(err)
	}

	am, err := agent.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	if err := tool.RegisterBuiltins(reg, root, cfg.ToolOutputDisplay); err != nil {
		t.Fatal(err)
	}
	sm := session.NewManager(store, cfg.CompactionTurns, cfg.CompactionTokens)
	b := ctxbuild.NewBuilder(cfg.RulesFile, cfg.WorkspaceRoot, cfg.ContextTurnWindow)
	pe := permission.NewEngine(permission.DecisionAsk)
	app := New(cfg, am, sm, b, pe, reg, provider)
	return app, root
}

func TestHandleTurnAllowFlow(t *testing.T) {
	p := &stubProvider{
		responses: []string{
			`{"type":"tool_call","tool":{"name":"read","args":{"path":"a.txt"}}}`,
			`{"type":"final","content":"done-read"}`,
		},
	}
	a, _ := setupTestApp(t, p)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "read file",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "done-read" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
}

func TestHandleTurnAskDenyStillContinues(t *testing.T) {
	p := &stubProvider{
		responses: []string{
			`{"type":"tool_call","tool":{"name":"edit","args":{"path":"x.txt","content":"abc"}}}`,
			`{"type":"final","content":"done-after-deny"}`,
		},
	}
	a, _ := setupTestApp(t, p)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "edit file",
		Agent: "plan",
	}, fixedApprover(false))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "done-after-deny" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
}

func TestHandleTurnContinuesUntilFinal(t *testing.T) {
	p := &stubProvider{
		responses: []string{
			`{"type":"tool_call","tool":{"name":"read","args":{"path":"a.txt"}}}`,
			`{"type":"tool_call","tool":{"name":"read","args":{"path":"a.txt"}}}`,
			`{"type":"tool_call","tool":{"name":"read","args":{"path":"a.txt"}}}`,
			`{"type":"tool_call","tool":{"name":"read","args":{"path":"a.txt"}}}`,
			`{"type":"tool_call","tool":{"name":"read","args":{"path":"a.txt"}}}`,
		},
	}
	a, _ := setupTestApp(t, p)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "loop forever",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "stub-final" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
}

func TestHandleTurnRateLimitFallback(t *testing.T) {
	p := &rateLimitThenSuccessProvider{}
	a, _ := setupTestApp(t, p)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "test rate limit fallback",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "ok-after-fallback" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
	if p.calls != 2 {
		t.Fatalf("expected 2 llm calls, got %d", p.calls)
	}
}

func TestHandleTurnTimeoutFallback(t *testing.T) {
	p := &timeoutThenSuccessProvider{}
	a, _ := setupTestApp(t, p)
	a.SetRetriever(fixedRetriever{
		value: strings.Repeat("[CONTEXT 1] a.txt:1-1 (score=0.9)\nhello world\n", 260),
	})
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "summarize file",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "ok-after-timeout-fallback" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
	if p.calls != 2 {
		t.Fatalf("expected 2 llm calls, got %d", p.calls)
	}
	if p.secondPromptChars <= 0 || p.firstPromptChars <= 0 {
		t.Fatalf("invalid prompt sizes: first=%d second=%d", p.firstPromptChars, p.secondPromptChars)
	}
	if p.secondPromptChars >= p.firstPromptChars {
		t.Fatalf("expected fallback prompt compaction: first=%d second=%d", p.firstPromptChars, p.secondPromptChars)
	}
}

func TestHandleTurnRecoversAfterPlannerTimeoutError(t *testing.T) {
	p := &plannerTimeoutRecoverProvider{}
	a, _ := setupTestApp(t, p)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "continue the task after planner timeout",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "recovered-after-planner-timeout" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
	if p.calls != 3 {
		t.Fatalf("expected 3 calls (2 timeout + 1 recovery), got %d", p.calls)
	}
}

func TestHandleTurnRepairsInvalidPlannerOutput(t *testing.T) {
	p := &invalidThenRepairProvider{}
	a, _ := setupTestApp(t, p)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "test planner repair",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "repaired-final" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
	if p.calls != 2 {
		t.Fatalf("expected 2 llm calls (invalid + repair), got %d", p.calls)
	}
}

func TestHandleTurnStopsOnInvalidModelLoop(t *testing.T) {
	a, _ := setupTestApp(t, alwaysInvalidProvider{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.HandleTurn(ctx, TurnRequest{
		Input: "invalid forever",
		Agent: "build",
	}, fixedApprover(true))
	if err == nil {
		t.Fatalf("expected error")
	}
	if got := err.Error(); !strings.Contains(got, "invalid structured output") {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestHandleTurnStopsOnRepeatingToolLoop(t *testing.T) {
	a, _ := setupTestApp(t, repeatToolProvider{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.HandleTurn(ctx, TurnRequest{
		Input: "repeat tool forever",
		Agent: "build",
	}, fixedApprover(true))
	if err == nil {
		t.Fatalf("expected error")
	}
	if got := err.Error(); !strings.Contains(got, "repetitive tool-call loop") {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestHandleTurnInjectsRetrievedContext(t *testing.T) {
	p := &contextAwareProvider{}
	a, _ := setupTestApp(t, p)
	a.SetRetriever(fixedRetriever{
		value: "[CONTEXT 1] a.txt:1-1 (score=0.9)\nhello",
	})
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "where is the text",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "ok-with-retrieval" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
	if !p.foundRetrievedContext {
		t.Fatalf("expected retrieved context to be injected into system messages")
	}
}

func TestHandleTurnAutoContinuationOnLength(t *testing.T) {
	p := &continuationProvider{}
	a, _ := setupTestApp(t, p)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "return a final answer",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "continued-ok" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
	if p.calls != 3 {
		t.Fatalf("expected 3 provider calls, got %d", p.calls)
	}
}

func TestAdaptiveMaxTokens(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "system", Content: strings.Repeat("x", 200)},
		{Role: "user", Content: strings.Repeat("y", 200)},
	}
	gotPlan := adaptiveMaxTokens(agent.Definition{Name: "plan", MaxTokens: 6000}, msgs)
	if gotPlan != 6000 {
		t.Fatalf("expected plan max_tokens to stay 6000, got %d", gotPlan)
	}

	longMsgs := []llm.ChatMessage{
		{Role: "system", Content: strings.Repeat("x", 120000)},
	}
	gotLong := adaptiveMaxTokens(agent.Definition{Name: "build", MaxTokens: 16000}, longMsgs)
	if gotLong != 16000 {
		t.Fatalf("expected long prompt to keep 16000, got %d", gotLong)
	}

	gotKeep := adaptiveMaxTokens(agent.Definition{Name: "build", MaxTokens: 7000}, msgs)
	if gotKeep != 7000 {
		t.Fatalf("expected keep 7000, got %d", gotKeep)
	}
}

func TestHandleTurnEmitsEvents(t *testing.T) {
	p := &stubProvider{
		responses: []string{
			`{"type":"tool_call","tool":{"name":"read","args":{"path":"a.txt"}}}`,
			`{"type":"final","content":"done"}`,
		},
	}
	a, _ := setupTestApp(t, p)
	var events []Event
	ctx := WithEventHandler(context.Background(), func(ev Event) {
		events = append(events, ev)
	})
	_, err := a.HandleTurn(ctx, TurnRequest{
		Input: "read a file",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected emitted events")
	}
}

func TestHandleTurnUsesPlannerThenWriter(t *testing.T) {
	p := &plannerWriterProvider{}
	a, _ := setupTestApp(t, p)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "give detailed output",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "detailed final output" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
	if p.calls != 2 {
		t.Fatalf("expected 2 llm calls, got %d", p.calls)
	}
	if len(p.streams) < 2 || p.streams[0] {
		t.Fatalf("expected planner call to be non-stream")
	}
	if !p.streams[1] {
		t.Fatalf("expected writer call to be stream")
	}
	if p.maxTokens[0] > plannerTokenCap {
		t.Fatalf("expected planner max_tokens <= %d, got %d", plannerTokenCap, p.maxTokens[0])
	}
}

func TestNeedsContinuationPatchMarkerDetection(t *testing.T) {
	inline := `final text mentions "*** BEGIN PATCH" in explanation only`
	if needsContinuation("stop", inline) {
		t.Fatalf("inline marker mention should not trigger continuation")
	}
	standalone := "header\n*** BEGIN PATCH\n+line\n"
	if !needsContinuation("stop", standalone) {
		t.Fatalf("standalone patch begin without end should trigger continuation")
	}
}

func TestHandleTurnAskClarificationAction(t *testing.T) {
	p := &stubProvider{
		responses: []string{
			`{"type":"ask_clarification","content":"请补充部署目标平台和账号信息。"}`,
		},
	}
	a, _ := setupTestApp(t, p)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "帮我部署这个项目",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if !strings.Contains(resp.Output, "部署目标平台") {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
}

func TestHandleTurnNoToolAction(t *testing.T) {
	p := &stubProvider{
		responses: []string{
			`{"type":"no_tool","content":"这里不需要工具，直接给你答案。"}`,
		},
	}
	a, _ := setupTestApp(t, p)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "把这句话改得更顺一点",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if !strings.Contains(resp.Output, "不需要工具") {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
}

func TestHandleTurnToolFailureFeedsBackToPlanner(t *testing.T) {
	p := &toolFailureRecoveryProvider{}
	a, _ := setupTestApp(t, p)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "read and summarize",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "recovered-after-tool-error" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
	if !p.sawHint {
		t.Fatalf("expected tool failure hint in planner messages")
	}
}

func TestHandleTurnRefreshesRetrievalEachPlannerStep(t *testing.T) {
	p := &toolFailureRecoveryProvider{}
	a, _ := setupTestApp(t, p)
	r := &trackingRetriever{value: "[CONTEXT 1] a.txt:1-1 (score=0.9)\nhello"}
	a.SetRetriever(r)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "read and summarize",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "recovered-after-tool-error" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
	if r.calls < 2 {
		t.Fatalf("expected retrieval to refresh each planner step, calls=%d", r.calls)
	}
	foundErrorAwareQuery := false
	for _, q := range r.queries {
		if strings.Contains(strings.ToLower(q), "recent_error:") {
			foundErrorAwareQuery = true
			break
		}
	}
	if !foundErrorAwareQuery {
		t.Fatalf("expected refreshed retrieval query to include recent_error context; queries=%v", r.queries)
	}
}

func TestHandleTurnAutoVerifyAfterWrite(t *testing.T) {
	p := &stubProvider{
		responses: []string{
			`{"type":"tool_call","tool":{"name":"edit","args":{"path":"a.txt","content":"updated"}}}`,
			`{"type":"final","content":"done-after-write"}`,
		},
	}
	a, _ := setupTestApp(t, p)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "update file then finish",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "done-after-write" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}
	sess, err := a.GetSession(context.Background(), resp.SessionID)
	if err != nil {
		t.Fatalf("get session failed: %v", err)
	}
	if len(sess.Turns) == 0 {
		t.Fatalf("expected turns")
	}
	last := sess.Turns[len(sess.Turns)-1]
	foundVerify := false
	for _, r := range last.Results {
		if r.Name == "verify" {
			foundVerify = true
			break
		}
	}
	if !foundVerify {
		t.Fatalf("expected auto verify result after write")
	}
}

func TestHandleTurnPhaseGateRequiresProjectVerifyAndWritesSummary(t *testing.T) {
	p := &stubProvider{
		responses: []string{
			`{"type":"tool_call","tool":{"name":"edit","args":{"path":"a.txt","content":"phase-1-change"}}}`,
			`{"type":"final","content":"phase complete"}`,
		},
	}
	a, root := setupTestApp(t, p)
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "按 1 个阶段完成：先改文件，再给最终结果",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	if resp.Output != "phase complete" {
		t.Fatalf("unexpected output: %q", resp.Output)
	}

	sess, err := a.GetSession(context.Background(), resp.SessionID)
	if err != nil {
		t.Fatalf("get session failed: %v", err)
	}
	if len(sess.Turns) == 0 {
		t.Fatalf("expected turns")
	}
	last := sess.Turns[len(sess.Turns)-1]
	verifyCount := 0
	for _, r := range last.Results {
		if r.Name == "verify" {
			verifyCount++
		}
	}
	if verifyCount < 2 {
		t.Fatalf("expected auto changed_files verify + phase project verify, got %d", verifyCount)
	}

	decisionLog := filepath.Join(root, "docs", "decision-log.md")
	raw, err := os.ReadFile(decisionLog)
	if err != nil {
		t.Fatalf("expected decision log to be written: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "Phase 1/1") {
		t.Fatalf("expected phase summary in decision log, got: %q", text)
	}
}

func TestHandleTurnPersistsSessionActiveRoot(t *testing.T) {
	p := &stubProvider{
		responses: []string{
			`{"type":"final","content":"ok"}`,
		},
	}
	a, root := setupTestApp(t, p)
	if err := os.MkdirAll(filepath.Join(root, "projects", "trade-lab"), 0o755); err != nil {
		t.Fatal(err)
	}
	resp, err := a.HandleTurn(context.Background(), TurnRequest{
		Input: "please continue work under projects/trade-lab and just summarize current status",
		Agent: "build",
	}, fixedApprover(true))
	if err != nil {
		t.Fatalf("handle turn failed: %v", err)
	}
	sess, err := a.GetSession(context.Background(), resp.SessionID)
	if err != nil {
		t.Fatalf("get session failed: %v", err)
	}
	if sess.ActiveRoot != "projects/trade-lab" {
		t.Fatalf("expected session active_root=projects/trade-lab, got %q", sess.ActiveRoot)
	}
}

func TestBuildPhaseVerifyFailureSummaryTemplate(t *testing.T) {
	summary := buildPhaseVerifyFailureSummary(
		2,
		6,
		errors.New("verify failed in .: command \"go test ./...\": exit status 1"),
		"$ go test ./...\ncmd/server/main.go:12:2: undefined: badSymbol\nFAIL\ttrade-lab/cmd/server [build failed]",
		[]string{"projects/trade-lab/cmd/server/main.go", "projects/trade-lab/internal/config/config.go"},
	)
	required := []string{
		"Phase 2/6 verify(project) failed.",
		"Root error:",
		"Changed files in this phase:",
		"Key diagnostics:",
		"Next action:",
	}
	for _, needle := range required {
		if !strings.Contains(summary, needle) {
			t.Fatalf("expected summary to contain %q, got: %s", needle, summary)
		}
	}
}

func osWrite(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func testPromptChars(msgs []llm.ChatMessage) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Role)
		total += len(m.Content)
	}
	return total
}
