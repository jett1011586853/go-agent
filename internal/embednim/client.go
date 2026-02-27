package embednim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		timeout = 120 * time.Second
	}
	return &Client{
		BaseURL: baseURL,
		Model:   strings.TrimSpace(model),
		HTTP: &http.Client{
			Timeout: timeout,
		},
	}
}

type embedReq struct {
	Input     []string `json:"input"`
	Model     string   `json:"model"`
	InputType string   `json:"input_type"`
	Modality  string   `json:"modality"`
}

type embedResp struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

func (c *Client) Embed(ctx context.Context, texts []string, inputType string) ([][]float64, error) {
	if c == nil {
		return nil, fmt.Errorf("nil embedding client")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("embedding base url is empty")
	}
	if strings.TrimSpace(c.Model) == "" {
		return nil, fmt.Errorf("embedding model is empty")
	}
	if len(texts) == 0 {
		return nil, nil
	}
	inputType = strings.TrimSpace(strings.ToLower(inputType))
	if inputType == "" {
		inputType = "document"
	}

	reqBody, err := json.Marshal(embedReq{
		Input:     texts,
		Model:     c.Model,
		InputType: inputType,
		Modality:  "text",
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/embeddings", bytes.NewReader(reqBody))
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, fmt.Errorf("embed request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out embedResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("embed response has no data")
	}

	vecs := make([][]float64, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			continue
		}
		vecs[d.Index] = d.Embedding
	}
	for i := range vecs {
		if len(vecs[i]) == 0 {
			return nil, fmt.Errorf("embed response missing vector at index %d", i)
		}
	}
	return vecs, nil
}
