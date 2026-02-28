package llm

import "context"

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model          string
	Messages       []ChatMessage
	Temperature    float64
	MaxTokens      int
	Stream         *bool
	EnableThinking *bool
	ClearThinking  *bool
}

type ChatResponse struct {
	Content      string
	Reasoning    string
	FinishReason string
}

type StreamEvent struct {
	Type string
	Text string
}

type Stream interface {
	Recv() (*StreamEvent, error)
	Close() error
}

type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	ChatStream(ctx context.Context, req ChatRequest) (Stream, error)
}

type StreamDelta struct {
	Content   string
	Reasoning string
}

type StreamHandler func(delta StreamDelta)

type streamHandlerContextKey struct{}

func WithStreamHandler(ctx context.Context, handler StreamHandler) context.Context {
	if handler == nil {
		return ctx
	}
	return context.WithValue(ctx, streamHandlerContextKey{}, handler)
}

func streamHandlerFromContext(ctx context.Context) StreamHandler {
	if ctx == nil {
		return nil
	}
	v := ctx.Value(streamHandlerContextKey{})
	h, _ := v.(StreamHandler)
	return h
}
