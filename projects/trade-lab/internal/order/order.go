package order

import (
	"time"
)

// OrderSide represents buy or sell
type OrderSide string

const (
	SideBuy OrderSide = "buy"
	SideSell OrderSide = "sell"
)

// OrderType represents order type
type OrderType string

const (
	TypeMarket OrderType = "market"
	TypeLimit OrderType = "limit"
)

// OrderStatus represents order status
type OrderStatus string

const (
	StatusNew OrderStatus = "new"
	StatusPending OrderStatus = "pending"
	StatusFilled OrderStatus = "filled"
	StatusPartial OrderStatus = "partial"
	StatusRejected OrderStatus = "rejected"
	StatusCancelled OrderStatus = "cancelled"
)

// Order represents a trading order
type Order struct {
	ID string `json:"id"`
	Symbol string `json:"symbol"`
	Side OrderSide `json:"side"`
	Type OrderType `json:"type"`
	Status OrderStatus `json:"status"`
	Quantity float64 `json:"quantity"`
	Price float64 `json:"price"` // limit price
	FilledQuantity float64 `json:"filled_quantity"`
	FilledPrice float64 `json:"filled_price"` // avg fill price
	Timestamp time.Time `json:"timestamp"`
	Reason string `json:"reason"` // rejection reason
}

// Fill represents an order fill
type Fill struct {
	OrderID string `json:"order_id"`
	Symbol string `json:"symbol"`
	Side OrderSide `json:"side"`
	Quantity float64 `json:"quantity"`
	Price float64 `json:"price"`
	Commission float64 `json:"commission"`
	Timestamp time.Time `json:"timestamp"`
}
