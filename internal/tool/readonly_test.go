package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadToolPathSafety(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, root, 4096); err != nil {
		t.Fatal(err)
	}

	_, err := reg.Run(context.Background(), "read", json.RawMessage(`{"path":"../outside.txt"}`))
	if err == nil {
		t.Fatalf("expected path traversal to fail")
	}

	res, err := reg.Run(context.Background(), "read", json.RawMessage(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatalf("expected read success, got %v", err)
	}
	if res.Output == "" {
		t.Fatalf("expected read output")
	}
}

func TestEditAndPatchTools(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(target, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	if err := RegisterBuiltins(reg, root, 4096); err != nil {
		t.Fatal(err)
	}

	_, err := reg.Run(context.Background(), "edit", json.RawMessage(`{"path":"sample.txt","content":"line1\nline2"}`))
	if err != nil {
		t.Fatalf("edit failed: %v", err)
	}

	res, err := reg.Run(context.Background(), "patch", json.RawMessage(`{"path":"sample.txt","search":"line2","replace":"lineB","all":false}`))
	if err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	if res.Output == "" {
		t.Fatalf("expected patch output")
	}

	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "line1\nlineB" {
		t.Fatalf("unexpected patched content: %q", string(raw))
	}
}

func TestWebfetchTool(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, root, 4096); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok-webfetch"))
	}))
	defer srv.Close()

	args := json.RawMessage(`{"url":"` + srv.URL + `"}`)
	res, err := reg.Run(context.Background(), "webfetch", args)
	if err != nil {
		t.Fatalf("webfetch failed: %v", err)
	}
	if res.Output == "" || !strings.Contains(res.Output, "ok-webfetch") {
		t.Fatalf("unexpected webfetch output: %q", res.Output)
	}
}
