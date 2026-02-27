package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func RegisterBuiltins(reg *Registry, workspaceRoot string, outputLimit int) error {
	base := newBaseTool(workspaceRoot, outputLimit)
	builtins := []Tool{
		&listTool{baseTool: base},
		&globTool{baseTool: base},
		&grepTool{baseTool: base},
		&readTool{baseTool: base},
		&editTool{baseTool: base},
		&patchTool{baseTool: base},
		&bashTool{baseTool: base},
		newWebfetchTool(base),
		newWebsearchTool(base),
	}
	for _, t := range builtins {
		if err := reg.Register(t); err != nil {
			return err
		}
	}
	return nil
}

type listTool struct{ baseTool }

func (t *listTool) Name() string { return "list" }
func (t *listTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`)
}
func (t *listTool) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path string `json:"path"`
	}
	_ = parseJSONArgs(args, &in)
	p, err := t.safePath(in.Path)
	if err != nil {
		return Result{}, err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return Result{}, err
	}
	var lines []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}
	sort.Strings(lines)
	return Result{Output: t.trimOutput(strings.Join(lines, "\n"))}, nil
}

type globTool struct{ baseTool }

func (t *globTool) Name() string { return "glob" }
func (t *globTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`)
}
func (t *globTool) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := parseJSONArgs(args, &in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return Result{}, fmt.Errorf("pattern is required")
	}
	basePath, err := t.safePath(in.Path)
	if err != nil {
		return Result{}, err
	}
	var matches []string
	err = filepath.WalkDir(basePath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(basePath, path)
		if relErr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		ok, _ := filepath.Match(in.Pattern, filepath.Base(path))
		if ok {
			if d.IsDir() {
				matches = append(matches, rel+"/")
			} else {
				matches = append(matches, rel)
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return Result{Output: "no matches"}, nil
	}
	return Result{Output: t.trimOutput(strings.Join(matches, "\n"))}, nil
}

type grepTool struct{ baseTool }

func (t *grepTool) Name() string { return "grep" }
func (t *grepTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"query":{"type":"string"},"path":{"type":"string"},"limit":{"type":"integer"}},"required":["query"]}`)
}
func (t *grepTool) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Query string `json:"query"`
		Path  string `json:"path"`
		Limit int    `json:"limit"`
	}
	if err := parseJSONArgs(args, &in); err != nil {
		return Result{}, err
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" {
		return Result{}, fmt.Errorf("query is required")
	}
	if in.Limit <= 0 {
		in.Limit = 50
	}
	if in.Limit > 500 {
		in.Limit = 500
	}
	root, err := t.safePath(in.Path)
	if err != nil {
		return Result{}, err
	}

	var out []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if len(out) >= in.Limit {
			return fs.SkipAll
		}
		if d.IsDir() {
			base := strings.ToLower(d.Name())
			if base == ".git" || base == "node_modules" || base == ".idea" || base == ".vscode" {
				return filepath.SkipDir
			}
			return nil
		}
		if !textLikeFile(path, d) {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if bytes.IndexByte(raw, 0) >= 0 {
			return nil
		}
		lines := strings.Split(string(raw), "\n")
		for idx, line := range lines {
			if strings.Contains(strings.ToLower(line), strings.ToLower(in.Query)) {
				rel, _ := filepath.Rel(root, path)
				out = append(out, fmt.Sprintf("%s:%d: %s", rel, idx+1, strings.TrimRight(line, "\r")))
				if len(out) >= in.Limit {
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return Result{}, err
	}
	if len(out) == 0 {
		return Result{Output: "no matches"}, nil
	}
	return Result{Output: t.trimOutput(strings.Join(out, "\n"))}, nil
}

type readTool struct{ baseTool }

func (t *readTool) Name() string { return "read" }
func (t *readTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"path":{"type":"string"},"start_line":{"type":"integer"},"end_line":{"type":"integer"}},"required":["path"]}`)
}
func (t *readTool) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
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
	raw, err := os.ReadFile(p)
	if err != nil {
		return Result{}, err
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return Result{}, fmt.Errorf("binary file is not supported")
	}
	lines := strings.Split(string(raw), "\n")
	if in.StartLine <= 0 {
		in.StartLine = 1
	}
	if in.EndLine <= 0 || in.EndLine > len(lines) {
		in.EndLine = len(lines)
	}
	if in.StartLine > in.EndLine {
		return Result{}, fmt.Errorf("invalid line range")
	}
	var b strings.Builder
	for i := in.StartLine; i <= in.EndLine; i++ {
		fmt.Fprintf(&b, "%6d | %s", i, strings.TrimRight(lines[i-1], "\r"))
		if i < in.EndLine {
			b.WriteString("\n")
		}
	}
	return Result{Output: t.trimOutput(b.String())}, nil
}
