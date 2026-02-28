package embednim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEmbedMapsDocumentToPassage(t *testing.T) {
	var gotInputType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = r.Body.Close()
		if v, ok := req["input_type"].(string); ok {
			gotInputType = v
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2],"index":0}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "model", 5*time.Second)
	_, err := c.Embed(context.Background(), []string{"hello"}, "document")
	if err != nil {
		t.Fatalf("embed failed: %v", err)
	}
	if gotInputType != "passage" {
		t.Fatalf("expected input_type=passage, got %q", gotInputType)
	}
}
