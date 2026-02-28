package tool

import (
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

func TestSummarizeCommandOutputSkipsForNonTestCommand(t *testing.T) {
	raw := strings.Repeat("hello\n", 300)
	got := summarizeCommandOutput("echo hi", raw)
	if got != raw {
		t.Fatalf("expected output unchanged for non-test command")
	}
}
