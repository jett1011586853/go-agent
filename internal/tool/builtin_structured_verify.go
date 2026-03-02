package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

type mkdirTool struct{ baseTool }

func (t *mkdirTool) Name() string { return "mkdir" }
func (t *mkdirTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
}
func (t *mkdirTool) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path string `json:"path"`
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
	if err := os.MkdirAll(p, 0o755); err != nil {
		return Result{}, err
	}
	rel := t.relPath(p)
	return Result{
		Output: fmt.Sprintf("mkdir: %s", rel),
		Writes: []WriteRecord{{Path: rel, Op: "mkdir"}},
	}, nil
}

type writeFileTool struct{ baseTool }

func (t *writeFileTool) Name() string { return "write_file" }
func (t *writeFileTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"},"mode":{"type":"string","enum":["create","overwrite","append"]}},"required":["path","content"]}`)
}
func (t *writeFileTool) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Mode    string `json:"mode"`
	}
	if err := parseJSONArgs(args, &in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Path) == "" {
		return Result{}, fmt.Errorf("path is required")
	}
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode == "" {
		mode = "overwrite"
	}
	if !slices.Contains([]string{"create", "overwrite", "append"}, mode) {
		return Result{}, fmt.Errorf("invalid mode: %s", mode)
	}
	p, err := t.safePath(in.Path)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return Result{}, err
	}
	_, statErr := os.Stat(p)
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return Result{}, statErr
	}

	switch mode {
	case "create":
		if exists {
			return Result{}, fmt.Errorf("file already exists: %s", in.Path)
		}
		if err := os.WriteFile(p, []byte(in.Content), 0o644); err != nil {
			return Result{}, err
		}
	case "append":
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return Result{}, err
		}
		defer f.Close()
		if _, err := f.WriteString(in.Content); err != nil {
			return Result{}, err
		}
	default: // overwrite
		if err := os.WriteFile(p, []byte(in.Content), 0o644); err != nil {
			return Result{}, err
		}
	}

	op := "modify"
	if !exists {
		op = "create"
	}
	if mode == "append" {
		op = "append"
	}
	rel := t.relPath(p)
	return Result{
		Output: fmt.Sprintf("write_file: %s (mode=%s bytes=%d)", rel, mode, len(in.Content)),
		Writes: []WriteRecord{{Path: rel, Op: op}},
	}, nil
}

type verifyTool struct{ baseTool }

func (t *verifyTool) Name() string { return "verify" }
func (t *verifyTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"scope":{"type":"string","enum":["project","changed_files","full"]},"path":{"type":"string"},"changed_files":{"type":"array","items":{"type":"string"}},"timeout_sec":{"type":"integer"}}}`)
}

func (t *verifyTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Scope        string   `json:"scope"`
		Path         string   `json:"path"`
		ChangedFiles []string `json:"changed_files"`
		TimeoutSec   int      `json:"timeout_sec"`
	}
	if err := parseJSONArgs(args, &in); err != nil {
		return Result{}, err
	}
	scope := strings.ToLower(strings.TrimSpace(in.Scope))
	if scope == "" {
		scope = "project"
	}
	if !slices.Contains([]string{"project", "changed_files", "full"}, scope) {
		return Result{}, fmt.Errorf("invalid scope: %s", scope)
	}
	basePath, err := t.safePath(in.Path)
	if err != nil {
		return Result{}, err
	}
	if in.TimeoutSec <= 0 {
		in.TimeoutSec = 300
	}
	if in.TimeoutSec > 1200 {
		in.TimeoutSec = 1200
	}

	verifyRoots := []string{basePath}
	if scope == "changed_files" {
		verifyRoots = t.resolveChangedRoots(basePath, in.ChangedFiles)
		if len(verifyRoots) == 0 {
			verifyRoots = []string{basePath}
		}
	}

	var summary []string
	for _, root := range verifyRoots {
		plan, detectErr := t.detectVerifierPlan(root)
		rel := t.relPath(root)
		if detectErr != nil {
			summary = append(summary, fmt.Sprintf("[%s] detect error: %v", rel, detectErr))
			continue
		}
		if len(plan.commands) == 0 {
			ok, fallbackOut := t.minimalSafetyCheck(root, in.ChangedFiles)
			if ok {
				summary = append(summary, fmt.Sprintf("[%s] fallback minimal safety check: ok\n%s", rel, fallbackOut))
				continue
			}
			return Result{Output: t.trimOutput(strings.Join(summary, "\n"))}, fmt.Errorf("verify failed in %s: %s", rel, fallbackOut)
		}

		summary = append(summary, fmt.Sprintf("[%s] verifier: %s", rel, plan.name))
		for _, command := range plan.commands {
			runCtx, cancel := context.WithTimeout(ctx, time.Duration(in.TimeoutSec)*time.Second)
			out, runErr := runShellCommand(runCtx, root, command)
			cancel()
			out = summarizeCommandOutput(command, out)
			if strings.TrimSpace(out) != "" {
				summary = append(summary, fmt.Sprintf("$ %s\n%s", command, out))
			} else {
				summary = append(summary, fmt.Sprintf("$ %s\n(ok)", command))
			}
			if runErr != nil {
				if shouldIgnoreVerifierError(command, out) {
					summary = append(summary, "(info) verifier returned no testable package yet; treated as pass for scaffold stage")
					continue
				}
				result := t.trimOutput(strings.Join(summary, "\n"))
				return Result{Output: result}, fmt.Errorf("verify failed in %s: command %q: %w", rel, command, runErr)
			}
		}
	}
	if len(summary) == 0 {
		summary = append(summary, "verify: no matching verifier actions")
	}
	return Result{Output: t.trimOutput(strings.Join(summary, "\n"))}, nil
}

type verifierPlan struct {
	name     string
	commands []string
}

func (t *verifyTool) detectVerifierPlan(root string) (verifierPlan, error) {
	if exists(filepath.Join(root, "Makefile")) {
		target := detectMakeTarget(filepath.Join(root, "Makefile"), []string{"test", "check", "build"})
		if target != "" {
			return verifierPlan{name: "make", commands: []string{"make " + target}}, nil
		}
	}

	if exists(filepath.Join(root, "taskfile.yml")) {
		return verifierPlan{name: "task", commands: []string{"task test"}}, nil
	}
	if exists(filepath.Join(root, "Taskfile.yml")) {
		return verifierPlan{name: "task", commands: []string{"task test"}}, nil
	}
	if exists(filepath.Join(root, "justfile")) {
		return verifierPlan{name: "just", commands: []string{"just test"}}, nil
	}

	if exists(filepath.Join(root, "package.json")) {
		scripts := readPackageScripts(filepath.Join(root, "package.json"))
		pm := detectPackageManager(root)
		if pm == "" {
			pm = "npm"
		}
		cmds := make([]string, 0, 2)
		if _, hasTS := scripts["lint"]; hasTS && exists(filepath.Join(root, "tsconfig.json")) {
			cmds = append(cmds, packageManagerCommand(pm, "lint"))
		}
		if _, ok := scripts["test"]; ok {
			cmds = append(cmds, packageManagerCommand(pm, "test"))
		} else if _, ok := scripts["build"]; ok {
			cmds = append(cmds, packageManagerCommand(pm, "build"))
		}
		if len(cmds) > 0 {
			return verifierPlan{name: "package-json", commands: cmds}, nil
		}
	}

	if exists(filepath.Join(root, "go.mod")) {
		return verifierPlan{name: "go", commands: []string{"go test ./..."}}, nil
	}
	if exists(filepath.Join(root, "Cargo.toml")) {
		return verifierPlan{name: "cargo", commands: []string{"cargo test"}}, nil
	}
	if exists(filepath.Join(root, "pom.xml")) {
		return verifierPlan{name: "maven", commands: []string{"mvn -q test"}}, nil
	}
	if exists(filepath.Join(root, "build.gradle")) || exists(filepath.Join(root, "build.gradle.kts")) {
		return verifierPlan{name: "gradle", commands: []string{"gradle test"}}, nil
	}
	if exists(filepath.Join(root, "tox.ini")) {
		return verifierPlan{name: "tox", commands: []string{"tox -q"}}, nil
	}
	if exists(filepath.Join(root, "noxfile.py")) {
		return verifierPlan{name: "nox", commands: []string{"nox"}}, nil
	}
	if exists(filepath.Join(root, "pytest.ini")) || pyprojectHasPytest(filepath.Join(root, "pyproject.toml")) {
		return verifierPlan{name: "pytest", commands: []string{"pytest -q"}}, nil
	}
	if exists(filepath.Join(root, "requirements.txt")) {
		return verifierPlan{name: "python-compileall", commands: []string{"python -m compileall ."}}, nil
	}
	return verifierPlan{name: "fallback"}, nil
}

func (t *verifyTool) resolveChangedRoots(basePath string, changed []string) []string {
	roots := make([]string, 0, len(changed))
	seen := map[string]struct{}{}
	for _, p := range changed {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs, err := t.safePath(p)
		if err != nil {
			continue
		}
		fi, err := os.Stat(abs)
		if err == nil && !fi.IsDir() {
			abs = filepath.Dir(abs)
		}
		root := nearestProjectRoot(abs, basePath)
		if root == "" {
			root = basePath
		}
		key := filepath.Clean(strings.ToLower(root))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		roots = append(roots, root)
	}
	return roots
}

func nearestProjectRoot(startDir, stopDir string) string {
	markers := []string{
		"Makefile", "justfile", "taskfile.yml", "Taskfile.yml",
		"go.mod", "package.json", "Cargo.toml", "pom.xml", "build.gradle", "build.gradle.kts",
		"tox.ini", "noxfile.py", "pytest.ini", "pyproject.toml", "requirements.txt",
	}
	stopAbs, _ := filepath.Abs(stopDir)
	cur, _ := filepath.Abs(startDir)
	for {
		for _, marker := range markers {
			if exists(filepath.Join(cur, marker)) {
				return cur
			}
		}
		if strings.EqualFold(cur, stopAbs) {
			break
		}
		next := filepath.Dir(cur)
		if next == cur {
			break
		}
		cur = next
	}
	return ""
}

func (t *verifyTool) minimalSafetyCheck(root string, changed []string) (bool, string) {
	files := make([]string, 0, len(changed))
	for _, p := range changed {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs, err := t.safePath(p)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(abs), strings.ToLower(root)) {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			continue
		}
		files = append(files, abs)
	}
	if len(files) == 0 {
		entries, err := os.ReadDir(root)
		if err != nil {
			return false, fmt.Sprintf("cannot read directory: %v", err)
		}
		return len(entries) > 0, "no verifier matched; directory exists and is readable"
	}

	for _, file := range files {
		ext := strings.ToLower(filepath.Ext(file))
		raw, err := os.ReadFile(file)
		if err != nil {
			return false, fmt.Sprintf("cannot read changed file %s: %v", filepath.Base(file), err)
		}
		switch ext {
		case ".json":
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				return false, fmt.Sprintf("json parse failed for %s: %v", filepath.Base(file), err)
			}
		case ".yaml", ".yml":
			var v any
			if err := yaml.Unmarshal(raw, &v); err != nil {
				return false, fmt.Sprintf("yaml parse failed for %s: %v", filepath.Base(file), err)
			}
		}
	}
	return true, "minimal safety check passed (json/yaml parse + file readability)"
}

func runShellCommand(ctx context.Context, dir, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", command)
	cmd.Dir = dir
	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	return formatCommandStreams(stdoutBuf.String(), stderrBuf.String()), err
}

func shouldIgnoreVerifierError(command, output string) bool {
	cmd := strings.ToLower(strings.TrimSpace(command))
	if strings.HasPrefix(cmd, "go test") && strings.Contains(strings.ToLower(output), "no packages to test") {
		return true
	}
	return false
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func detectMakeTarget(path string, targets []string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := string(raw)
	for _, target := range targets {
		re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `\s*:`)
		if re.MatchString(text) {
			return target
		}
	}
	return ""
}

func readPackageScripts(path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var parsed struct {
		Scripts map[string]any `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return map[string]any{}
	}
	if parsed.Scripts == nil {
		return map[string]any{}
	}
	return parsed.Scripts
}

func detectPackageManager(root string) string {
	if exists(filepath.Join(root, "pnpm-lock.yaml")) {
		if _, err := exec.LookPath("pnpm"); err == nil {
			return "pnpm"
		}
	}
	if exists(filepath.Join(root, "yarn.lock")) {
		if _, err := exec.LookPath("yarn"); err == nil {
			return "yarn"
		}
	}
	if _, err := exec.LookPath("npm"); err == nil {
		return "npm"
	}
	return ""
}

func packageManagerCommand(pm, script string) string {
	pm = strings.ToLower(strings.TrimSpace(pm))
	switch pm {
	case "pnpm":
		if script == "test" {
			return "pnpm test"
		}
		return "pnpm " + script
	case "yarn":
		return "yarn " + script
	default:
		if script == "test" {
			return "npm test"
		}
		return "npm run " + script
	}
}

func pyprojectHasPytest(path string) bool {
	if !exists(path) {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(raw))
	return strings.Contains(lower, "[tool.pytest") || strings.Contains(lower, "pytest")
}
