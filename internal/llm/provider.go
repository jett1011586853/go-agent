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
	Content   string
	Reasoning string
}

type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
