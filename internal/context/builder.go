package context

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"go-agent/internal/llm"
	"go-agent/internal/message"
	"go-agent/internal/session"
)

type Builder struct {
	rulesPath     string
	workspaceRoot string
	turnWindow    int
}

func NewBuilder(rulesPath, workspaceRoot string, turnWindow int) *Builder {
	if turnWindow <= 0 {
		turnWindow = 10
	}
	return &Builder{
		rulesPath:     rulesPath,
		workspaceRoot: strings.TrimSpace(workspaceRoot),
		turnWindow:    turnWindow,
	}
}

func (b *Builder) Build(agentName string, s session.Session, current []message.Message, toolSchemas map[string]json.RawMessage) []llm.ChatMessage {
	return b.BuildWithExtras(agentName, s, current, toolSchemas, nil)
}

func (b *Builder) BuildWithExtras(
	agentName string,
	s session.Session,
	current []message.Message,
	toolSchemas map[string]json.RawMessage,
	extraSystem []string,
) []llm.ChatMessage {
	messages := make([]llm.ChatMessage, 0, 64)
	messages = append(messages, llm.ChatMessage{
		Role:    "system",
		Content: b.systemPrompt(agentName, toolSchemas),
	})
	if rules := LoadRules(b.rulesPath); rules != "" {
		messages = append(messages, llm.ChatMessage{
			Role:    "system",
			Content: "Workspace rules from AGENTS.md:\n" + rules,
		})
	}
	if strings.TrimSpace(s.WorkingSummary) != "" {
		messages = append(messages, llm.ChatMessage{
			Role:    "system",
			Content: "Working summary:\n" + s.WorkingSummary,
		})
	}
	if len(s.PinnedFacts) > 0 {
		messages = append(messages, llm.ChatMessage{
			Role:    "system",
			Content: "Pinned facts:\n- " + strings.Join(s.PinnedFacts, "\n- "),
		})
	}
	if ws := b.workspaceSignals(); ws != "" {
		messages = append(messages, llm.ChatMessage{
			Role:    "system",
			Content: "Workspace signals:\n" + ws,
		})
	}
	for _, extra := range extraSystem {
		extra = strings.TrimSpace(extra)
		if extra == "" {
			continue
		}
		messages = append(messages, llm.ChatMessage{
			Role:    "system",
			Content: extra,
		})
	}

	start := 0
	if len(s.Turns) > b.turnWindow {
		start = len(s.Turns) - b.turnWindow
	}
	for _, t := range s.Turns[start:] {
		for _, m := range t.Messages {
			messages = append(messages, llm.ChatMessage{
				Role:    toLLMRole(m.Role),
				Content: m.Content,
			})
		}
	}
	for _, m := range current {
		messages = append(messages, llm.ChatMessage{
			Role:    toLLMRole(m.Role),
			Content: m.Content,
		})
	}
	return messages
}

func (b *Builder) systemPrompt(agentName string, toolSchemas map[string]json.RawMessage) string {
	toolNames := make([]string, 0, len(toolSchemas))
	for name := range toolSchemas {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)
	toolsText := make([]string, 0, len(toolNames))
	for _, name := range toolNames {
		toolsText = append(toolsText, fmt.Sprintf("- %s schema: %s", name, string(toolSchemas[name])))
	}
	if len(toolsText) == 0 {
		toolsText = append(toolsText, "- no tools")
	}
	actionTypes := []string{"final", "tool_call", "ask_clarification", "no_tool"}
	return fmt.Sprintf(`You are OpenCode-style coding agent "%s".
You must reply with exactly one JSON object and nothing else.
Allowed response types: %s

For tool call:
{
  "type": "tool_call",
  "tool": {
    "name": "<tool_name>",
    "args": { ... }
  },
  "thinking": "short reason"
}

For final answer:
{
  "type": "final",
  "content": "final user-facing answer",
  "next_steps": ["optional"],
  "thinking": "short reason"
}

For clarification question:
{
  "type": "ask_clarification",
  "content": "single concise clarification question",
  "thinking": "short reason"
}

For direct no-tool response:
{
  "type": "no_tool",
  "content": "direct answer without tools",
  "thinking": "short reason"
}

Do not invent tool names. Use only:
%s`, agentName, strings.Join(actionTypes, ","), strings.Join(toolsText, "\n"))
}

func toLLMRole(role message.Role) string {
	switch role {
	case message.RoleAssistant:
		return "assistant"
	case message.RoleSystem:
		return "system"
	case message.RoleTool:
		return "tool"
	default:
		return "user"
	}
}

func BuildToolHint(allowed []string) string {
	if len(allowed) == 0 {
		return "No tools enabled."
	}
	sort.Strings(allowed)
	return "Enabled tools: " + strings.Join(allowed, ", ")
}

func FilterToolSchemas(all map[string]json.RawMessage, allowed []string) map[string]json.RawMessage {
	set := map[string]struct{}{}
	for _, a := range allowed {
		set[strings.ToLower(strings.TrimSpace(a))] = struct{}{}
	}
	out := map[string]json.RawMessage{}
	for name, schema := range all {
		if len(set) == 0 || slices.Contains(allowed, name) {
			out[name] = schema
			continue
		}
		if _, ok := set[strings.ToLower(name)]; ok {
			out[name] = schema
		}
	}
	return out
}

func (b *Builder) workspaceSignals() string {
	root := strings.TrimSpace(b.workspaceRoot)
	if root == "" {
		return ""
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	entries, err := os.ReadDir(absRoot)
	if err != nil {
		return ""
	}

	items := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		items = append(items, name)
	}
	sort.Strings(items)
	if len(items) > 20 {
		items = items[:20]
	}

	lines := []string{
		"root: " + absRoot,
	}
	if branch := gitBranchFromHead(absRoot); branch != "" {
		lines = append(lines, "git_branch: "+branch)
	}
	if len(items) > 0 {
		lines = append(lines, "top_entries: "+strings.Join(items, ", "))
	}
	return strings.Join(lines, "\n")
}

func gitBranchFromHead(root string) string {
	headPath := filepath.Join(root, ".git", "HEAD")
	raw, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(raw))
	if strings.HasPrefix(head, "ref: ") {
		ref := strings.TrimPrefix(head, "ref: ")
		ref = strings.TrimSpace(ref)
		const p = "refs/heads/"
		if strings.HasPrefix(ref, p) {
			return strings.TrimPrefix(ref, p)
		}
		return filepath.Base(ref)
	}
	if len(head) > 12 {
		return head[:12]
	}
	return head
}
