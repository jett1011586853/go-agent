package retrieval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type queryOnlyEmbedder struct {
	query []float64
}

func (q queryOnlyEmbedder) Embed(_ context.Context, texts []string, inputType string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	if strings.EqualFold(strings.TrimSpace(inputType), "query") {
		for i := range out {
			out[i] = append([]float64(nil), q.query...)
		}
		return out, nil
	}
	for i := range out {
		out[i] = []float64{1, 0}
	}
	return out, nil
}

type staticEmbedder struct{}

func (staticEmbedder) Embed(_ context.Context, texts []string, inputType string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i, txt := range texts {
		size := float64(len([]rune(txt)))
		if strings.EqualFold(strings.TrimSpace(inputType), "query") {
			out[i] = []float64{1, 0}
			continue
		}
		out[i] = []float64{size + 1, 1}
	}
	return out, nil
}

type reverseReranker struct{}

func (reverseReranker) Rerank(_ context.Context, _ string, docs []string, topN int) ([]int, error) {
	order := make([]int, 0, len(docs))
	for i := len(docs) - 1; i >= 0; i-- {
		order = append(order, i)
	}
	if topN > 0 && len(order) > topN {
		order = order[:topN]
	}
	return order, nil
}

type failingReranker struct{}

func (failingReranker) Rerank(_ context.Context, _ string, _ []string, _ int) ([]int, error) {
	return nil, context.DeadlineExceeded
}

func TestTopKWithDiversity(t *testing.T) {
	chunks := []Chunk{
		{Path: "a.go", StartLine: 1, EndLine: 20, Text: "a1", Vec: []float64{0.90, 0.40}},
		{Path: "a.go", StartLine: 21, EndLine: 40, Text: "a2", Vec: []float64{0.80, 0.50}},
		{Path: "a.go", StartLine: 41, EndLine: 60, Text: "a3", Vec: []float64{0.70, 0.60}},
		{Path: "b.go", StartLine: 1, EndLine: 20, Text: "b1", Vec: []float64{0.95, 0.10}},
		{Path: "c.go", StartLine: 1, EndLine: 20, Text: "c1", Vec: []float64{0.60, 0.80}},
	}
	got := topKWithDiversity([]float64{1, 0}, chunks, 4, 2)
	if len(got) != 4 {
		t.Fatalf("expected 4 results, got %d", len(got))
	}
	countA := 0
	for _, it := range got {
		if it.Chunk.Path == "a.go" {
			countA++
		}
	}
	if countA > 2 {
		t.Fatalf("expected per-file diversity limit 2, got a.go=%d", countA)
	}
	if got[0].Chunk.Path != "b.go" {
		t.Fatalf("expected top result from b.go, got %s", got[0].Chunk.Path)
	}
}

func TestSaveLoadJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idx.jsonl")
	in := []Chunk{
		{Path: "x.go", StartLine: 1, EndLine: 10, Text: "package x", Vec: []float64{0.1, 0.2}},
		{Path: "y.go", StartLine: 20, EndLine: 40, Text: "package y", Vec: []float64{0.3, 0.4}},
	}
	if err := saveJSONL(path, in); err != nil {
		t.Fatalf("save index: %v", err)
	}
	out, err := loadJSONL(path)
	if err != nil {
		t.Fatalf("load index: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("expected %d chunks, got %d", len(in), len(out))
	}
	if out[0].Path != in[0].Path || out[1].Path != in[1].Path {
		t.Fatalf("unexpected roundtrip chunks")
	}
}

func TestServiceRetrieveFormatsContext(t *testing.T) {
	svc := NewService(queryOnlyEmbedder{query: []float64{1, 0}}, DefaultOptions(t.TempDir()))
	svc.chunks = []Chunk{
		{Path: "pkg/auth.go", StartLine: 10, EndLine: 30, Text: "func Auth() {}", Vec: []float64{0.9, 0.1}},
		{Path: "pkg/user.go", StartLine: 1, EndLine: 20, Text: "func User() {}", Vec: []float64{0.6, 0.1}},
	}
	ctx := context.Background()
	out, err := svc.Retrieve(ctx, "where is auth middleware")
	if err != nil {
		t.Fatalf("retrieve failed: %v", err)
	}
	if !strings.Contains(out, "[CONTEXT 1] pkg/auth.go:10-30") {
		t.Fatalf("unexpected retrieve output: %s", out)
	}
}

func TestServiceRetrieveAppliesRerankOrder(t *testing.T) {
	svc := NewService(queryOnlyEmbedder{query: []float64{1, 0}}, DefaultOptions(t.TempDir()))
	svc.SetReranker(reverseReranker{})
	svc.chunks = []Chunk{
		{Path: "pkg/a.go", StartLine: 1, EndLine: 10, Text: "A", Vec: []float64{0.95, 0.1}},
		{Path: "pkg/b.go", StartLine: 1, EndLine: 10, Text: "B", Vec: []float64{0.90, 0.1}},
	}
	out, err := svc.Retrieve(context.Background(), "where")
	if err != nil {
		t.Fatalf("retrieve failed: %v", err)
	}
	if !strings.Contains(out, "[CONTEXT 1] pkg/b.go:1-10") {
		t.Fatalf("expected reranked top context from pkg/b.go, got: %s", out)
	}
}

func TestServiceRetrieveFallsBackWhenRerankFails(t *testing.T) {
	svc := NewService(queryOnlyEmbedder{query: []float64{1, 0}}, DefaultOptions(t.TempDir()))
	svc.SetReranker(failingReranker{})
	svc.chunks = []Chunk{
		{Path: "pkg/a.go", StartLine: 1, EndLine: 10, Text: "A", Vec: []float64{0.95, 0.1}},
		{Path: "pkg/b.go", StartLine: 1, EndLine: 10, Text: "B", Vec: []float64{0.90, 0.1}},
	}
	out, err := svc.Retrieve(context.Background(), "where")
	if err != nil {
		t.Fatalf("retrieve failed: %v", err)
	}
	if !strings.Contains(out, "[CONTEXT 1] pkg/a.go:1-10") {
		t.Fatalf("expected fallback to embedding order, got: %s", out)
	}
}

func TestServiceOnToolResultReindexesEditedPath(t *testing.T) {
	root := t.TempDir()
	opts := DefaultOptions(root)
	opts.IndexPath = filepath.Join(root, "idx.jsonl")
	opts.ChunkLines = 20
	opts.ChunkOverlap = 5
	opts.BatchSize = 4

	file := filepath.Join(root, "a.go")
	if err := os.WriteFile(file, []byte("package main\n\nfunc A(){\nprintln(\"old\")\n}\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	svc := NewService(staticEmbedder{}, opts)
	if err := svc.LoadOrBuild(context.Background(), true); err != nil {
		t.Fatalf("load/build: %v", err)
	}

	if err := os.WriteFile(file, []byte("package main\n\nfunc A(){\nprintln(\"new\")\n}\n"), 0o644); err != nil {
		t.Fatalf("rewrite file: %v", err)
	}
	args, _ := json.Marshal(map[string]any{"path": "a.go"})
	if err := svc.OnToolResult(context.Background(), "edit", args, nil); err != nil {
		t.Fatalf("on tool result: %v", err)
	}
	svc.mu.RLock()
	defer svc.mu.RUnlock()
	found := false
	for _, ch := range svc.chunks {
		if ch.Path == "a.go" && strings.Contains(ch.Text, "println(\"new\")") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected updated chunk text after reindex")
	}
}
