package fault

import (
	"context"
	"sync"
	"time"
)

// FaultType represents different fault types
type FaultType string

const (
	FaultDBLock       FaultType = "db_lock"
	FaultMsgBacklog   FaultType = "msg_backlog"
	FaultBadData      FaultType = "bad_data"
	FaultTimeout      FaultType = "timeout"
	FaultMemoryPressure FaultType = "memory_pressure"
)

// Fault represents a fault injection configuration
type Fault struct {
	Type       FaultType      `json:"type"`
	Enabled    bool           `json:"enabled"`
	Duration   time.Duration  `json:"duration"`
	Intensity  float64        `json:"intensity"` // 0.0 - 1.0
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// Injector manages fault injection
type Injector struct {
	mu       sync.RWMutex
	faults   map[FaultType]*Fault
	active   map[FaultType]context.CancelFunc
	metrics  *Metrics
}

// Metrics tracks fault injection metrics
type Metrics struct {
	mu              sync.RWMutex
	TotalInjected   int64
	TotalRecovered  int64
	ActiveFaults    int64
	RecoveryTime    time.Duration
	FailureCount    map[FaultType]int64
}

// NewInjector creates a new fault injector
func NewInjector() *Injector {
	return &Injector{
		faults:  make(map[FaultType]*Fault),
		active:  make(map[FaultType]context.CancelFunc),
		metrics: &Metrics{FailureCount: make(map[FaultType]int64)},
	}
}

// Inject injects a fault
func (i *Injector) Inject(ctx context.Context, f *Fault) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !f.Enabled {
		return nil
	}

	i.faults[f.Type] = f
	faultCtx, cancel := context.WithCancel(ctx)
	i.active[f.Type] = cancel

	i.metrics.mu.Lock()
	i.metrics.TotalInjected++
	i.metrics.ActiveFaults++
	i.metrics.mu.Unlock()

	go i.runFault(faultCtx, f)
	return nil
}

// runFault executes the fault simulation
func (i *Injector) runFault(ctx context.Context, f *Fault) {
	timer := time.NewTimer(f.Duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		i.recover(f.Type)
	case <-timer.C:
		i.recover(f.Type)
	}
}

// recover marks a fault as recovered
func (i *Injector) recover(ft FaultType) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if _, ok := i.active[ft]; ok {
		delete(i.active, ft)
		i.metrics.mu.Lock()
		i.metrics.TotalRecovered++
		i.metrics.ActiveFaults--
		i.metrics.mu.Unlock()
	}
}

// Recover stops a specific fault
func (i *Injector) Recover(ft FaultType) {
	i.mu.RLock()
	cancel, ok := i.active[ft]
	i.mu.RUnlock()

	if ok {
		cancel()
	}
}

// RecoverAll stops all active faults
func (i *Injector) RecoverAll() {
	i.mu.RLock()
	cancels := make([]context.CancelFunc, 0, len(i.active))
	for _, cancel := range i.active {
		cancels = append(cancels, cancel)
	}
	i.mu.RUnlock()

	for _, cancel := range cancels {
		cancel()
	}
}

// GetMetrics returns current metrics
func (i *Injector) GetMetrics() *Metrics {
	i.metrics.mu.RLock()
	defer i.metrics.mu.RUnlock()
	return i.metrics
}

// IsFaultActive checks if a fault is active
func (i *Injector) IsFaultActive(ft FaultType) bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	_, ok := i.active[ft]
	return ok
}

// DegradationStrategy defines recovery behavior
type DegradationStrategy int

const (
	StrategyRetry DegradationStrategy = iota
	StrategyFallback
	StrategyCircuitBreak
	StrategyReject
)

// RecoveryConfig configures recovery behavior
type RecoveryConfig struct {
	MaxRetries      int
	RetryDelay      time.Duration
	Timeout         time.Duration
	Strategy        DegradationStrategy
	FallbackHandler func(error) error
}

// DefaultRecoveryConfig returns default recovery config
func DefaultRecoveryConfig() *RecoveryConfig {
	return &RecoveryConfig{
		MaxRetries: 3,
		RetryDelay: 100 * time.Millisecond,
		Timeout:    5 * time.Second,
		Strategy:   StrategyRetry,
	}
}
