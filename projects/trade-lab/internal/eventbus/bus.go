package eventbus

import (
	"context"
	"sync"
	"time"
)

// EventType represents the type of event
type EventType string

const (
	// Market data events
	EventMarketData EventType = "market_data"
	EventKline EventType = "kline"
	EventTick EventType = "tick"

	// Signal events
	EventSignal EventType = "signal"

	// Order events
	EventOrder EventType = "order"
	EventOrderFilled EventType = "order_filled"
	EventOrderRejected EventType = "order_rejected"

	// Risk events
	EventRiskAlert EventType = "risk_alert"
	EventCircuitBreaker EventType = "circuit_breaker"
)

// Event represents a generic event
type Event struct {
	ID string `json:"id"`
	Type EventType `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Source string `json:"source"`
	Payload interface{} `json:"payload"`
}

// Handler is the event handler function
type Handler func(ctx context.Context, evt Event) error

// Bus is the event bus interface
type Bus interface {
	Publish(ctx context.Context, evt Event) error
	Subscribe(eventType EventType, handler Handler) string
	Unsubscribe(subscriptionID string)
	Replay(ctx context.Context, from time.Time, to time.Time) error
}

// InMemoryBus is an in-memory event bus implementation
type InMemoryBus struct {
	mu sync.RWMutex
	handlers map[EventType]map[string]Handler
	history []Event
	maxHistory int
}

// NewInMemoryBus creates a new in-memory event bus
func NewInMemoryBus(maxHistory int) *InMemoryBus {
	return &InMemoryBus{
		handlers: make(map[EventType]map[string]Handler),
		history: make([]Event, 0),
		maxHistory: maxHistory,
	}
}

// Publish publishes an event to all subscribers
func (b *InMemoryBus) Publish(ctx context.Context, evt Event) error {
	b.mu.Lock()
	b.history = append(b.history, evt)
	if len(b.history) > b.maxHistory {
		b.history = b.history[len(b.history)-b.maxHistory:]
	}
	handlers, ok := b.handlers[evt.Type]
	b.mu.Unlock()

	if !ok {
		return nil
	}

	for _, h := range handlers {
		if err := h(ctx, evt); err != nil {
			return err
		}
	}
	return nil
}

// Subscribe subscribes to an event type
func (b *InMemoryBus) Subscribe(eventType EventType, handler Handler) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.handlers[eventType] == nil {
		b.handlers[eventType] = make(map[string]Handler)
	}

	id := generateID()
	b.handlers[eventType][id] = handler
	return id
}

// Unsubscribe removes a subscription
func (b *InMemoryBus) Unsubscribe(subscriptionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, handlers := range b.handlers {
		delete(handlers, subscriptionID)
	}
}

// Replay replays events from history
func (b *InMemoryBus) Replay(ctx context.Context, from, to time.Time) error {
	b.mu.RLock()
	history := make([]Event, len(b.history))
	copy(history, b.history)
	b.mu.RUnlock()

	for _, evt := range history {
		if evt.Timestamp.Before(from) || evt.Timestamp.After(to) {
			continue
		}
		if err := b.Publish(ctx, evt); err != nil {
			return err
		}
	}
	return nil
}

func generateID() string {
	return time.Now().Format("20060102150405.000000000")
}
