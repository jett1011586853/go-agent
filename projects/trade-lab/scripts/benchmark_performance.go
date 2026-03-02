package main

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"trade-lab/internal/eventbus"
)

func main() {
	fmt.Println("=== Trade-Lab Performance Baseline ===")
	fmt.Printf("Go Version: %s\n", runtime.Version())
	fmt.Printf("CPU Cores: %d\n", runtime.NumCPU())
	fmt.Println()

	// 1. Event Bus Throughput
	benchmarkEventBus()

	// 2. Long-time Replay
	benchmarkLongReplay()

	// 3. Memory Usage
	benchmarkMemoryUsage()
}

func benchmarkEventBus() {
	fmt.Println("--- Event Bus Throughput ---")

	bus := eventbus.NewInMemoryBus(10000)
	count := 1000000
	ctx := context.Background()

	start := time.Now()

	for i := 0; i < count; i++ {
		evt := eventbus.Event{
			ID: fmt.Sprintf("evt-%d", i),
			Type: eventbus.EventMarketData,
			Timestamp: time.Now(),
			Source: "benchmark",
			Payload: map[string]interface{}{"price": 50000.0 + float64(i%1000)},
		}
		bus.Publish(ctx, evt)
	}

	elapsed := time.Since(start)
	throughput := float64(count) / elapsed.Seconds()

	fmt.Printf("Events: %d\n", count)
	fmt.Printf("Duration: %v\n", elapsed)
	fmt.Printf("Throughput: %.0f events/sec\n", throughput)
	fmt.Println()
}

func benchmarkLongReplay() {
	fmt.Println("--- Long-time Replay ---")

	bus := eventbus.NewInMemoryBus(50000)
	events := 500000
	ctx := context.Background()

	// Populate history
	for i := 0; i < events; i++ {
		evt := eventbus.Event{
			ID: fmt.Sprintf("replay-%d", i),
			Type: eventbus.EventMarketData,
			Timestamp: time.Now().Add(time.Duration(i) * time.Millisecond),
			Source: "benchmark",
			Payload: map[string]interface{}{"price": 50000.0 + float64(i%1000)},
		}
		bus.Publish(ctx, evt)
	}

	start := time.Now()

	// Replay
	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	bus.Replay(ctx, from, to)

	elapsed := time.Since(start)
	throughput := float64(events) / elapsed.Seconds()

	fmt.Printf("Events Replayed: %d\n", events)
	fmt.Printf("Duration: %v\n", elapsed)
	fmt.Printf("Throughput: %.0f events/sec\n", throughput)
	fmt.Println()
}

func benchmarkMemoryUsage() {
	fmt.Println("--- Memory Usage ---")

	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	// Create many objects
	bus := eventbus.NewInMemoryBus(10000)
	ctx := context.Background()

	for i := 0; i < 100000; i++ {
		evt := eventbus.Event{
			ID: fmt.Sprintf("mem-%d", i),
			Type: eventbus.EventMarketData,
			Timestamp: time.Now(),
			Source: "benchmark",
			Payload: map[string]interface{}{"price": float64(i)},
		}
		bus.Publish(ctx, evt)
	}

	runtime.GC()
	runtime.ReadMemStats(&m2)

	allocMB := float64(m2.Alloc-m1.Alloc) / 1024 / 1024
	heapMB := float64(m2.HeapAlloc-m1.HeapAlloc) / 1024 / 1024

	fmt.Printf("Allocated Memory: %.2f MB\n", allocMB)
	fmt.Printf("Heap Memory: %.2f MB\n", heapMB)
	fmt.Printf("GC Cycles: %d\n", m2.NumGC-m1.NumGC)
	fmt.Println()
}
