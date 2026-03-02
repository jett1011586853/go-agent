package order

import (
	"context"
	"sync"
	"time"

	"trade-lab/internal/eventbus"
)

// Matcher is the order matching engine
type Matcher struct {
	mu sync.RWMutex
	orders map[string]*Order
	orderBook map[string][]*Order // symbol -> orders
	bus eventbus.Bus
}

// NewMatcher creates a new order matcher
func NewMatcher(bus eventbus.Bus) *Matcher {
	return &Matcher{
		orders: make(map[string]*Order),
		orderBook: make(map[string][]*Order),
		bus: bus,
	}
}

// Submit submits an order for matching
func (m *Matcher) Submit(ctx context.Context, order *Order) (*Fill, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	order.Status = StatusPending
	order.Timestamp = time.Now()
	m.orders[order.ID] = order

	var fill *Fill

	switch order.Type {
	case TypeMarket:
		fill = m.matchMarketOrder(order)
	case TypeLimit:
		fill = m.matchLimitOrder(order)
	}

	if fill != nil {
		// Publish fill event
		evt := eventbus.Event{
			ID: "fill-" + order.ID,
			Type: eventbus.EventOrderFilled,
			Timestamp: time.Now(),
			Source: "matcher",
			Payload: fill,
		}
		m.bus.Publish(ctx, evt)
	}

	return fill, nil
}

func (m *Matcher) matchMarketOrder(order *Order) *Fill {
	// Simulate market order execution at current price
	fill := &Fill{
		OrderID: order.ID,
		Symbol: order.Symbol,
		Side: order.Side,
		Quantity: order.Quantity,
		Price: order.Price, // Use order price as execution price
		Commission: order.Quantity * order.Price * 0.001,
		Timestamp: time.Now(),
	}

	order.Status = StatusFilled
	order.FilledQuantity = order.Quantity
	order.FilledPrice = order.Price

	return fill
}

func (m *Matcher) matchLimitOrder(order *Order) *Fill {
	// Add to order book
	m.orderBook[order.Symbol] = append(m.orderBook[order.Symbol], order)

	// Check if order can be matched immediately
	oppositeSide := SideSell
	if order.Side == SideSell {
		oppositeSide = SideBuy
	}

	for _, bookOrder := range m.orderBook[order.Symbol] {
		if bookOrder.Side == oppositeSide && bookOrder.Status == StatusPending {
			// Match found
			if (order.Side == SideBuy && order.Price >= bookOrder.Price) ||
				(order.Side == SideSell && order.Price <= bookOrder.Price) {

				fillQty := min(order.Quantity, bookOrder.Quantity)
				fillPrice := (order.Price + bookOrder.Price) / 2

				fill := &Fill{
					OrderID: order.ID,
					Symbol: order.Symbol,
					Side: order.Side,
					Quantity: fillQty,
					Price: fillPrice,
					Commission: fillQty * fillPrice * 0.001,
					Timestamp: time.Now(),
				}

				order.FilledQuantity = fillQty
				order.FilledPrice = fillPrice
				bookOrder.FilledQuantity = fillQty
				bookOrder.FilledPrice = fillPrice

				if order.FilledQuantity >= order.Quantity {
					order.Status = StatusFilled
				}
				if bookOrder.FilledQuantity >= bookOrder.Quantity {
					bookOrder.Status = StatusFilled
				}

				return fill
			}
		}
	}

	return nil
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
