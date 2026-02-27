package app

import (
	"context"
	"errors"
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

type alwaysInvalidProvider struct{}

func (alwaysInvalidProvider) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{Content: "not a json object"}, nil
}

type repeatToolProvider struct{}

func (repeatToolProvider) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{Content: `{"type":"tool_call","tool":{"name":"read","args":{"path":"a.txt"}}}`}, nil
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

type fixedRetriever struct {
	value string
}

func (r fixedRetriever) Retrieve(context.Context, string) (string, error) {
	return r.value, nil
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

func osWrite(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
