package tests

import (
	"context"
	"testing"
	"time"

	"trade-lab/internal/backtest"
	"trade-lab/internal/eventbus"
	"trade-lab/internal/marketdata"
	"trade-lab/internal/strategy"
)

func TestSMAStrategyBacktest(t *testing.T) {
	bus := eventbus.NewInMemoryBus(10000)
	strat := strategy.NewSMAStrategy(5, 10)

	config := backtest.Config{
		InitialCapital: 100000,
		CommissionRate: 0.001,
		SlippageRate: 0.0005,
		PositionSize: 0.95,
	}

	engine := backtest.NewEngine(config, strat, bus)

	// Simulate market data
	go func() {
		prices := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110}
		for i, price := range prices {
			kline := marketdata.Kline{
				Symbol: "TEST",
				OpenTime: time.Now().Add(time.Duration(i) * time.Hour),
				Open: price,
				High: price + 1,
				Low: price - 1,
				Close: price,
				Volume: 1000,
				CloseTime: time.Now().Add(time.Duration(i)*time.Hour + 59*time.Minute),
			}
			evt := eventbus.Event{
				ID: string(rune('0' + i)),
				Type: eventbus.EventKline,
				Timestamp: kline.OpenTime,
				Source: "test",
				Payload: kline,
			}
			bus.Publish(context.Background(), evt)
		}
	}()

	_, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("backtest run failed: %v", err)
	}

	report := engine.GenerateReport()
	if report.InitialCapital != 100000 {
		t.Errorf("expected initial capital 100000, got %f", report.InitialCapital)
	}

	t.Logf("Backtest Report:")
	t.Logf(" Final Equity: %.2f", report.FinalEquity)
	t.Logf(" Total Return: %.2f%%", report.TotalReturnPct)
	t.Logf(" Total Trades: %d", report.TotalTrades)
}

func TestBollingerBandsStrategy(t *testing.T) {
	bus := eventbus.NewInMemoryBus(10000)
	strat := strategy.NewBollingerBandsStrategy(20, 2.0)

	config := backtest.Config{
		InitialCapital: 100000,
		CommissionRate: 0.001,
		SlippageRate: 0.0005,
		PositionSize: 0.95,
	}

	engine := backtest.NewEngine(config, strat, bus)

	_, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("backtest run failed: %v", err)
	}

	report := engine.GenerateReport()
	t.Logf("Bollinger Bands Report:")
	t.Logf(" Final Equity: %.2f", report.FinalEquity)
}

func TestMomentumStrategy(t *testing.T) {
	bus := eventbus.NewInMemoryBus(10000)
	strat := strategy.NewMomentumStrategy(5)

	config := backtest.Config{
		InitialCapital: 100000,
		CommissionRate: 0.001,
		SlippageRate: 0.0005,
		PositionSize: 0.95,
	}

	engine := backtest.NewEngine(config, strat, bus)

	_, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("backtest run failed: %v", err)
	}

	report := engine.GenerateReport()
	t.Logf("Momentum Report:")
	t.Logf(" Final Equity: %.2f", report.FinalEquity)
}
