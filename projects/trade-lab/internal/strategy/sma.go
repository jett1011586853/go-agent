package strategy

import (
	"context"
	"fmt"
	"trade-lab/internal/eventbus"
	"trade-lab/internal/marketdata"
)

// SMAStrategy is a Simple Moving Average crossover strategy
type SMAStrategy struct {
	*BaseStrategy
	shortPeriod int
	longPeriod int
	shortPrices []float64
	longPrices []float64
}

// NewSMAStrategy creates a new SMA strategy
func NewSMAStrategy(shortPeriod, longPeriod int) *SMAStrategy {
	return &SMAStrategy{
		BaseStrategy: NewBaseStrategy("sma"),
		shortPeriod: shortPeriod,
		longPeriod: longPeriod,
		shortPrices: make([]float64, 0),
		longPrices: make([]float64, 0),
	}
}

func (s *SMAStrategy) OnEvent(ctx context.Context, evt eventbus.Event) (*Signal, error) {
	if evt.Type != eventbus.EventKline {
		return nil, nil
	}

	kline, ok := evt.Payload.(marketdata.Kline)
	if !ok {
		return nil, fmt.Errorf("invalid kline payload")
	}

	s.shortPrices = append(s.shortPrices, kline.Close)
	s.longPrices = append(s.longPrices, kline.Close)

	// Keep only needed prices
	if len(s.shortPrices) > s.shortPeriod {
		s.shortPrices = s.shortPrices[1:]
	}
	if len(s.longPrices) > s.longPeriod {
		s.longPrices = s.longPrices[1:]
	}

	// Need enough data
	if len(s.longPrices) < s.longPeriod {
		return &Signal{Symbol: kline.Symbol, Action: "hold", Price: kline.Close}, nil
	}

	shortMA := calculateMA(s.shortPrices)
	longMA := calculateMA(s.longPrices)

	// Determine signal
	var action string
	var strength float64

	if shortMA > longMA {
		action = "buy"
		strength = (shortMA - longMA) / longMA
		if strength > 1.0 {
			strength = 1.0
		}
	} else if shortMA < longMA {
		action = "sell"
		strength = (longMA - shortMA) / longMA
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

func (s *SMAStrategy) Reset() {
	s.shortPrices = make([]float64, 0)
	s.longPrices = make([]float64, 0)
}

func (s *SMAStrategy) SetParams(params map[string]interface{}) error {
	if v, ok := params["short_period"]; ok {
		if period, ok := v.(int); ok {
			s.shortPeriod = period
		}
	}
	if v, ok := params["long_period"]; ok {
		if period, ok := v.(int); ok {
			s.longPeriod = period
		}
	}
	return nil
}

func calculateMA(prices []float64) float64 {
	if len(prices) == 0 {
		return 0
	}
	var sum float64
	for _, p := range prices {
		sum += p
	}
	return sum / float64(len(prices))
}

var _ Strategy = (*SMAStrategy)(nil)
