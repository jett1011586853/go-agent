package tests

import (
	"context"
	"sync"
	"testing"
	"time"

	"trade-lab/internal/fault"
)

func TestFaultInjectorBasic(t *testing.T) {
	injector := fault.NewInjector()

	// Test inject and auto-recover
	f := &fault.Fault{
		Type: fault.FaultTimeout,
		Enabled: true,
		Duration: 100 * time.Millisecond,
		Intensity: 0.5,
	}

	err := injector.Inject(context.Background(), f)
	if err != nil {
		t.Fatalf("inject failed: %v", err)
	}

	if !injector.IsFaultActive(fault.FaultTimeout) {
		t.Error("fault should be active")
	}

	// Wait for auto-recovery
	time.Sleep(150 * time.Millisecond)

	if injector.IsFaultActive(fault.FaultTimeout) {
		t.Error("fault should have auto-recovered")
	}

	metrics := injector.GetMetrics()
	if metrics.TotalInjected != 1 {
		t.Errorf("expected 1 injected, got %d", metrics.TotalInjected)
	}
	if metrics.TotalRecovered != 1 {
		t.Errorf("expected 1 recovered, got %d", metrics.TotalRecovered)
	}
}

func TestFaultInjectorManualRecover(t *testing.T) {
	injector := fault.NewInjector()

	f := &fault.Fault{
		Type: fault.FaultDBLock,
		Enabled: true,
		Duration: 10 * time.Second, // Long duration
		Intensity: 1.0,
	}

	injector.Inject(context.Background(), f)

	if !injector.IsFaultActive(fault.FaultDBLock) {
		t.Error("fault should be active")
	}

	// Manual recovery
	injector.Recover(fault.FaultDBLock)

	time.Sleep(50 * time.Millisecond)

	if injector.IsFaultActive(fault.FaultDBLock) {
		t.Error("fault should be recovered")
	}
}

func TestFaultInjectorMultipleFaults(t *testing.T) {
	injector := fault.NewInjector()

	faults := []*fault.Fault{
		{Type: fault.FaultTimeout, Enabled: true, Duration: 200 * time.Millisecond},
		{Type: fault.FaultMsgBacklog, Enabled: true, Duration: 200 * time.Millisecond},
		{Type: fault.FaultBadData, Enabled: true, Duration: 200 * time.Millisecond},
	}

	for _, f := range faults {
		injector.Inject(context.Background(), f)
	}

	metrics := injector.GetMetrics()
	if metrics.ActiveFaults != 3 {
		t.Errorf("expected 3 active faults, got %d", metrics.ActiveFaults)
	}

	// Recover all
	injector.RecoverAll()

	time.Sleep(50 * time.Millisecond)

	metrics = injector.GetMetrics()
	if metrics.ActiveFaults != 0 {
		t.Errorf("expected 0 active faults, got %d", metrics.ActiveFaults)
	}
}

func TestFaultInjectorDisabled(t *testing.T) {
	injector := fault.NewInjector()

	f := &fault.Fault{
		Type: fault.FaultTimeout,
		Enabled: false, // Disabled
		Duration: 100 * time.Millisecond,
	}

	injector.Inject(context.Background(), f)

	if injector.IsFaultActive(fault.FaultTimeout) {
		t.Error("disabled fault should not be active")
	}
}

func TestRecoveryConfig(t *testing.T) {
	config := fault.DefaultRecoveryConfig()

	if config.MaxRetries != 3 {
		t.Errorf("expected 3 retries, got %d", config.MaxRetries)
	}
	if config.Strategy != fault.StrategyRetry {
		t.Errorf("expected StrategyRetry, got %d", config.Strategy)
	}
	if config.RetryDelay != 100*time.Millisecond {
		t.Errorf("expected 100ms retry delay, got %v", config.RetryDelay)
	}
}

func TestConcurrentFaultInjection(t *testing.T) {
	injector := fault.NewInjector()
	var wg sync.WaitGroup

	// Concurrent injections
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := &fault.Fault{
				Type: fault.FaultTimeout,
				Enabled: true,
				Duration: 50 * time.Millisecond,
			}
			injector.Inject(context.Background(), f)
		}()
	}

	wg.Wait()

	metrics := injector.GetMetrics()
	if metrics.TotalInjected != 10 {
		t.Errorf("expected 10 injections, got %d", metrics.TotalInjected)
	}
}

func TestFaultTypes(t *testing.T) {
	types := []fault.FaultType{
		fault.FaultDBLock,
		fault.FaultMsgBacklog,
		fault.FaultBadData,
		fault.FaultTimeout,
		fault.FaultMemoryPressure,
	}

	injector := fault.NewInjector()

	for _, ft := range types {
		f := &fault.Fault{
			Type: ft,
			Enabled: true,
			Duration: 50 * time.Millisecond,
		}
		err := injector.Inject(context.Background(), f)
		if err != nil {
			t.Errorf("failed to inject %s: %v", ft, err)
		}
	}

	time.Sleep(100 * time.Millisecond)

	metrics := injector.GetMetrics()
	if metrics.TotalInjected != 5 {
		t.Errorf("expected 5 injections, got %d", metrics.TotalInjected)
	}
}
