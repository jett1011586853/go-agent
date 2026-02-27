package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type NVIDIAProvider struct {
	baseURL        string
	apiKey         string
	httpClient     *http.Client
	maxRetries     int
	requestTimeout time.Duration
	stream         bool
	enableThinking bool
	clearThinking  bool
}

func NewNVIDIAProvider(
	baseURL string,
	apiKey string,
	timeout time.Duration,
	maxRetries int,
	stream bool,
	enableThinking bool,
	clearThinking bool,
) *NVIDIAProvider {
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxRetries > 6 {
		maxRetries = 6
	}
	return &NVIDIAProvider{
		baseURL:        strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:         strings.TrimSpace(apiKey),
		httpClient:     &http.Client{},
		maxRetries:     maxRetries,
		requestTimeout: timeout,
		stream:         stream,
		enableThinking: enableThinking,
		clearThinking:  clearThinking,
	}
}

func (p *NVIDIAProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if p.apiKey == "" {
		return ChatResponse{}, errors.New("NVIDIA_API_KEY is required")
	}
	if req.Model == "" {
		return ChatResponse{}, errors.New("model is required")
	}
	if len(req.Messages) == 0 {
		return ChatResponse{}, errors.New("messages cannot be empty")
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 1200
	}

	payload := map[string]any{
		"model":       req.Model,
		"messages":    req.Messages,
		"temperature": req.Temperature,
		"top_p":       1,
		"max_tokens":  req.MaxTokens,
	}
	stream := p.stream
	if req.Stream != nil {
		stream = *req.Stream
	}
	enableThinking := p.enableThinking
	if req.EnableThinking != nil {
		enableThinking = *req.EnableThinking
	}
	clearThinking := p.clearThinking
	if req.ClearThinking != nil {
		clearThinking = *req.ClearThinking
	}
	payload["stream"] = stream
	payload["chat_template_kwargs"] = map[string]any{
		"enable_thinking": enableThinking,
		"clear_thinking":  clearThinking,
	}
	rawBody, err := json.Marshal(payload)
	if err != nil {
		return ChatResponse{}, err
	}

	url := p.baseURL + "/chat/completions"
	for attempt := 0; ; attempt++ {
		reqCtx := ctx
		var cancel context.CancelFunc
		if !stream && p.requestTimeout > 0 {
			reqCtx, cancel = context.WithTimeout(ctx, p.requestTimeout)
		}
		httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(rawBody))
		if err != nil {
			if cancel != nil {
				cancel()
			}
			return ChatResponse{}, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json")

		resp, reqErr := p.httpClient.Do(httpReq)
		if reqErr != nil {
			if p.shouldRetry(attempt, 0, reqErr) {
				if err := waitBackoff(ctx, attempt, 0, ""); err != nil {
					if cancel != nil {
						cancel()
					}
					return ChatResponse{}, err
				}
				if cancel != nil {
					cancel()
				}
				continue
			}
			if cancel != nil {
				cancel()
			}
			return ChatResponse{}, fmt.Errorf("chat request failed: %w", reqErr)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
			retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
			_ = resp.Body.Close()
			if p.shouldRetry(attempt, resp.StatusCode, nil) {
				if err := waitBackoff(ctx, attempt, resp.StatusCode, retryAfter); err != nil {
					if cancel != nil {
						cancel()
					}
					return ChatResponse{}, err
				}
				if cancel != nil {
					cancel()
				}
				continue
			}
			if resp.StatusCode == http.StatusTooManyRequests {
				if cancel != nil {
					cancel()
				}
				return ChatResponse{}, fmt.Errorf("chat request HTTP 429 (rate limited). Reduce max_tokens or retry later: %s", string(body))
			}
			if cancel != nil {
				cancel()
			}
			return ChatResponse{}, fmt.Errorf("chat request HTTP %d: %s", resp.StatusCode, string(body))
		}

		out, parseErr := parseResponse(resp.Body, stream)
		_ = resp.Body.Close()
		if parseErr == nil {
			if cancel != nil {
				cancel()
			}
			return out, nil
		}

		if p.shouldRetry(attempt, 0, parseErr) {
			if err := waitBackoff(ctx, attempt, 0, ""); err != nil {
				if cancel != nil {
					cancel()
				}
				return ChatResponse{}, err
			}
			if cancel != nil {
				cancel()
			}
			continue
		}
		if cancel != nil {
			cancel()
		}
		return ChatResponse{}, parseErr
	}
}

func parseResponse(body io.Reader, stream bool) (ChatResponse, error) {
	if stream {
		return parseStreamResponse(body)
	}
	return parseJSONResponse(body)
}

func parseJSONResponse(body io.Reader) (ChatResponse, error) {
	raw, err := io.ReadAll(io.LimitReader(body, 8*1024*1024))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read chat response: %w", err)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content          any    `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ChatResponse{}, fmt.Errorf("decode chat response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return ChatResponse{}, errors.New("empty chat choices")
	}
	return ChatResponse{
		Content:   coerceContent(parsed.Choices[0].Message.Content),
		Reasoning: strings.TrimSpace(parsed.Choices[0].Message.ReasoningContent),
	}, nil
}

func parseStreamResponse(body io.Reader) (ChatResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var content strings.Builder
	var reasoning strings.Builder

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if chunk.Choices[0].Delta.ReasoningContent != "" {
			reasoning.WriteString(chunk.Choices[0].Delta.ReasoningContent)
		}
		if chunk.Choices[0].Delta.Content != "" {
			content.WriteString(chunk.Choices[0].Delta.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		return ChatResponse{}, fmt.Errorf("read stream response: %w", err)
	}
	if content.Len() == 0 && reasoning.Len() == 0 {
		return ChatResponse{}, errors.New("empty stream response")
	}
	return ChatResponse{
		Content:   content.String(),
		Reasoning: reasoning.String(),
	}, nil
}

func (p *NVIDIAProvider) shouldRetry(attempt int, statusCode int, reqErr error) bool {
	if attempt >= p.maxRetries {
		return false
	}
	if reqErr != nil {
		if errors.Is(reqErr, context.DeadlineExceeded) || errors.Is(reqErr, context.Canceled) {
			return false
		}
		var ne net.Error
		if errors.As(reqErr, &ne) {
			return true
		}
		return true
	}
	switch statusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return statusCode >= 500
	}
}

func waitBackoff(ctx context.Context, attempt int, statusCode int, retryAfter string) error {
	delay := parseRetryAfter(retryAfter)
	if delay <= 0 {
		switch statusCode {
		case http.StatusTooManyRequests:
			delay = time.Duration((attempt+1)*2) * time.Second
			if delay > 20*time.Second {
				delay = 20 * time.Second
			}
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			delay = time.Duration(attempt+1) * time.Second
			if delay > 10*time.Second {
				delay = 10 * time.Second
			}
		default:
			delay = time.Duration(attempt+1) * 300 * time.Millisecond
		}
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if sec, err := time.ParseDuration(v + "s"); err == nil && sec > 0 {
		if sec > 2*time.Minute {
			return 2 * time.Minute
		}
		return sec
	}
	if ts, err := http.ParseTime(v); err == nil {
		d := time.Until(ts)
		if d <= 0 {
			return 0
		}
		if d > 2*time.Minute {
			return 2 * time.Minute
		}
		return d
	}
	return 0
}

func coerceContent(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []any:
		var b strings.Builder
		for _, item := range val {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := m["text"].(string); ok {
				b.WriteString(s)
			}
		}
		return b.String()
	default:
		return ""
	}
}
