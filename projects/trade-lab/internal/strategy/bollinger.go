package strategy

import (
	"context"
	"math"
	"trade-lab/internal/eventbus"
	"trade-lab/internal/marketdata"
)

// BollingerBandsStrategy is a Bollinger Bands mean reversion strategy
type BollingerBandsStrategy struct {
	*BaseStrategy
	period int
	stdDev float64
	prices []float64
}

// NewBollingerBandsStrategy creates a new Bollinger Bands strategy
func NewBollingerBandsStrategy(period int, stdDev float64) *BollingerBandsStrategy {
	return &BollingerBandsStrategy{
		BaseStrategy: NewBaseStrategy("bollinger"),
		period: period,
		stdDev: stdDev,
		prices: make([]float64, 0),
	}
}

func (s *BollingerBandsStrategy) OnEvent(ctx context.Context, evt eventbus.Event) (*Signal, error) {
	if evt.Type != eventbus.EventKline {
		return nil, nil
	}

	kline, ok := evt.Payload.(marketdata.Kline)
	if !ok {
		return nil, nil
	}

	s.prices = append(s.prices, kline.Close)
	if len(s.prices) > s.period {
		s.prices = s.prices[1:]
	}

	if len(s.prices) < s.period {
		return &Signal{Symbol: kline.Symbol, Action: "hold", Price: kline.Close}, nil
	}

	// Calculate Bollinger Bands
	middle := calculateMA(s.prices)
	std := calculateStd(s.prices, middle)
	upper := middle + s.stdDev*std
	lower := middle - s.stdDev*std

	var action string
	var strength float64

	// Price touches lower band -> buy signal
	if kline.Close <= lower {
		action = "buy"
		strength = (lower - kline.Close) / lower
		if strength > 1.0 {
			strength = 1.0
		}
	} else if kline.Close >= upper {
		// Price touches upper band -> sell signal
		action = "sell"
		strength = (kline.Close - upper) / upper
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

func (s *BollingerBandsStrategy) Reset() {
	s.prices = make([]float64, 0)
}

func calculateStd(prices []float64, mean float64) float64 {
	var sum float64
	for _, p := range prices {
		sum += math.Pow(p-mean, 2)
	}
	return math.Sqrt(sum / float64(len(prices)))
}

var _ Strategy = (*BollingerBandsStrategy)(nil)
