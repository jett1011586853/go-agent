package app

import (
	"fmt"
	"strings"
	"sync/atomic"
)

type runtimeMetrics struct {
	turnsTotal          atomic.Int64
	turnsWithSkills     atomic.Int64
	totalPlannerSteps   atomic.Int64
	toolCalls           atomic.Int64
	toolErrors          atomic.Int64
	recoveryAttempts    atomic.Int64
	recoverySuccesses   atomic.Int64
	invalidModelOutputs atomic.Int64
	phaseRecoveryTotal  atomic.Int64
	phaseRecoveryOK     atomic.Int64
	phaseRecListTotal   atomic.Int64
	phaseRecListOK      atomic.Int64
	phaseRecReadTotal   atomic.Int64
	phaseRecReadOK      atomic.Int64
	phaseRecVerifyTotal atomic.Int64
	phaseRecVerifyOK    atomic.Int64
	phaseRecClarTotal   atomic.Int64
	phaseRecClarOK      atomic.Int64
	phaseRecOtherTotal  atomic.Int64
	phaseRecOtherOK     atomic.Int64
}

type metricsSnapshot struct {
	TurnsTotal             int64
	SkillHitRate           float64
	WrongCallRate          float64
	RecoverySuccess        float64
	AvgPlannerPerTurn      float64
	PhaseRecoveryTotal     int64
	PhaseRecoveryRate      float64
	PhaseRecoveryBreakdown string
}

func newRuntimeMetrics() *runtimeMetrics {
	return &runtimeMetrics{}
}

func (m *runtimeMetrics) snapshot() metricsSnapshot {
	if m == nil {
		return metricsSnapshot{}
	}
	turns := m.turnsTotal.Load()
	withSkills := m.turnsWithSkills.Load()
	steps := m.totalPlannerSteps.Load()
	toolCalls := m.toolCalls.Load()
	toolErrors := m.toolErrors.Load()
	recoveryAttempts := m.recoveryAttempts.Load()
	recoverySuccess := m.recoverySuccesses.Load()
	phaseTotal := m.phaseRecoveryTotal.Load()
	phaseOK := m.phaseRecoveryOK.Load()
	listTotal := m.phaseRecListTotal.Load()
	listOK := m.phaseRecListOK.Load()
	readTotal := m.phaseRecReadTotal.Load()
	readOK := m.phaseRecReadOK.Load()
	verifyTotal := m.phaseRecVerifyTotal.Load()
	verifyOK := m.phaseRecVerifyOK.Load()
	clarTotal := m.phaseRecClarTotal.Load()
	clarOK := m.phaseRecClarOK.Load()
	otherTotal := m.phaseRecOtherTotal.Load()
	otherOK := m.phaseRecOtherOK.Load()
	breakdown := fmt.Sprintf(
		"list=%d/%.1f%% read=%d/%.1f%% verify=%d/%.1f%% clarify=%d/%.1f%% other=%d/%.1f%%",
		listTotal,
		safeRate(listOK, listTotal)*100,
		readTotal,
		safeRate(readOK, readTotal)*100,
		verifyTotal,
		safeRate(verifyOK, verifyTotal)*100,
		clarTotal,
		safeRate(clarOK, clarTotal)*100,
		otherTotal,
		safeRate(otherOK, otherTotal)*100,
	)

	return metricsSnapshot{
		TurnsTotal:             turns,
		SkillHitRate:           safeRate(withSkills, turns),
		WrongCallRate:          safeRate(toolErrors, toolCalls),
		RecoverySuccess:        safeRate(recoverySuccess, recoveryAttempts),
		AvgPlannerPerTurn:      safeRate(steps, turns),
		PhaseRecoveryTotal:     phaseTotal,
		PhaseRecoveryRate:      safeRate(phaseOK, phaseTotal),
		PhaseRecoveryBreakdown: breakdown,
	}
}

func (m metricsSnapshot) String() string {
	return fmt.Sprintf(
		"metrics: turns=%d skill_hit=%.1f%% wrong_call=%.1f%% recovery=%.1f%% avg_planner_steps=%.2f phase_recovery=%d/%.1f%% (%s)",
		m.TurnsTotal,
		m.SkillHitRate*100,
		m.WrongCallRate*100,
		m.RecoverySuccess*100,
		m.AvgPlannerPerTurn,
		m.PhaseRecoveryTotal,
		m.PhaseRecoveryRate*100,
		m.PhaseRecoveryBreakdown,
	)
}

func (m *runtimeMetrics) recordPhaseRecoveryTrigger(kind string) {
	if m == nil {
		return
	}
	m.phaseRecoveryTotal.Add(1)
	switch normalizePhaseRecoveryKind(kind) {
	case "list":
		m.phaseRecListTotal.Add(1)
	case "read":
		m.phaseRecReadTotal.Add(1)
	case "verify":
		m.phaseRecVerifyTotal.Add(1)
	case "ask_clarification":
		m.phaseRecClarTotal.Add(1)
	default:
		m.phaseRecOtherTotal.Add(1)
	}
}

func (m *runtimeMetrics) recordPhaseRecoveryOutcome(kind string, ok bool) {
	if m == nil || !ok {
		return
	}
	m.phaseRecoveryOK.Add(1)
	switch normalizePhaseRecoveryKind(kind) {
	case "list":
		m.phaseRecListOK.Add(1)
	case "read":
		m.phaseRecReadOK.Add(1)
	case "verify":
		m.phaseRecVerifyOK.Add(1)
	case "ask_clarification":
		m.phaseRecClarOK.Add(1)
	default:
		m.phaseRecOtherOK.Add(1)
	}
}

func normalizePhaseRecoveryKind(kind string) string {
	kind = normalizeRecoveryKind(kind)
	switch kind {
	case "list", "read", "verify", "ask_clarification":
		return kind
	default:
		return "other"
	}
}

func normalizeRecoveryKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if strings.HasPrefix(kind, "tool_call.") {
		kind = strings.TrimPrefix(kind, "tool_call.")
	}
	return kind
}

func safeRate(num, den int64) float64 {
	if den <= 0 {
		return 0
	}
	return float64(num) / float64(den)
}
