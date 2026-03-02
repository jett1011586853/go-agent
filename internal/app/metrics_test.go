package app

import (
	"strings"
	"testing"
)

func TestRuntimeMetricsPhaseRecoveryCounters(t *testing.T) {
	m := newRuntimeMetrics()
	m.recordPhaseRecoveryTrigger("tool_call.verify")
	m.recordPhaseRecoveryOutcome("tool_call.verify", true)
	m.recordPhaseRecoveryTrigger("tool_call.read")
	m.recordPhaseRecoveryOutcome("tool_call.read", false)
	m.recordPhaseRecoveryTrigger("ask_clarification")
	m.recordPhaseRecoveryOutcome("ask_clarification", true)

	s := m.snapshot()
	if s.PhaseRecoveryTotal != 3 {
		t.Fatalf("expected phase recovery total=3, got %d", s.PhaseRecoveryTotal)
	}
	if s.PhaseRecoveryRate <= 0.0 {
		t.Fatalf("expected non-zero phase recovery rate")
	}
	if !strings.Contains(s.PhaseRecoveryBreakdown, "verify=") {
		t.Fatalf("expected verify breakdown, got: %s", s.PhaseRecoveryBreakdown)
	}
	if !strings.Contains(s.PhaseRecoveryBreakdown, "read=") {
		t.Fatalf("expected read breakdown, got: %s", s.PhaseRecoveryBreakdown)
	}
	if !strings.Contains(s.PhaseRecoveryBreakdown, "clarify=") {
		t.Fatalf("expected clarify breakdown, got: %s", s.PhaseRecoveryBreakdown)
	}
}
