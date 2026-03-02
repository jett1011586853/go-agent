package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"go-agent/internal/skills"
)

type skillTool struct {
	baseTool
	svc *skills.Service
}

func RegisterSkillTool(reg *Registry, svc *skills.Service, outputLimit int) error {
	if reg == nil {
		return fmt.Errorf("tool registry is nil")
	}
	return reg.Register(&skillTool{
		baseTool: newBaseTool(".", outputLimit),
		svc:      svc,
	})
}

func (t *skillTool) Name() string { return "skill" }

func (t *skillTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"action":{"type":"string","enum":["list","load"]},"name":{"type":"string"},"limit":{"type":"integer"}},"required":["action"]}`)
}

func (t *skillTool) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Action string `json:"action"`
		Name   string `json:"name"`
		Limit  int    `json:"limit"`
	}
	if err := parseJSONArgs(args, &in); err != nil {
		return Result{}, err
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	if action == "" {
		return Result{}, fmt.Errorf("action is required")
	}
	if t.svc == nil {
		return Result{Output: "skills service is not enabled"}, nil
	}

	switch action {
	case "list":
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > 200 {
			limit = 200
		}
		items := t.svc.List(limit)
		if len(items) == 0 {
			return Result{Output: "no skills available"}, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "skills (%d):\n", len(items))
		for _, item := range items {
			desc := strings.TrimSpace(item.Description)
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&b, "- %s: %s\n", item.Name, desc)
		}
		return Result{Output: t.trimOutput(strings.TrimSpace(b.String()))}, nil
	case "load":
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return Result{}, fmt.Errorf("name is required when action=load")
		}
		item, err := t.svc.LoadByName(name)
		if err != nil {
			return Result{}, err
		}
		desc := strings.TrimSpace(item.Description)
		if desc == "" {
			desc = "(no description)"
		}
		body := strings.TrimSpace(item.Body)
		var b strings.Builder
		fmt.Fprintf(&b, "skill: %s\n", item.Name)
		fmt.Fprintf(&b, "source: %s\n", filepath.ToSlash(item.Path))
		fmt.Fprintf(&b, "description: %s\n", desc)
		if len(item.UseWhen) > 0 {
			fmt.Fprintf(&b, "use_when: %s\n", strings.Join(item.UseWhen, " | "))
		}
		if len(item.AvoidWhen) > 0 {
			fmt.Fprintf(&b, "avoid_when: %s\n", strings.Join(item.AvoidWhen, " | "))
		}
		if len(item.Required) > 0 {
			parts := make([]string, 0, len(item.Required))
			for _, arg := range item.Required {
				label := strings.TrimSpace(arg.Name)
				if label == "" {
					continue
				}
				if arg.Required {
					label += " (required)"
				}
				if d := strings.TrimSpace(arg.Description); d != "" {
					label += ": " + d
				}
				parts = append(parts, label)
				if len(parts) >= 5 {
					break
				}
			}
			if len(parts) > 0 {
				fmt.Fprintf(&b, "required_args: %s\n", strings.Join(parts, " | "))
			}
		}
		if len(item.Examples) > 0 {
			ex := item.Examples[0]
			exParts := make([]string, 0, 3)
			if s := strings.TrimSpace(ex.Input); s != "" {
				exParts = append(exParts, "input="+s)
			}
			if s := strings.TrimSpace(ex.Decision); s != "" {
				exParts = append(exParts, "decision="+s)
			}
			if s := strings.TrimSpace(ex.Output); s != "" {
				exParts = append(exParts, "output="+s)
			}
			if len(exParts) > 0 {
				fmt.Fprintf(&b, "example: %s\n", strings.Join(exParts, " ; "))
			}
		}
		fmt.Fprint(&b, "\n")
		b.WriteString(body)
		return Result{Output: t.trimOutput(strings.TrimSpace(b.String()))}, nil
	default:
		return Result{}, fmt.Errorf("unsupported action %q (use list|load)", action)
	}
}
