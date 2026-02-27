package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type editTool struct{ baseTool }

func (t *editTool) Name() string { return "edit" }
func (t *editTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"},"append":{"type":"boolean"}},"required":["path","content"]}`)
}
func (t *editTool) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Append  bool   `json:"append"`
	}
	if err := parseJSONArgs(args, &in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Path) == "" {
		return Result{}, fmt.Errorf("path is required")
	}
	p, err := t.safePath(in.Path)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return Result{}, err
	}

	if in.Append {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return Result{}, err
		}
		defer f.Close()
		if _, err := f.WriteString(in.Content); err != nil {
			return Result{}, err
		}
	} else {
		if err := os.WriteFile(p, []byte(in.Content), 0o644); err != nil {
			return Result{}, err
		}
	}
	return Result{Output: fmt.Sprintf("edited: %s (%d bytes)", in.Path, len(in.Content))}, nil
}

type patchTool struct{ baseTool }

func (t *patchTool) Name() string { return "patch" }
func (t *patchTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"path":{"type":"string"},"search":{"type":"string"},"replace":{"type":"string"},"all":{"type":"boolean"}},"required":["path","search","replace"]}`)
}
func (t *patchTool) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path    string `json:"path"`
		Search  string `json:"search"`
		Replace string `json:"replace"`
		All     bool   `json:"all"`
	}
	if err := parseJSONArgs(args, &in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Path) == "" {
		return Result{}, fmt.Errorf("path is required")
	}
	if in.Search == "" {
		return Result{}, fmt.Errorf("search is required")
	}
	p, err := t.safePath(in.Path)
	if err != nil {
		return Result{}, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return Result{}, err
	}
	src := string(raw)
	if !strings.Contains(src, in.Search) {
		return Result{}, fmt.Errorf("search text not found")
	}

	replaced := 1
	dst := strings.Replace(src, in.Search, in.Replace, 1)
	if in.All {
		replaced = strings.Count(src, in.Search)
		dst = strings.ReplaceAll(src, in.Search, in.Replace)
	}
	if err := os.WriteFile(p, []byte(dst), 0o644); err != nil {
		return Result{}, err
	}
	return Result{Output: fmt.Sprintf("patched: %s (replacements=%d)", in.Path, replaced)}, nil
}

type bashTool struct{ baseTool }

func (t *bashTool) Name() string { return "bash" }
func (t *bashTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"cmd":{"type":"string"},"cwd":{"type":"string"},"timeout_sec":{"type":"integer"}},"required":["cmd"]}`)
}
func (t *bashTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Cmd        string `json:"cmd"`
		Cwd        string `json:"cwd"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := parseJSONArgs(args, &in); err != nil {
		return Result{}, err
	}
	in.Cmd = strings.TrimSpace(in.Cmd)
	if in.Cmd == "" {
		return Result{}, fmt.Errorf("cmd is required")
	}
	if in.TimeoutSec <= 0 {
		in.TimeoutSec = 60
	}
	if in.TimeoutSec > 600 {
		in.TimeoutSec = 600
	}
	cwd, err := t.safePath(in.Cwd)
	if err != nil {
		return Result{}, err
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(in.TimeoutSec)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "powershell", "-NoProfile", "-Command", in.Cmd)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	text := t.trimOutput(string(out))
	if err != nil {
		return Result{Output: text}, fmt.Errorf("bash failed: %w", err)
	}
	return Result{Output: text}, nil
}
