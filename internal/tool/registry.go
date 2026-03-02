package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

type Result struct {
	Output string        `json:"output"`
	Writes []WriteRecord `json:"writes,omitempty"`
}

type WriteRecord struct {
	Path string `json:"path"`
	Op   string `json:"op"` // create|modify|append|mkdir|delete
}

type Tool interface {
	Name() string
	Schema() []byte
	Run(ctx context.Context, args json.RawMessage) (Result, error)
}

type Hook interface {
	BeforeRun(ctx context.Context, toolName string, args json.RawMessage) error
	AfterRun(ctx context.Context, toolName string, args json.RawMessage, result Result, runErr error) error
}

type Registry struct {
	tools map[string]Tool
	hooks []Hook
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) error {
	if t == nil {
		return errors.New("tool is nil")
	}
	name := strings.ToLower(strings.TrimSpace(t.Name()))
	if name == "" {
		return errors.New("tool name is empty")
	}
	if _, ok := r.tools[name]; ok {
		return fmt.Errorf("tool %q already registered", name)
	}
	r.tools[name] = t
	return nil
}

func (r *Registry) Has(name string) bool {
	_, ok := r.tools[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

func (r *Registry) Get(name string) (Tool, error) {
	t, ok := r.tools[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, fmt.Errorf("tool %q is not registered", name)
	}
	return t, nil
}

func (r *Registry) List() []string {
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (r *Registry) RegisterHook(h Hook) error {
	if h == nil {
		return errors.New("hook is nil")
	}
	r.hooks = append(r.hooks, h)
	return nil
}

func (r *Registry) Schemas(allowed []string) map[string]json.RawMessage {
	set := map[string]struct{}{}
	for _, name := range allowed {
		set[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	out := map[string]json.RawMessage{}
	for name, t := range r.tools {
		if len(set) > 0 {
			if _, ok := set[name]; !ok {
				continue
			}
		}
		out[name] = json.RawMessage(t.Schema())
	}
	return out
}

func (r *Registry) Run(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	t, err := r.Get(name)
	if err != nil {
		return Result{}, err
	}
	for _, h := range r.hooks {
		if err := h.BeforeRun(ctx, name, args); err != nil {
			return Result{}, err
		}
	}
	res, runErr := t.Run(ctx, args)
	for _, h := range r.hooks {
		if err := h.AfterRun(ctx, name, args, res, runErr); err != nil {
			if runErr == nil {
				runErr = err
			}
		}
	}
	return res, runErr
}

type baseTool struct {
	root        string
	outputLimit int
}

func newBaseTool(root string, outputLimit int) baseTool {
	if outputLimit <= 0 {
		outputLimit = 50 * 1024
	}
	return baseTool{
		root:        root,
		outputLimit: outputLimit,
	}
}

func (b baseTool) trimOutput(s string) string {
	r := []rune(s)
	if len(r) <= b.outputLimit {
		return s
	}
	return string(r[:b.outputLimit]) + fmt.Sprintf("\n...[truncated %d chars]", len(r)-b.outputLimit)
}

func (b baseTool) safePath(userPath string) (string, error) {
	userPath = strings.TrimSpace(userPath)
	if userPath == "" {
		userPath = "."
	}
	rootAbs, err := filepath.Abs(b.root)
	if err != nil {
		return "", err
	}
	candidate := userPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootAbs, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes workspace root: %s", userPath)
	}
	return abs, nil
}

func (b baseTool) relPath(absPath string) string {
	rootAbs, err := filepath.Abs(b.root)
	if err != nil {
		return filepath.ToSlash(strings.TrimSpace(absPath))
	}
	absPath, err = filepath.Abs(absPath)
	if err != nil {
		return filepath.ToSlash(strings.TrimSpace(absPath))
	}
	rel, err := filepath.Rel(rootAbs, absPath)
	if err != nil {
		return filepath.ToSlash(strings.TrimSpace(absPath))
	}
	return filepath.ToSlash(rel)
}

func parseJSONArgs(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

func textLikeFile(path string, d fs.DirEntry) bool {
	if d.IsDir() {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp", ".exe", ".dll", ".so", ".zip", ".gz", ".jar", ".class", ".pdf":
		return false
	default:
		return true
	}
}
