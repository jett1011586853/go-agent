package backtest

import (
	"context"
	"fmt"
	"math"
	"time"

	"trade-lab/internal/eventbus"
	"trade-lab/internal/strategy"
)

// Config is the backtest configuration
type Config struct {
	InitialCapital float64
	CommissionRate float64
	SlippageRate float64
	PositionSize float64 // fraction of capital (0.0-1.0)
}

// Position represents a position
type Position struct {
	Symbol string
	Quantity float64
	EntryPrice float64
	CurrentPrice float64
}

// Trade represents a trade record
type Trade struct {
	ID string
	Symbol string
	Side string // buy/sell
	Quantity float64
	Price float64
	Commission float64
	Slippage float64
	Timestamp time.Time
	PNL float64
}

// EquityPoint represents a point in equity curve
type EquityPoint struct {
	Timestamp time.Time
	Equity float64
}

// Report is the backtest report
type Report struct {
	InitialCapital float64
	FinalEquity float64
	TotalReturn float64
	TotalReturnPct float64
	MaxDrawdown float64
	MaxDrawdownPct float64
	SharpeRatio float64
	WinRate float64
	TotalTrades int
	WinningTrades int
	LosingTrades int
	EquityCurve []EquityPoint
	Trades []Trade
}

// Engine is the backtest engine
type Engine struct {
	config Config
	strategy strategy.Strategy
	bus eventbus.Bus
	
	// State
	cash float64
	positions map[string]*Position
	trades []Trade
	equityCurve []EquityPoint
	
	// Metrics
	peakEquity float64
	maxDrawdown float64
	returns []float64
}

// NewEngine creates a new backtest engine
func NewEngine(config Config, strat strategy.Strategy, bus eventbus.Bus) *Engine {
	return &Engine{
		config: config,
		strategy: strat,
		bus: bus,
		cash: config.InitialCapital,
		positions: make(map[string]*Position),
		trades: make([]Trade, 0),
		equityCurve: make([]EquityPoint, 0),
		returns: make([]float64, 0),
	}
}

// Run runs the backtest
func (e *Engine) Run(ctx context.Context) (*Report, error) {
	// Subscribe to signals
	e.bus.Subscribe(eventbus.EventSignal, func(ctx context.Context, evt eventbus.Event) error {
		sig, ok := evt.Payload.(*strategy.Signal)
		if !ok {
			return nil
		}
		return e.handleSignal(sig, evt.Timestamp)
	})

	// Subscribe to market data for position updates
	e.bus.Subscribe(eventbus.EventKline, func(ctx context.Context, evt eventbus.Event) error {
		return e.updatePositions(evt)
	})

	// Run strategy on events
	e.bus.Subscribe(eventbus.EventKline, func(ctx context.Context, evt eventbus.Event) error {
		sig, err := e.strategy.OnEvent(ctx, evt)
		if err != nil {
			return err
		}
		if sig != nil && sig.Action != "hold" {
			sigEvt := eventbus.Event{
				ID: fmt.Sprintf("sig-%d", evt.Timestamp.UnixNano()),
				Type: eventbus.EventSignal,
				Timestamp: evt.Timestamp,
				Source: e.strategy.Name(),
				Payload: sig,
			}
			return e.bus.Publish(ctx, sigEvt)
		}
		return nil
	})

	return nil, nil
}

func (e *Engine) handleSignal(sig *strategy.Signal, ts time.Time) error {
	pos, exists := e.positions[sig.Symbol]

	switch sig.Action {
	case "buy":
		if !exists || pos.Quantity == 0 {
			// Open new position
			capital := e.cash * e.config.PositionSize
			price := e.applySlippage(sig.Price, "buy")
			quantity := capital / price
			commission := quantity * price * e.config.CommissionRate

			if quantity*price+commission > e.cash {
				return nil // Insufficient funds
			}

			e.cash -= quantity*price + commission
			e.positions[sig.Symbol] = &Position{
				Symbol: sig.Symbol,
				Quantity: quantity,
				EntryPrice: price,
				CurrentPrice: price,
			}

			e.trades = append(e.trades, Trade{
				ID: fmt.Sprintf("trade-%d", len(e.trades)+1),
				Symbol: sig.Symbol,
				Side: "buy",
				Quantity: quantity,
				Price: price,
				Commission: commission,
				Slippage: math.Abs(price - sig.Price) * quantity,
				Timestamp: ts,
			})
		}

	case "sell":
		if exists && pos.Quantity > 0 {
			// Close position
			price := e.applySlippage(sig.Price, "sell")
			commission := pos.Quantity * price * e.config.CommissionRate
			pnl := (price-pos.EntryPrice)*pos.Quantity - commission

			e.cash += pos.Quantity*price - commission

			e.trades = append(e.trades, Trade{
				ID: fmt.Sprintf("trade-%d", len(e.trades)+1),
				Symbol: sig.Symbol,
				Side: "sell",
				Quantity: pos.Quantity,
				Price: price,
				Commission: commission,
				Slippage: math.Abs(price - sig.Price) * pos.Quantity,
				Timestamp: ts,
				PNL: pnl,
			})

			pos.Quantity = 0
		}
	}

	return nil
}

func (e *Engine) updatePositions(evt eventbus.Event) error {
	// Update position prices and calculate equity
	// Implementation depends on market data type
	return nil
}

func (e *Engine) applySlippage(price float64, side string) float64 {
	if side == "buy" {
		return price * (1 + e.config.SlippageRate)
	}
	return price * (1 - e.config.SlippageRate)
}

// GenerateReport generates the backtest report
func (e *Engine) GenerateReport() *Report {
	finalEquity := e.calculateEquity()
	totalReturn := finalEquity - e.config.InitialCapital
	totalReturnPct := totalReturn / e.config.InitialCapital * 100

	winningTrades := 0
	losingTrades := 0
	for _, t := range e.trades {
		if t.Side == "sell" {
			if t.PNL > 0 {
				winningTrades++
			} else {
				losingTrades++
			}
		}
	}

	winRate := 0.0
	if len(e.trades) > 0 {
		winRate = float64(winningTrades) / float64(winningTrades+losingTrades) * 100
	}

	return &Report{
		InitialCapital: e.config.InitialCapital,
		FinalEquity: finalEquity,
		TotalReturn: totalReturn,
		TotalReturnPct: totalReturnPct,
		MaxDrawdown: e.maxDrawdown,
		MaxDrawdownPct: e.maxDrawdown / e.peakEquity * 100,
		SharpeRatio: e.calculateSharpe(),
		WinRate: winRate,
		TotalTrades: len(e.trades),
		WinningTrades: winningTrades,
		LosingTrades: losingTrades,
		EquityCurve: e.equityCurve,
		Trades: e.trades,
	}
}

func (e *Engine) calculateEquity() float64 {
	equity := e.cash
	for _, pos := range e.positions {
		equity += pos.Quantity * pos.CurrentPrice
	}
	return equity
}

func (e *Engine) calculateSharpe() float64 {
	if len(e.returns) < 2 {
		return 0
	}

	var sum, sumSq float64
	for _, r := range e.returns {
		sum += r
	}
	mean := sum / float64(len(e.returns))

	for _, r := range e.returns {
		sumSq += math.Pow(r-mean, 2)
	}
	std := math.Sqrt(sumSq / float64(len(e.returns)-1))

	if std == 0 {
		return 0
	}

	// Annualized Sharpe (assuming daily returns)
	return mean / std * math.Sqrt(252)
}
