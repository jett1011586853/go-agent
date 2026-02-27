package message

import (
	"encoding/json"
	"time"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type ToolResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
	Allowed string `json:"allowed,omitempty"` // allow|ask|deny
}

type AuditEvent struct {
	Type      string    `json:"type"`
	ToolName  string    `json:"tool_name,omitempty"`
	Decision  string    `json:"decision,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Turn struct {
	ID        string       `json:"id"`
	Agent     string       `json:"agent"`
	UserInput string       `json:"user_input"`
	Messages  []Message    `json:"messages"`
	Results   []ToolResult `json:"results,omitempty"`
	Audit     []AuditEvent `json:"audit,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
}
