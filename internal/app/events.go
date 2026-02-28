package app

import "context"

type Event struct {
	Type string
	Text string
}

type EventHandler func(Event)

type eventHandlerContextKey struct{}

func WithEventHandler(ctx context.Context, h EventHandler) context.Context {
	if h == nil {
		return ctx
	}
	return context.WithValue(ctx, eventHandlerContextKey{}, h)
}

func emitEvent(ctx context.Context, ev Event) {
	if ctx == nil {
		return
	}
	h, _ := ctx.Value(eventHandlerContextKey{}).(EventHandler)
	if h == nil {
		return
	}
	h(ev)
}
