package strategy

import (
	"context"
	"trade-lab/internal/eventbus"
)

// Signal represents a trading signal
type Signal struct {
	Symbol string `json:"symbol"`
	Action string `json:"action"` // buy, sell, hold
	Strength float64 `json:"strength"` // 0.0 - 1.0
	Price float64 `json:"price"`
	Timestamp int64 `json:"timestamp"`
}

// Strategy is the interface for trading strategies
type Strategy interface {
	// Name returns the strategy name
	Name() string
	
	// OnEvent processes a market event and returns a signal
	OnEvent(ctx context.Context, evt eventbus.Event) (*Signal, error)
	
	// Reset resets the strategy state
	Reset()
	
	// SetParams sets strategy parameters
	SetParams(params map[string]interface{}) error
	
	// GetParams returns current parameters
	GetParams() map[string]interface{}
}

// BaseStrategy provides common functionality for strategies
type BaseStrategy struct {
	name string
	params map[string]interface{}
}

func NewBaseStrategy(name string) *BaseStrategy {
	return &BaseStrategy{
		name: name,
		params: make(map[string]interface{}),
	}
}

func (s *BaseStrategy) Name() string {
	return s.name
}

func (s *BaseStrategy) SetParams(params map[string]interface{}) error {
	for k, v := range params {
		s.params[k] = v
	}
	return nil
}

func (s *BaseStrategy) GetParams() map[string]interface{} {
	return s.params
}

func (s *BaseStrategy) Reset() {
	// Override in subclass
}
