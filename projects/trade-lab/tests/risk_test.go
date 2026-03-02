package tests

import (
	"context"
	"testing"
	"time"

	"trade-lab/internal/errors"
	"trade-lab/internal/eventbus"
	"trade-lab/internal/order"
	"trade-lab/internal/risk"
)

func TestSingleOrderLimitRule(t *testing.T) {
	bus := eventbus.NewInMemoryBus(1000)
	state := risk.NewState()
	controller := risk.NewController(state, bus)

	// Add rule: max order value 10000
	controller.AddRule(risk.NewSingleOrderLimitRule(10000))

	// Test: order within limit
	ord := &order.Order{
		ID: "test-1",
		Symbol: "BTCUSDT",
		Side: order.SideBuy,
		Type: order.TypeMarket,
		Quantity: 0.1,
		Price: 50000,
	}

	err := controller.CheckOrder(context.Background(), ord)
	if err != nil {
		t.Errorf("expected order to pass, got error: %v", err)
	}

	// Test: order exceeds limit
	ord2 := &order.Order{
		ID: "test-2",
		Symbol: "BTCUSDT",
		Side: order.SideBuy,
		Type: order.TypeMarket,
		Quantity: 1,
		Price: 50000,
	}

	err = controller.CheckOrder(context.Background(), ord2)
	if err == nil {
		t.Error("expected order to be rejected")
	}
	if !errors.Is(err, errors.ErrRiskLimitExceeded) {
		t.Errorf("expected ErrRiskLimitExceeded, got: %v", err)
	}
}

func TestDailyLossLimitRule(t *testing.T) {
	bus := eventbus.NewInMemoryBus(1000)
	state := risk.NewState()
	state.DailyLossLimit = 1000
	state.DailyPnL = -500 // Already lost 500

	controller := risk.NewController(state, bus)
	controller.AddRule(risk.NewDailyLossLimitRule(1000))

	// Test: within limit
	ord := &order.Order{
		ID: "test-1",
		Symbol: "BTCUSDT",
		Side: order.SideBuy,
		Quantity: 0.1,
		Price: 50000,
	}

	err := controller.CheckOrder(context.Background(), ord)
	if err != nil {
		t.Errorf("expected order to pass: %v", err)
	}

	// Test: exceeds limit
	state.DailyPnL = -1500
	err = controller.CheckOrder(context.Background(), ord)
	if err == nil {
		t.Error("expected order to be rejected")
	}
}

func TestCircuitBreakerRule(t *testing.T) {
	bus := eventbus.NewInMemoryBus(1000)
	state := risk.NewState()
	controller := risk.NewController(state, bus)
	controller.AddRule(risk.NewCircuitBreakerRule())

	// Test: normal operation
	ord := &order.Order{
		ID: "test-1",
		Symbol: "BTCUSDT",
		Side: order.SideBuy,
		Quantity: 0.1,
		Price: 50000,
	}

	err := controller.CheckOrder(context.Background(), ord)
	if err != nil {
		t.Errorf("expected order to pass: %v", err)
	}

	// Test: circuit breaker triggered
	state.CircuitBreakerTriggered = true
	state.CircuitBreakerTime = time.Now()

	err = controller.CheckOrder(context.Background(), ord)
	if err == nil {
		t.Error("expected order to be rejected")
	}
	if !errors.Is(err, errors.ErrRiskCircuitBreaker) {
		t.Errorf("expected ErrRiskCircuitBreaker, got: %v", err)
	}
}

func TestAuditLog(t *testing.T) {
	bus := eventbus.NewInMemoryBus(1000)
	state := risk.NewState()
	controller := risk.NewController(state, bus)
	controller.AddRule(risk.NewSingleOrderLimitRule(10000))

	// Submit multiple orders
	for i := 0; i < 5; i++ {
		ord := &order.Order{
			ID: string(rune('0' + i)),
			Symbol: "BTCUSDT",
			Side: order.SideBuy,
			Type: order.TypeMarket,
			Quantity: 0.1,
			Price: 50000,
		}
		controller.CheckOrder(context.Background(), ord)
	}

	auditLog := controller.GetAuditLog()
	if len(auditLog) != 5 {
		t.Errorf("expected 5 audit entries, got %d", len(auditLog))
	}

	for i, entry := range auditLog {
		t.Logf("Audit %d: OrderID=%s, Rule=%s, Action=%s",
			i, entry.OrderID, entry.Rule, entry.Action)
	}
}
