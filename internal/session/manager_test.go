package session

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-agent/internal/message"
	"go-agent/internal/storage"
)

func TestCompactionByTurnCount(t *testing.T) {
	store := newStore(t)
	mgr := NewManager(store, 6, 0)

	sess, err := mgr.CreateSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		_, err := mgr.AppendTurn(context.Background(), sess.ID, sampleTurn(fmt.Sprintf("turn-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := mgr.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) >= 7 {
		t.Fatalf("expected compaction to reduce turns, got=%d", len(got.Turns))
	}
	if len(got.ArchivedTurns) == 0 {
		t.Fatal("expected archived turns")
	}
	if strings.TrimSpace(got.WorkingSummary) == "" {
		t.Fatal("expected working summary to be generated")
	}
}

func TestCompactionByTokenThreshold(t *testing.T) {
	store := newStore(t)
	mgr := NewManager(store, 100, 120)

	sess, err := mgr.CreateSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("must keep this constraint for later reasoning. ", 20)
	for i := 0; i < 6; i++ {
		_, err := mgr.AppendTurn(context.Background(), sess.ID, sampleTurn(large+fmt.Sprintf(" %d", i)))
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := mgr.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ArchivedTurns) == 0 {
		t.Fatal("expected token-triggered compaction to archive old turns")
	}
	if len(got.PinnedFacts) == 0 {
		t.Fatal("expected pinned facts after compaction")
	}
}

func TestUpdateActiveRootPersists(t *testing.T) {
	store := newStore(t)
	mgr := NewManager(store, 6, 0)

	sess, err := mgr.CreateSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	updated, err := mgr.UpdateActiveRoot(context.Background(), sess.ID, `projects\trade-lab`)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ActiveRoot != "projects/trade-lab" {
		t.Fatalf("expected normalized active_root, got %q", updated.ActiveRoot)
	}
	loaded, err := mgr.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ActiveRoot != "projects/trade-lab" {
		t.Fatalf("expected persisted active_root, got %q", loaded.ActiveRoot)
	}
}

func sampleTurn(input string) message.Turn {
	now := time.Now().UTC()
	return message.Turn{
		ID:        fmt.Sprintf("t-%d", now.UnixNano()),
		Agent:     "build",
		UserInput: input,
		Messages: []message.Message{
			{
				Role:      message.RoleUser,
				Content:   input,
				CreatedAt: now,
			},
			{
				Role:      message.RoleAssistant,
				Content:   "ack: " + input,
				CreatedAt: now,
			},
		},
		CreatedAt: now,
	}
}

func newStore(t *testing.T) *storage.BoltStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.db")
	store, err := storage.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}
