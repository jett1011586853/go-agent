package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type websearchTool struct {
	baseTool
	client *http.Client
}

func newWebsearchTool(base baseTool) *websearchTool {
	return &websearchTool{
		baseTool: base,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *websearchTool) Name() string { return "websearch" }
func (t *websearchTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}},"required":["query"]}`)
}

func (t *websearchTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := parseJSONArgs(args, &in); err != nil {
		return Result{}, err
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" {
		return Result{}, fmt.Errorf("query is required")
	}
	if in.Limit <= 0 {
		in.Limit = 5
	}
	if in.Limit > 20 {
		in.Limit = 20
	}

	endpoint := "https://api.duckduckgo.com/?format=json&no_html=1&skip_disambig=1&q=" + url.QueryEscape(in.Query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("websearch HTTP %d: %s", resp.StatusCode, string(body))
	}

	var parsed struct {
		Heading       string `json:"Heading"`
		AbstractText  string `json:"AbstractText"`
		AbstractURL   string `json:"AbstractURL"`
		RelatedTopics []struct {
			Text     string `json:"Text"`
			FirstURL string `json:"FirstURL"`
			Name     string `json:"Name"`
			Topics   []struct {
				Text     string `json:"Text"`
				FirstURL string `json:"FirstURL"`
			} `json:"Topics"`
		} `json:"RelatedTopics"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Result{}, fmt.Errorf("decode websearch response: %w", err)
	}

	lines := []string{
		fmt.Sprintf("query: %s", in.Query),
	}
	if strings.TrimSpace(parsed.Heading) != "" {
		lines = append(lines, "heading: "+strings.TrimSpace(parsed.Heading))
	}
	if strings.TrimSpace(parsed.AbstractText) != "" {
		lines = append(lines, "abstract: "+strings.TrimSpace(parsed.AbstractText))
	}
	if strings.TrimSpace(parsed.AbstractURL) != "" {
		lines = append(lines, "abstract_url: "+strings.TrimSpace(parsed.AbstractURL))
	}

	count := 0
	for _, item := range parsed.RelatedTopics {
		if count >= in.Limit {
			break
		}
		if len(item.Topics) > 0 {
			for _, sub := range item.Topics {
				if count >= in.Limit {
					break
				}
				if strings.TrimSpace(sub.Text) == "" {
					continue
				}
				count++
				lines = append(lines, fmt.Sprintf("%d) %s", count, strings.TrimSpace(sub.Text)))
				if strings.TrimSpace(sub.FirstURL) != "" {
					lines = append(lines, "   "+strings.TrimSpace(sub.FirstURL))
				}
			}
			continue
		}
		if strings.TrimSpace(item.Text) == "" {
			continue
		}
		count++
		lines = append(lines, fmt.Sprintf("%d) %s", count, strings.TrimSpace(item.Text)))
		if strings.TrimSpace(item.FirstURL) != "" {
			lines = append(lines, "   "+strings.TrimSpace(item.FirstURL))
		}
	}

	return Result{Output: t.trimOutput(strings.Join(lines, "\n"))}, nil
}
