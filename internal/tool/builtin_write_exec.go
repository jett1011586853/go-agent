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
	_, statErr := os.Stat(p)
	existed := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return Result{}, statErr
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
	op := "modify"
	if !existed {
		op = "create"
	}
	if in.Append {
		op = "append"
	}
	return Result{
		Output: fmt.Sprintf("edited: %s (%d bytes)", in.Path, len(in.Content)),
		Writes: []WriteRecord{{Path: t.relPath(p), Op: op}},
	}, nil
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
	return Result{
		Output: fmt.Sprintf("patched: %s (replacements=%d)", in.Path, replaced),
		Writes: []WriteRecord{{Path: t.relPath(p), Op: "modify"}},
	}, nil
}

type applyDiffTool struct{ baseTool }

func (t *applyDiffTool) Name() string { return "apply_diff" }
func (t *applyDiffTool) Schema() []byte {
	return []byte(`{"type":"object","properties":{"diff":{"type":"string"}},"required":["diff"]}`)
}

func (t *applyDiffTool) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Diff string `json:"diff"`
	}
	if err := parseJSONArgs(args, &in); err != nil {
		return Result{}, err
	}
	diffText := extractDiffPayload(in.Diff)
	if strings.TrimSpace(diffText) == "" {
		return Result{}, fmt.Errorf("diff is required")
	}
	touched := parseTouchedPathsFromDiff(diffText)
	for _, rel := range touched {
		if strings.TrimSpace(rel) == "" {
			continue
		}
		if _, err := t.safePath(rel); err != nil {
			return Result{}, fmt.Errorf("diff path is outside workspace: %s", rel)
		}
	}

	out, err := runGitApply(ctx, t.root, []string{"apply", "--3way", "--unidiff-zero", "--whitespace=nowarn", "-"}, diffText)
	if err != nil {
		// Fallback when 3-way cannot be used (for example no index context).
		out2, err2 := runGitApply(ctx, t.root, []string{"apply", "--unidiff-zero", "--whitespace=nowarn", "-"}, diffText)
		if err2 != nil {
			merged := strings.TrimSpace(out + "\n" + out2)
			if merged == "" {
				merged = err2.Error()
			}
			return Result{Output: t.trimOutput(merged)}, fmt.Errorf("apply_diff failed: %w", err2)
		}
		out = out2
	}
	writes := make([]WriteRecord, 0, len(touched))
	seen := map[string]struct{}{}
	for _, path := range touched {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		writes = append(writes, WriteRecord{Path: path, Op: "modify"})
	}
	output := strings.TrimSpace(out)
	if output == "" {
		output = fmt.Sprintf("apply_diff: applied (%d files)", len(writes))
	} else {
		output = fmt.Sprintf("apply_diff: applied (%d files)\n%s", len(writes), output)
	}
	return Result{Output: t.trimOutput(output), Writes: writes}, nil
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
	in.Cmd = normalizeShellCommandForPowerShell(in.Cmd)
	if err := validateReadOnlyBashCommand(in.Cmd); err != nil {
		return Result{}, err
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
	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err = cmd.Run()
	streamText := formatCommandStreams(stdoutBuf.String(), stderrBuf.String())
	text := summarizeCommandOutput(in.Cmd, streamText)
	text = t.trimOutput(text)
	if err != nil {
		return Result{Output: text}, fmt.Errorf("bash failed: %w", err)
	}
	return Result{Output: text}, nil
}

func normalizeShellCommandForPowerShell(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return cmd
	}
	// LLMs often emit POSIX-style command chaining. Windows PowerShell 5.x
	// does not support "&&", so convert it to ";" to avoid parser failures.
	if strings.Contains(cmd, "&&") {
		cmd = strings.ReplaceAll(cmd, "&&", ";")
	}
	return cmd
}

func summarizeCommandOutput(cmd, output string) string {
	cmdLower := strings.ToLower(cmd)
	isGoCmd := strings.Contains(cmdLower, "go test") ||
		strings.Contains(cmdLower, "go build") ||
		strings.Contains(cmdLower, "go vet") ||
		strings.Contains(cmdLower, "go run")
	interestingCmd := isGoCmd ||
		strings.Contains(cmdLower, "pytest") ||
		strings.Contains(cmdLower, "npm test") ||
		strings.Contains(cmdLower, "pnpm test") ||
		strings.Contains(cmdLower, "yarn test")
	if !interestingCmd {
		return output
	}
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	if len(lines) <= 180 {
		return output
	}

	pattern := regexp.MustCompile(`(?i)(^FAIL\b|\bfail(ed|ure)?\b|panic:|fatal:|error:|undefined:|exception|traceback|:\d+:\d+:|not terminated|illegal character|cannot use|expected\b)`)
	goPattern := regexp.MustCompile(`(?i)(\.go:\d+(:\d+)?:|^\s*#\s|^\s*go:\s|^\s*(FAIL|ok)\b|build failed|test failed|undefined:|cannot use|missing return|not enough arguments)`)
	keep := map[int]struct{}{}
	keepWindow := func(center int, radius int) {
		start := center - radius
		if start < 0 {
			start = 0
		}
		end := center + radius
		if end >= len(lines) {
			end = len(lines) - 1
		}
		for i := start; i <= end; i++ {
			keep[i] = struct{}{}
		}
	}
	for i, line := range lines {
		if pattern.MatchString(line) {
			keepWindow(i, 3)
			continue
		}
		if isGoCmd && goPattern.MatchString(line) {
			keepWindow(i, 3)
		}
	}
	for i := 0; i < len(lines) && i < 15; i++ {
		keep[i] = struct{}{}
	}
	for i := len(lines) - 20; i < len(lines); i++ {
		if i >= 0 {
			keep[i] = struct{}{}
		}
	}
	if isGoCmd && len(keep) < 40 {
		for i, line := range lines {
			ll := strings.ToLower(strings.TrimSpace(line))
			if strings.HasPrefix(ll, "# ") || strings.HasPrefix(ll, "go: ") || strings.HasPrefix(ll, "fail\t") || strings.Contains(ll, "[build failed]") {
				keepWindow(i, 2)
			}
		}
	}
	if len(keep) == len(lines) {
		return output
	}

	ids := make([]int, 0, len(keep))
	for i := range keep {
		ids = append(ids, i)
	}
	slices.Sort(ids)
	var b strings.Builder
	fmt.Fprintf(&b, "[summarized output] kept %d/%d lines\n", len(ids), len(lines))
	last := -1
	for _, i := range ids {
		if last >= 0 && i-last > 1 {
			b.WriteString("...\n")
		}
		b.WriteString(lines[i])
		b.WriteString("\n")
		last = i
	}
	return strings.TrimSpace(b.String())
}

func validateReadOnlyBashCommand(cmd string) error {
	lower := strings.ToLower(strings.TrimSpace(cmd))
	blockedTokens := []string{
		"set-content",
		"add-content",
		"out-file",
		"tee-object",
		"remove-item",
		"move-item",
		"copy-item",
	}
	for _, token := range blockedTokens {
		if strings.Contains(lower, token) {
			return fmt.Errorf("bash write/delete commands are blocked (%s). use structured tools: edit/patch/write_file/mkdir", token)
		}
	}
	if strings.Contains(lower, "new-item") && strings.Contains(lower, "-itemtype") {
		if strings.Contains(lower, "directory") || strings.Contains(lower, "dir") || strings.Contains(lower, "file") {
			return fmt.Errorf("bash file/dir creation is blocked. use structured tools: mkdir/write_file")
		}
	}
	redir := regexp.MustCompile(`(^|[\s\)])(>>|>)([\s\(\[]|$)`)
	if redir.MatchString(cmd) {
		return fmt.Errorf("bash redirection write is blocked. use structured tools: edit/patch/write_file")
	}
	return nil
}

func formatCommandStreams(stdout, stderr string) string {
	stdout = strings.TrimSpace(stdout)
	stderr = strings.TrimSpace(stderr)
	if stdout == "" && stderr == "" {
		return ""
	}
	if stderr == "" {
		return "STDOUT:\n" + stdout
	}
	if stdout == "" {
		return "STDERR:\n" + stderr
	}
	return "STDOUT:\n" + stdout + "\n\nSTDERR:\n" + stderr
}

func runGitApply(ctx context.Context, dir string, args []string, diffText string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(diffText)
	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	return formatCommandStreams(stdoutBuf.String(), stderrBuf.String()), err
}

func extractDiffPayload(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	startMarker := "*** BEGIN PATCH"
	endMarker := "*** END PATCH"
	start := strings.Index(raw, startMarker)
	end := strings.LastIndex(raw, endMarker)
	if start >= 0 && end > start {
		body := raw[start+len(startMarker) : end]
		body = strings.TrimLeft(body, " \t\r\n")
		if strings.TrimSpace(body) == "" {
			return ""
		}
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		return body
	}
	body := strings.TrimSpace(raw)
	if body == "" {
		return ""
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body
}

func parseTouchedPathsFromDiff(diffText string) []string {
	lines := strings.Split(strings.ReplaceAll(diffText, "\r\n", "\n"), "\n")
	out := make([]string, 0, 8)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			path = strings.TrimPrefix(path, "b/")
			if path == "/dev/null" {
				continue
			}
			out = append(out, filepath.ToSlash(path))
			continue
		}
		if strings.HasPrefix(line, "diff --git ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				path := strings.TrimPrefix(parts[3], "b/")
				if path != "" && path != "/dev/null" {
					out = append(out, filepath.ToSlash(path))
				}
			}
		}
	}
	return out
}
