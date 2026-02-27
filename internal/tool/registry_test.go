package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type stubTool struct{}

func (stubTool) Name() string   { return "stub" }
func (stubTool) Schema() []byte { return []byte(`{"type":"object"}`) }
func (stubTool) Run(context.Context, json.RawMessage) (Result, error) {
	return Result{Output: "ok"}, nil
}

type stubHook struct {
	beforeCalled bool
	afterCalled  bool
	beforeErr    error
	afterErr     error
}

func (h *stubHook) BeforeRun(context.Context, string, json.RawMessage) error {
	h.beforeCalled = true
	return h.beforeErr
}

func (h *stubHook) AfterRun(context.Context, string, json.RawMessage, Result, error) error {
	h.afterCalled = true
	return h.afterErr
}

func TestRegistryRunHooks(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(stubTool{}); err != nil {
		t.Fatal(err)
	}
	h := &stubHook{}
	if err := reg.RegisterHook(h); err != nil {
		t.Fatal(err)
	}
	res, err := reg.Run(context.Background(), "stub", nil)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if res.Output != "ok" {
		t.Fatalf("unexpected output: %q", res.Output)
	}
	if !h.beforeCalled || !h.afterCalled {
		t.Fatalf("expected both hooks to be called, before=%v after=%v", h.beforeCalled, h.afterCalled)
	}
}

func TestRegistryBeforeHookStopsRun(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(stubTool{}); err != nil {
		t.Fatal(err)
	}
	h := &stubHook{beforeErr: errors.New("blocked")}
	if err := reg.RegisterHook(h); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Run(context.Background(), "stub", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !h.beforeCalled {
		t.Fatal("expected before hook to run")
	}
	if h.afterCalled {
		t.Fatal("after hook should not run when before hook fails")
	}
}

func TestRegistryAfterHookErrorPropagates(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(stubTool{}); err != nil {
		t.Fatal(err)
	}
	h := &stubHook{afterErr: errors.New("after failed")}
	if err := reg.RegisterHook(h); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Run(context.Background(), "stub", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !h.beforeCalled || !h.afterCalled {
		t.Fatalf("expected hooks to run, before=%v after=%v", h.beforeCalled, h.afterCalled)
	}
}
