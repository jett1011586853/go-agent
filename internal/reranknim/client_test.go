package reranknim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRerankUsesRankingEndpointAndParsesOrder(t *testing.T) {
	var gotPath string
	var gotModel string
	var gotQuery string
	var gotPassages int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = r.Body.Close()
		gotModel, _ = req["model"].(string)
		if q, ok := req["query"].(map[string]any); ok {
			gotQuery, _ = q["text"].(string)
		}
		if ps, ok := req["passages"].([]any); ok {
			gotPassages = len(ps)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rankings":[{"index":2,"logit":0.9},{"index":0,"logit":0.8},{"index":1,"logit":0.7}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "nvidia/llama-3.2-nv-rerankqa-1b-v2", 5*time.Second)
	order, err := c.Rerank(context.Background(), "where is auth", []string{"a", "b", "c"}, 2)
	if err != nil {
		t.Fatalf("rerank failed: %v", err)
	}
	if gotPath != "/v1/ranking" {
		t.Fatalf("unexpected endpoint: %s", gotPath)
	}
	if gotModel == "" || gotQuery == "" || gotPassages != 3 {
		t.Fatalf("unexpected request payload: model=%q query=%q passages=%d", gotModel, gotQuery, gotPassages)
	}
	if len(order) != 2 || order[0] != 2 || order[1] != 0 {
		t.Fatalf("unexpected order: %#v", order)
	}
}

func TestRerankSupportsDataShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":1,"score":0.99},{"index":0,"score":0.5}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "model", 5*time.Second)
	order, err := c.Rerank(context.Background(), "q", []string{"a", "b"}, 0)
	if err != nil {
		t.Fatalf("rerank failed: %v", err)
	}
	if len(order) != 2 || order[0] != 1 || order[1] != 0 {
		t.Fatalf("unexpected order: %#v", order)
	}
}
