package strategy

import (
	"context"
	"trade-lab/internal/eventbus"
	"trade-lab/internal/marketdata"
)

// MomentumStrategy is a momentum-based strategy
type MomentumStrategy struct {
	*BaseStrategy
	period int
	prices []float64
}

// NewMomentumStrategy creates a new momentum strategy
func NewMomentumStrategy(period int) *MomentumStrategy {
	return &MomentumStrategy{
		BaseStrategy: NewBaseStrategy("momentum"),
		period: period,
		prices: make([]float64, 0),
	}
}

func (s *MomentumStrategy) OnEvent(ctx context.Context, evt eventbus.Event) (*Signal, error) {
	if evt.Type != eventbus.EventKline {
		return nil, nil
	}

	kline, ok := evt.Payload.(marketdata.Kline)
	if !ok {
		return nil, nil
	}

	s.prices = append(s.prices, kline.Close)
	if len(s.prices) > s.period+1 {
		s.prices = s.prices[1:]
	}

	if len(s.prices) < s.period+1 {
		return &Signal{Symbol: kline.Symbol, Action: "hold", Price: kline.Close}, nil
	}

	// Calculate momentum (rate of change)
	currentPrice := s.prices[len(s.prices)-1]
	pastPrice := s.prices[0]
	momentum := (currentPrice - pastPrice) / pastPrice

	var action string
	var strength float64

	// Positive momentum -> buy, negative -> sell
	if momentum > 0.01 {
		action = "buy"
		strength = momentum
		if strength > 1.0 {
			strength = 1.0
		}
	} else if momentum < -0.01 {
		action = "sell"
		strength = -momentum
		if strength > 1.0 {
			strength = 1.0
		}
	} else {
		action = "hold"
		strength = 0
	}

	return &Signal{
		Symbol: kline.Symbol,
		Action: action,
		Strength: strength,
		Price: kline.Close,
		Timestamp: evt.Timestamp.Unix(),
	}, nil
}

func (s *MomentumStrategy) Reset() {
	s.prices = make([]float64, 0)
}

var _ Strategy = (*MomentumStrategy)(nil)
