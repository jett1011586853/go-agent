package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSummarizeCommandOutputKeepsErrorContext(t *testing.T) {
	lines := make([]string, 0, 250)
	for i := 1; i <= 250; i++ {
		lines = append(lines, "line "+strings.Repeat("x", 8))
	}
	lines[120] = "error: build failed"
	lines[121] = "details around error"
	raw := strings.Join(lines, "\n")

	got := summarizeCommandOutput("go test ./...", raw)
	if !strings.Contains(got, "[summarized output]") {
		t.Fatalf("expected summarized output header")
	}
	if !strings.Contains(got, "error: build failed") {
		t.Fatalf("expected error line to be kept")
	}
	if !strings.Contains(got, "details around error") {
		t.Fatalf("expected neighboring context line to be kept")
	}
}

func TestSummarizeCommandOutputKeepsGoCompilerDiagnostics(t *testing.T) {
	lines := make([]string, 0, 280)
	for i := 0; i < 280; i++ {
		lines = append(lines, "noise line "+strings.Repeat("x", 12))
	}
	lines[120] = "# trade-lab/cmd/server"
	lines[121] = "cmd/server/main.go:12:2: undefined: badSymbol"
	lines[122] = "cmd/server/main.go:15:7: cannot use x (type string) as int value in assignment"
	lines[123] = "FAIL\ttrade-lab/cmd/server [build failed]"
	raw := strings.Join(lines, "\n")

	got := summarizeCommandOutput("go build ./...", raw)
	if !strings.Contains(got, "[summarized output]") {
		t.Fatalf("expected summarized output header")
	}
	for _, needle := range []string{
		"# trade-lab/cmd/server",
		"cmd/server/main.go:12:2: undefined: badSymbol",
		"FAIL\ttrade-lab/cmd/server [build failed]",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("expected summarized output to contain %q\n%s", needle, got)
		}
	}
}

func TestSummarizeCommandOutputSkipsForNonTestCommand(t *testing.T) {
	raw := strings.Repeat("hello\n", 300)
	got := summarizeCommandOutput("echo hi", raw)
	if got != raw {
		t.Fatalf("expected output unchanged for non-test command")
	}
}

func TestBashToolBlocksWriteCommands(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, root, 4096); err != nil {
		t.Fatal(err)
	}

	_, err := reg.Run(context.Background(), "bash", json.RawMessage(`{"cmd":"Set-Content -Path x.txt -Value 'abc'","cwd":"."}`))
	if err == nil {
		t.Fatalf("expected bash write command to be blocked")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "blocked") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyToolUsesGoMod(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/demo\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main_test.go"), []byte("package main\nimport \"testing\"\nfunc TestOK(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, root, 4096); err != nil {
		t.Fatal(err)
	}
	res, err := reg.Run(context.Background(), "verify", json.RawMessage(`{"scope":"project","path":"."}`))
	if err != nil {
		t.Fatalf("verify failed: %v\noutput:\n%s", err, res.Output)
	}
	if !strings.Contains(strings.ToLower(res.Output), "go") {
		t.Fatalf("expected go verifier output, got: %s", res.Output)
	}
}

func TestApplyDiffToolAppliesUnifiedDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in PATH")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, root, 4096); err != nil {
		t.Fatal(err)
	}
	diff := `*** BEGIN PATCH
diff --git a/a.txt b/a.txt
--- a/a.txt
+++ b/a.txt
@@ -1 +1 @@
-hello
+world
*** END PATCH`
	args, _ := json.Marshal(map[string]any{"diff": diff})
	res, err := reg.Run(context.Background(), "apply_diff", args)
	if err != nil {
		t.Fatalf("apply_diff failed: %v\noutput:\n%s", err, res.Output)
	}
	raw, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "world\n" {
		t.Fatalf("unexpected file content: %q", string(raw))
	}
	if len(res.Writes) == 0 || res.Writes[0].Path != "a.txt" {
		t.Fatalf("expected write metadata for a.txt, got %+v", res.Writes)
	}
}

func TestBashToolOutputSeparatesStdoutStderr(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, root, 4096); err != nil {
		t.Fatal(err)
	}
	res, err := reg.Run(context.Background(), "bash", json.RawMessage(`{"cmd":"Write-Output 'ok'; [Console]::Error.WriteLine('bad')","cwd":"."}`))
	if err != nil {
		t.Fatalf("bash failed: %v\noutput:\n%s", err, res.Output)
	}
	if !strings.Contains(res.Output, "STDOUT:") || !strings.Contains(res.Output, "STDERR:") {
		t.Fatalf("expected separated stdout/stderr, got:\n%s", res.Output)
	}
}

func TestBashToolSupportsAndChainSyntax(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, root, 4096); err != nil {
		t.Fatal(err)
	}
	res, err := reg.Run(context.Background(), "bash", json.RawMessage(`{"cmd":"Write-Output 'first' && Write-Output 'second'","cwd":"."}`))
	if err != nil {
		t.Fatalf("bash failed: %v\noutput:\n%s", err, res.Output)
	}
	if !strings.Contains(res.Output, "first") || !strings.Contains(res.Output, "second") {
		t.Fatalf("expected chained outputs, got:\n%s", res.Output)
	}
}

func TestVerifyToolTreatsNoPackagesAsScaffoldPass(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/empty\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := RegisterBuiltins(reg, root, 4096); err != nil {
		t.Fatal(err)
	}
	res, err := reg.Run(context.Background(), "verify", json.RawMessage(`{"scope":"project","path":"."}`))
	if err != nil {
		t.Fatalf("verify should treat no-packages scaffold as pass: %v\noutput:\n%s", err, res.Output)
	}
	if !strings.Contains(strings.ToLower(res.Output), "no packages to test") {
		t.Fatalf("expected no-packages diagnostic in output, got:\n%s", res.Output)
	}
}
