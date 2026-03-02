package tests

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"trade-lab/internal/eventbus"
)

func TestEventBusPublishSubscribe(t *testing.T) {
	bus := eventbus.NewInMemoryBus(1000)
	
	var received int64
	bus.Subscribe(eventbus.EventKline, func(ctx context.Context, evt eventbus.Event) error {
		atomic.AddInt64(&received, 1)
		return nil
	})
	
	evt := eventbus.Event{
		ID: "test-1",
		Type: eventbus.EventKline,
		Timestamp: time.Now(),
		Source: "test",
		Payload: map[string]interface{}{"close": 100.0},
	}
	
	if err := bus.Publish(context.Background(), evt); err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	
	if atomic.LoadInt64(&received) != 1 {
		t.Errorf("expected 1 received, got %d", received)
	}
}

func TestEventBusReplay(t *testing.T) {
	bus := eventbus.NewInMemoryBus(1000)
	
	// Publish some events
	for i := 0; i < 5; i++ {
		evt := eventbus.Event{
			ID: string(rune('0'+i)),
			Type: eventbus.EventKline,
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			Source: "test",
		}
		bus.Publish(context.Background(), evt)
	}
	
	var replayCount int64
	bus.Subscribe(eventbus.EventKline, func(ctx context.Context, evt eventbus.Event) error {
		atomic.AddInt64(&replayCount, 1)
		return nil
	})
	
	// Replay all events
	from := time.Now().Add(-time.Second)
	to := time.Now().Add(10 * time.Second)
	if err := bus.Replay(context.Background(), from, to); err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	
	if atomic.LoadInt64(&replayCount) != 5 {
		t.Errorf("expected 5 replayed, got %d", replayCount)
	}
}

func BenchmarkEventBusPublish(b *testing.B) {
	bus := eventbus.NewInMemoryBus(100000)
	bus.Subscribe(eventbus.EventKline, func(ctx context.Context, evt eventbus.Event) error {
		return nil
	})
	
	evt := eventbus.Event{
		Type: eventbus.EventKline,
		Timestamp: time.Now(),
		Source: "bench",
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Publish(context.Background(), evt)
	}
}
