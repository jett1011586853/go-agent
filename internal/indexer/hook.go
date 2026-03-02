package indexer

import (
	"context"
	"encoding/json"
	"strings"

	"go-agent/internal/tool"
)

type ToolHook struct {
	service *Service
}

func NewToolHook(service *Service) *ToolHook {
	return &ToolHook{service: service}
}

func (h *ToolHook) BeforeRun(context.Context, string, json.RawMessage) error {
	return nil
}

func (h *ToolHook) AfterRun(_ context.Context, toolName string, args json.RawMessage, result tool.Result, runErr error) error {
	if h == nil || h.service == nil || runErr != nil {
		return nil
	}
	for _, w := range result.Writes {
		path := strings.TrimSpace(w.Path)
		if path == "" {
			continue
		}
		h.service.Enqueue(path)
	}
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	switch toolName {
	case "edit", "patch", "write_file", "mkdir", "apply_diff":
		var in struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil
		}
		if strings.TrimSpace(in.Path) != "" {
			h.service.Enqueue(in.Path)
		}
	}
	return nil
}
