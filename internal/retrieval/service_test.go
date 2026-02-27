package retrieval

import (
	"context"
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
