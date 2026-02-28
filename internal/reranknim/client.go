package reranknim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	Model   string
	HTTP    *http.Client
}

func New(baseURL, model string, timeout time.Duration) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Client{
		BaseURL: baseURL,
		Model:   strings.TrimSpace(model),
		HTTP: &http.Client{
			Timeout: timeout,
		},
	}
}

type rankingReq struct {
	Model string `json:"model"`
	Query struct {
		Text string `json:"text"`
	} `json:"query"`
	Passages []struct {
		Text string `json:"text"`
	} `json:"passages"`
	Truncate string `json:"truncate,omitempty"`
}

type rankingResp struct {
	Rankings []rankingItem `json:"rankings"`
	Data     []rankingItem `json:"data"`
	Results  []rankingItem `json:"results"`
}

type rankingItem struct {
	Index          int     `json:"index"`
	Score          float64 `json:"score"`
	Logit          float64 `json:"logit"`
	RelevanceScore float64 `json:"relevance_score"`
}

func (c *Client) Rerank(ctx context.Context, query string, docs []string, topN int) ([]int, error) {
	if c == nil {
		return nil, fmt.Errorf("nil rerank client")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("rerank base url is empty")
	}
	if strings.TrimSpace(c.Model) == "" {
		return nil, fmt.Errorf("rerank model is empty")
	}
	query = strings.TrimSpace(query)
	if query == "" || len(docs) == 0 {
		return nil, nil
	}
	reqBody := rankingReq{
		Model: c.Model,
		Passages: make([]struct {
			Text string `json:"text"`
		}, 0, len(docs)),
		Truncate: "END",
	}
	reqBody.Query.Text = query
	for _, doc := range docs {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}
		reqBody.Passages = append(reqBody.Passages, struct {
			Text string `json:"text"`
		}{Text: doc})
	}
	if len(reqBody.Passages) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/ranking", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
		return nil, fmt.Errorf("rerank request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out rankingResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	items := out.Rankings
	if len(items) == 0 {
		items = out.Data
	}
	if len(items) == 0 {
		items = out.Results
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("rerank response has no rankings")
	}

	allZeroIndex := true
	for _, it := range items {
		if it.Index != 0 {
			allZeroIndex = false
			break
		}
	}
	if allZeroIndex && len(items) == len(reqBody.Passages) {
		for i := range items {
			items[i].Index = i
		}
	}

	sort.SliceStable(items, func(i, j int) bool {
		si := rankingScore(items[i])
		sj := rankingScore(items[j])
		if si == sj {
			return items[i].Index < items[j].Index
		}
		return si > sj
	})

	order := make([]int, 0, len(items))
	seen := map[int]struct{}{}
	for _, it := range items {
		if it.Index < 0 || it.Index >= len(reqBody.Passages) {
			continue
		}
		if _, ok := seen[it.Index]; ok {
			continue
		}
		seen[it.Index] = struct{}{}
		order = append(order, it.Index)
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("rerank response has no valid indexes")
	}
	if topN > 0 && len(order) > topN {
		order = order[:topN]
	}
	return order, nil
}

func rankingScore(it rankingItem) float64 {
	if it.Score != 0 {
		return it.Score
	}
	if it.RelevanceScore != 0 {
		return it.RelevanceScore
	}
	return it.Logit
}
