package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestBoltStorePersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent.db")
	s1, err := NewBoltStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	want := []byte(`{"id":"sess_1","turns":[{"id":"t1"}]}`)
	if err := s1.SaveSession(context.Background(), "sess_1", want); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := NewBoltStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	got, err := s2.LoadSession(context.Background(), "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("unexpected payload: got=%s want=%s", string(got), string(want))
	}
}
