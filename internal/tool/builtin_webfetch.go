package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type webfetchTool struct {
	baseTool
	client *http.Client
}

func newWebfetchTool(base baseTool) *webfetchTool {
	return &webfetchTool{
		baseTool: base,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *webfetchTool) Name() string { return "webfetch" }
func (t *webfetchTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`)
}
func (t *webfetchTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := parseJSONArgs(args, &in); err != nil {
		return Result{}, err
	}
	in.URL = strings.TrimSpace(in.URL)
	if in.URL == "" {
		return Result{}, fmt.Errorf("url is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	out := fmt.Sprintf("status: %d\ncontent-type: %s\n\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), string(body))
	return Result{Output: t.trimOutput(out)}, nil
}
