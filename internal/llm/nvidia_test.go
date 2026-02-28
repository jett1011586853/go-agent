package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNVIDIAProviderRetryThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		raw, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		if payload["stream"] != false {
			t.Fatalf("expected stream=false, got %v", payload["stream"])
		}
		kwargs, ok := payload["chat_template_kwargs"].(map[string]any)
		if !ok {
			t.Fatalf("missing chat_template_kwargs")
		}
		if kwargs["enable_thinking"] != true || kwargs["clear_thinking"] != false {
			t.Fatalf("unexpected kwargs: %+v", kwargs)
		}
		if n < 3 {
			http.Error(w, "try later", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok-final"}}]}`))
	}))
	defer srv.Close()

	p := NewNVIDIAProvider(srv.URL, "test-key", 5*time.Second, 3, false, true, false)
	resp, err := p.Chat(context.Background(), ChatRequest{
		Model:       "z-ai/glm5",
		Messages:    []ChatMessage{{Role: "user", Content: "hi"}},
		Temperature: 0.1,
		MaxTokens:   256,
	})
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if resp.Content != "ok-final" {
		t.Fatalf("unexpected content: %q", resp.Content)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 calls, got %d", got)
	}
}

func TestNVIDIAProviderNoRetryForBadRequest(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	p := NewNVIDIAProvider(srv.URL, "test-key", 5*time.Second, 3, false, true, false)
	_, err := p.Chat(context.Background(), ChatRequest{
		Model:       "z-ai/glm5",
		Messages:    []ChatMessage{{Role: "user", Content: "hi"}},
		Temperature: 0.1,
		MaxTokens:   256,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
}

func TestNVIDIAProviderStreamWithReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["stream"] != true {
			t.Fatalf("expected stream=true, got %v", payload["stream"])
		}
		kwargs, ok := payload["chat_template_kwargs"].(map[string]any)
		if !ok {
			t.Fatalf("missing chat_template_kwargs")
		}
		if kwargs["enable_thinking"] != true || kwargs["clear_thinking"] != false {
			t.Fatalf("unexpected kwargs: %+v", kwargs)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking-1 \"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"type\\\":\\\"final\\\",\\\"content\\\":\\\"ok\\\"}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	p := NewNVIDIAProvider(srv.URL, "test-key", 5*time.Second, 0, true, true, false)
	resp, err := p.Chat(context.Background(), ChatRequest{
		Model:       "z-ai/glm5",
		Messages:    []ChatMessage{{Role: "user", Content: "hi"}},
		Temperature: 0.1,
		MaxTokens:   256,
	})
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if resp.Content != `{"type":"final","content":"ok"}` {
		t.Fatalf("unexpected content: %q", resp.Content)
	}
	if resp.Reasoning != "thinking-1 " {
		t.Fatalf("unexpected reasoning: %q", resp.Reasoning)
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	d := parseRetryAfter("5")
	if d != 5*time.Second {
		t.Fatalf("expected 5s, got %s", d)
	}
}

func TestNVIDIAProviderStreamNotBoundByRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	p := NewNVIDIAProvider(srv.URL, "test-key", 50*time.Millisecond, 0, true, true, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := p.Chat(ctx, ChatRequest{
		Model:       "z-ai/glm5",
		Messages:    []ChatMessage{{Role: "user", Content: "hi"}},
		Temperature: 0.1,
		MaxTokens:   256,
	})
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("unexpected content: %q", resp.Content)
	}
}

func TestNVIDIAProviderStreamDeltaCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking \"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	p := NewNVIDIAProvider(srv.URL, "test-key", 5*time.Second, 0, true, true, false)
	var gotContent strings.Builder
	var gotReasoning strings.Builder
	ctx := WithStreamHandler(context.Background(), func(delta StreamDelta) {
		gotContent.WriteString(delta.Content)
		gotReasoning.WriteString(delta.Reasoning)
	})
	resp, err := p.Chat(ctx, ChatRequest{
		Model:       "z-ai/glm5",
		Messages:    []ChatMessage{{Role: "user", Content: "hi"}},
		Temperature: 0.1,
		MaxTokens:   256,
	})
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if gotContent.String() != "hello world" {
		t.Fatalf("unexpected callback content: %q", gotContent.String())
	}
	if gotReasoning.String() != "thinking " {
		t.Fatalf("unexpected callback reasoning: %q", gotReasoning.String())
	}
	if resp.Content != "hello world" {
		t.Fatalf("unexpected content: %q", resp.Content)
	}
}

func TestNVIDIAProviderFinishReasonJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"content":"x"}}]}`))
	}))
	defer srv.Close()

	p := NewNVIDIAProvider(srv.URL, "test-key", 5*time.Second, 0, false, true, false)
	resp, err := p.Chat(context.Background(), ChatRequest{
		Model:       "z-ai/glm5",
		Messages:    []ChatMessage{{Role: "user", Content: "hi"}},
		Temperature: 0.1,
		MaxTokens:   64,
	})
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if resp.FinishReason != "length" {
		t.Fatalf("unexpected finish_reason: %q", resp.FinishReason)
	}
}

func TestNVIDIAProviderFinishReasonStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	p := NewNVIDIAProvider(srv.URL, "test-key", 5*time.Second, 0, true, true, false)
	resp, err := p.Chat(context.Background(), ChatRequest{
		Model:       "z-ai/glm5",
		Messages:    []ChatMessage{{Role: "user", Content: "hi"}},
		Temperature: 0.1,
		MaxTokens:   64,
	})
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("unexpected finish_reason: %q", resp.FinishReason)
	}
}

func TestNVIDIAProviderChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking \"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	p := NewNVIDIAProvider(srv.URL, "test-key", 5*time.Second, 0, true, true, false)
	stream, err := p.ChatStream(context.Background(), ChatRequest{
		Model:       "z-ai/glm5",
		Messages:    []ChatMessage{{Role: "user", Content: "hi"}},
		Temperature: 0.1,
		MaxTokens:   64,
	})
	if err != nil {
		t.Fatalf("chat stream failed: %v", err)
	}
	defer stream.Close()

	var got []StreamEvent
	for {
		ev, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			t.Fatalf("stream recv failed: %v", recvErr)
		}
		got = append(got, *ev)
	}
	if len(got) < 3 {
		t.Fatalf("expected >=3 events, got %d", len(got))
	}
	if got[0].Type != "delta" || got[1].Type != "delta" {
		t.Fatalf("unexpected first events: %+v", got[:2])
	}
	if got[2].Type != "meta" || !strings.Contains(got[2].Text, "finish_reason=stop") {
		t.Fatalf("unexpected meta event: %+v", got[2])
	}
}
