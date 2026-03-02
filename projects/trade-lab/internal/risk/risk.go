package risk

import (
	"context"
	"fmt"
	"sync"
	"time"

	"trade-lab/internal/errors"
	"trade-lab/internal/eventbus"
	"trade-lab/internal/order"
)

// Rule represents a risk rule
type Rule interface {
	Name() string
	Check(ctx context.Context, ord *order.Order, state *State) error
}

// State represents the current risk state
type State struct {
	mu sync.RWMutex
	DailyPnL float64
	DailyLossLimit float64
	Positions map[string]float64 // symbol -> quantity
	PositionLimit float64
	TotalPositionValue float64
	SingleOrderLimit float64
	CircuitBreakerTriggered bool
	CircuitBreakerTime time.Time
	OrderCount int
	OrderCountLimit int
}

// NewState creates a new risk state
func NewState() *State {
	return &State{
		Positions: make(map[string]float64),
	}
}

// Controller is the risk controller
type Controller struct {
	rules []Rule
	state *State
	bus eventbus.Bus
	auditLog []AuditEntry
}

// AuditEntry represents an audit log entry
type AuditEntry struct {
	Timestamp time.Time
	OrderID string
	Rule string
	Action string // approved/rejected
	Reason string
}

// NewController creates a new risk controller
func NewController(state *State, bus eventbus.Bus) *Controller {
	return &Controller{
		rules: make([]Rule, 0),
		state: state,
		bus: bus,
		auditLog: make([]AuditEntry, 0),
	}
}

// AddRule adds a risk rule
func (c *Controller) AddRule(rule Rule) {
	c.rules = append(c.rules, rule)
}

// CheckOrder checks an order against all risk rules
func (c *Controller) CheckOrder(ctx context.Context, ord *order.Order) error {
	c.state.mu.RLock()
	defer c.state.mu.RUnlock()

	for _, rule := range c.rules {
		if err := rule.Check(ctx, ord, c.state); err != nil {
			c.logAudit(ord.ID, rule.Name(), "rejected", err.Error())
			c.publishRejection(ctx, ord, rule.Name(), err.Error())
			return err
		}
	}

	c.logAudit(ord.ID, "all", "approved", "")
	return nil
}

func (c *Controller) logAudit(orderID, rule, action, reason string) {
	c.auditLog = append(c.auditLog, AuditEntry{
		Timestamp: time.Now(),
		OrderID: orderID,
		Rule: rule,
		Action: action,
		Reason: reason,
	})
}

func (c *Controller) publishRejection(ctx context.Context, ord *order.Order, rule, reason string) {
	evt := eventbus.Event{
		ID: "risk-reject-" + ord.ID,
		Type: eventbus.EventOrderRejected,
		Timestamp: time.Now(),
		Source: "risk_controller",
		Payload: map[string]interface{}{
			"order_id": ord.ID,
			"rule": rule,
			"reason": reason,
		},
	}
	c.bus.Publish(ctx, evt)
}

// GetAuditLog returns the audit log
func (c *Controller) GetAuditLog() []AuditEntry {
	return c.auditLog
}

// --- Risk Rules ---

// SingleOrderLimitRule checks single order size limit
type SingleOrderLimitRule struct {
	limit float64
}

func NewSingleOrderLimitRule(limit float64) *SingleOrderLimitRule {
	return &SingleOrderLimitRule{limit: limit}
}

func (r *SingleOrderLimitRule) Name() string {
	return "single_order_limit"
}

func (r *SingleOrderLimitRule) Check(ctx context.Context, ord *order.Order, state *State) error {
	orderValue := ord.Quantity * ord.Price
	if orderValue > r.limit {
		return errors.New(errors.ErrRiskLimitExceeded, 
			"order value exceeds limit").WithDetail(fmt.Sprintf("value=%.2f, limit=%.2f", orderValue, r.limit))
	}
	return nil
}

// DailyLossLimitRule checks daily loss limit
type DailyLossLimitRule struct {
	limit float64
}

func NewDailyLossLimitRule(limit float64) *DailyLossLimitRule {
	return &DailyLossLimitRule{limit: limit}
}

func (r *DailyLossLimitRule) Name() string {
	return "daily_loss_limit"
}

func (r *DailyLossLimitRule) Check(ctx context.Context, ord *order.Order, state *State) error {
	if state.DailyPnL < -r.limit {
		return errors.New(errors.ErrRiskDailyLossLimit,
			"daily loss limit exceeded").WithDetail(fmt.Sprintf("pnl=%.2f, limit=%.2f", state.DailyPnL, r.limit))
	}
	return nil
}

// PositionLimitRule checks position limit
type PositionLimitRule struct {
	limit float64
}

func NewPositionLimitRule(limit float64) *PositionLimitRule {
	return &PositionLimitRule{limit: limit}
}

func (r *PositionLimitRule) Name() string {
	return "position_limit"
}

func (r *PositionLimitRule) Check(ctx context.Context, ord *order.Order, state *State) error {
	newValue := ord.Quantity * ord.Price
	if state.TotalPositionValue+newValue > r.limit {
		return errors.New(errors.ErrRiskPositionLimit,
			"position limit exceeded").WithDetail(fmt.Sprintf("current=%.2f, new=%.2f, limit=%.2f",
				state.TotalPositionValue, newValue, r.limit))
	}
	return nil
}

// CircuitBreakerRule checks circuit breaker
type CircuitBreakerRule struct{}

func NewCircuitBreakerRule() *CircuitBreakerRule {
	return &CircuitBreakerRule{}
}

func (r *CircuitBreakerRule) Name() string {
	return "circuit_breaker"
}

func (r *CircuitBreakerRule) Check(ctx context.Context, ord *order.Order, state *State) error {
	if state.CircuitBreakerTriggered {
		return errors.New(errors.ErrRiskCircuitBreaker,
			"circuit breaker triggered").WithDetail(fmt.Sprintf("time=%v", state.CircuitBreakerTime))
	}
	return nil
}

var _ Rule = (*SingleOrderLimitRule)(nil)
var _ Rule = (*DailyLossLimitRule)(nil)
var _ Rule = (*PositionLimitRule)(nil)
var _ Rule = (*CircuitBreakerRule)(nil)
