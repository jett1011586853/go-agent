package indexer

import (
	"context"
	"sync"
	"testing"
	"time"
)

type stubUpdater struct {
	mu    sync.Mutex
	calls [][]string
}

func (s *stubUpdater) ReindexPaths(_ context.Context, paths []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := append([]string(nil), paths...)
	s.calls = append(s.calls, cp)
	return nil
}

func (s *stubUpdater) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stubUpdater) allPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		out = append(out, c...)
	}
	return out
}

func TestIndexerDebouncedEnqueue(t *testing.T) {
	up := &stubUpdater{}
	svc := NewService(t.TempDir(), up, Options{
		Debounce: 80 * time.Millisecond,
		Timeout:  time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx, func(error) {})

	svc.Enqueue("a.go")
	svc.Enqueue("a.go")
	svc.Enqueue("b.go")
	time.Sleep(220 * time.Millisecond)

	if up.callCount() == 0 {
		t.Fatalf("expected at least one updater call")
	}
	all := up.allPaths()
	seen := map[string]bool{}
	for _, p := range all {
		seen[p] = true
	}
	if !seen["a.go"] || !seen["b.go"] {
		t.Fatalf("expected merged paths a.go and b.go, got %+v", all)
	}
}
