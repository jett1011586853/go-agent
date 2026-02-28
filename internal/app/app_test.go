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

type alwaysInvalidProvider struct{}

func (alwaysInvalidProvider) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{Content: "not a json object"}, nil
}

func (alwaysInvalidProvider) ChatStream(context.Context, llm.ChatRequest) (llm.Stream, error) {
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
				Tools:       []string{"list", "glob", "grep", "read", "edit", "patch", "bash"},
				Permissions: map[string]string{"*": "allow"},
				Temperature: 0.1,
				MaxTokens:   512,
			},
			"plan": {
				Mode:        "primary",
				Model:       "stub-model",
				Tools:       []string{"list", "glob", "grep", "read", "edit", "patch", "bash"},
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
	if err := tool.RegisterBuiltins(reg, root, cfg.ToolOutputLimit); err != nil {
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

func osWrite(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
