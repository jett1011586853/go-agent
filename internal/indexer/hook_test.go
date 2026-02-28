package indexer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go-agent/internal/tool"
)

func TestToolHookEnqueueEditPath(t *testing.T) {
	up := &stubUpdater{}
	svc := NewService(t.TempDir(), up, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx, func(error) {})

	hook := NewToolHook(svc)
	args, _ := json.Marshal(map[string]any{"path": "pkg/a.go"})
	if err := hook.AfterRun(context.Background(), "edit", args, tool.Result{}, nil); err != nil {
		t.Fatalf("after run: %v", err)
	}
	// Flush asynchronously.
	time.Sleep(900 * time.Millisecond)

	all := up.allPaths()
	found := false
	for _, p := range all {
		if p == "pkg/a.go" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected queued path pkg/a.go, got %+v", all)
	}
}
