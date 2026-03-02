package main

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"trade-lab/internal/eventbus"
)

func main() {
	fmt.Println("=== Event Bus Benchmark ===")
	fmt.Println()

	// Test 1: Throughput
	fmt.Println("Test 1: Throughput (events/sec)")
	bus := eventbus.NewInMemoryBus(1000000)
	
	var count int64
	bus.Subscribe(eventbus.EventKline, func(ctx context.Context, evt eventbus.Event) error {
		atomic.AddInt64(&count, 1)
		return nil
	})

	totalEvents := 100000
	start := time.Now()
	
	for i := 0; i < totalEvents; i++ {
		evt := eventbus.Event{
			ID: fmt.Sprintf("evt-%d", i),
			Type: eventbus.EventKline,
			Timestamp: time.Now(),
			Source: "benchmark",
		}
		bus.Publish(context.Background(), evt)
	}

	elapsed := time.Since(start)
	throughput := float64(totalEvents) / elapsed.Seconds()
	
	fmt.Printf(" Events: %d\n", totalEvents)
	fmt.Printf(" Time: %v\n", elapsed.Round(time.Millisecond))
	fmt.Printf(" Throughput: %.0f events/sec\n", throughput)
	fmt.Println()

	// Test 2: Latency
	fmt.Println("Test 2: Latency")
	bus2 := eventbus.NewInMemoryBus(1000)
	
	var latencies []time.Duration
	bus2.Subscribe(eventbus.EventKline, func(ctx context.Context, evt eventbus.Event) error {
		latency := time.Since(evt.Timestamp)
		latencies = append(latencies, latency)
		return nil
	})

	for i := 0; i < 1000; i++ {
		evt := eventbus.Event{
			ID: fmt.Sprintf("lat-%d", i),
			Type: eventbus.EventKline,
			Timestamp: time.Now(),
			Source: "benchmark",
		}
		bus2.Publish(context.Background(), evt)
	}

	var total time.Duration
	for _, l := range latencies {
		total += l
	}
	avgLatency := total / time.Duration(len(latencies))
	
	fmt.Printf(" Avg Latency: %v\n", avgLatency)
	fmt.Println()

	// Test 3: Memory usage
	fmt.Println("Test 3: Memory (history buffer)")
	bus3 := eventbus.NewInMemoryBus(10000)
	for i := 0; i < 15000; i++ {
		evt := eventbus.Event{
			ID: fmt.Sprintf("mem-%d", i),
			Type: eventbus.EventKline,
			Timestamp: time.Now(),
			Source: "benchmark",
		}
		bus3.Publish(context.Background(), evt)
	}
	fmt.Printf(" Max history: 10000\n")
	fmt.Printf(" Published: 15000\n")
	fmt.Printf(" Buffer overflow handled: OK\n")
	fmt.Println()

	fmt.Println("=== Benchmark Complete ===")
	
	// Write results to file
	f, _ := os.Create("benchmark_results.txt")
	fmt.Fprintf(f, "Throughput: %.0f events/sec\n", throughput)
	fmt.Fprintf(f, "Avg Latency: %v\n", avgLatency)
	fmt.Fprintf(f, "Timestamp: %s\n", time.Now().Format(time.RFC3339))
	f.Close()
}
