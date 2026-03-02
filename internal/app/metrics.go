package app

import (
	"fmt"
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
}

type metricsSnapshot struct {
	TurnsTotal        int64
	SkillHitRate      float64
	WrongCallRate     float64
	RecoverySuccess   float64
	AvgPlannerPerTurn float64
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

	return metricsSnapshot{
		TurnsTotal:        turns,
		SkillHitRate:      safeRate(withSkills, turns),
		WrongCallRate:     safeRate(toolErrors, toolCalls),
		RecoverySuccess:   safeRate(recoverySuccess, recoveryAttempts),
		AvgPlannerPerTurn: safeRate(steps, turns),
	}
}

func (m metricsSnapshot) String() string {
	return fmt.Sprintf(
		"metrics: turns=%d skill_hit=%.1f%% wrong_call=%.1f%% recovery=%.1f%% avg_planner_steps=%.2f",
		m.TurnsTotal,
		m.SkillHitRate*100,
		m.WrongCallRate*100,
		m.RecoverySuccess*100,
		m.AvgPlannerPerTurn,
	)
}

func safeRate(num, den int64) float64 {
	if den <= 0 {
		return 0
	}
	return float64(num) / float64(den)
}
