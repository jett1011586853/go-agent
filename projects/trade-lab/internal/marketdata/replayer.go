package marketdata

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"time"

	"trade-lab/internal/eventbus"
)

// Kline represents a K-line (candlestick) data point
type Kline struct {
	Symbol string `json:"symbol"`
	OpenTime time.Time `json:"open_time"`
	Open float64 `json:"open"`
	High float64 `json:"high"`
	Low float64 `json:"low"`
	Close float64 `json:"close"`
	Volume float64 `json:"volume"`
	CloseTime time.Time `json:"close_time"`
}

// Tick represents a tick data point
type Tick struct {
	Symbol string `json:"symbol"`
	Timestamp time.Time `json:"timestamp"`
	Price float64 `json:"price"`
	Volume float64 `json:"volume"`
	Side string `json:"side"` // buy/sell
}

// Replayer is the market data replayer interface
type Replayer interface {
	Load(ctx context.Context) error
	Replay(ctx context.Context, bus eventbus.Bus) error
	Seek(timestamp time.Time) error
}

// CSVReplayer replays market data from CSV files
type CSVReplayer struct {
	filePath string
	dataType string // "kline" or "tick"
	data []interface{}
	currentIndex int
}

// NewCSVReplayer creates a new CSV replayer
func NewCSVReplayer(filePath, dataType string) *CSVReplayer {
	return &CSVReplayer{
		filePath: filePath,
		dataType: dataType,
		currentIndex: 0,
	}
}

// Load loads market data from CSV file
func (r *CSVReplayer) Load(ctx context.Context) error {
	file, err := os.Open(r.filePath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read csv: %w", err)
	}

	if len(records) == 0 {
		return fmt.Errorf("empty csv file")
	}

	// Skip header
	records = records[1:]

	r.data = make([]interface{}, 0, len(records))

	for _, record := range records {
		switch r.dataType {
		case "kline":
			kline, err := parseKline(record)
			if err != nil {
				return fmt.Errorf("parse kline: %w", err)
			}
			r.data = append(r.data, kline)
		case "tick":
			tick, err := parseTick(record)
			if err != nil {
				return fmt.Errorf("parse tick: %w", err)
			}
			r.data = append(r.data, tick)
		default:
			return fmt.Errorf("unknown data type: %s", r.dataType)
		}
	}

	return nil
}

// Replay replays market data to event bus
func (r *CSVReplayer) Replay(ctx context.Context, bus eventbus.Bus) error {
	for i := r.currentIndex; i < len(r.data); i++ {
		var evt eventbus.Event
		switch v := r.data[i].(type) {
		case Kline:
			evt = eventbus.Event{
				ID: fmt.Sprintf("kline-%d", i),
				Type: eventbus.EventKline,
				Timestamp: v.OpenTime,
				Source: "csv_replayer",
				Payload: v,
			}
		case Tick:
			evt = eventbus.Event{
				ID: fmt.Sprintf("tick-%d", i),
				Type: eventbus.EventTick,
				Timestamp: v.Timestamp,
				Source: "csv_replayer",
				Payload: v,
			}
		}

		if err := bus.Publish(ctx, evt); err != nil {
			return fmt.Errorf("publish event: %w", err)
		}
		r.currentIndex = i + 1
	}
	return nil
}

// Seek seeks to a specific timestamp
func (r *CSVReplayer) Seek(timestamp time.Time) error {
	for i, d := range r.data {
		var t time.Time
		switch v := d.(type) {
		case Kline:
			t = v.OpenTime
		case Tick:
			t = v.Timestamp
		}
		if t.Equal(timestamp) || t.After(timestamp) {
			r.currentIndex = i
			return nil
		}
	}
	return fmt.Errorf("timestamp not found")
}

func parseKline(record []string) (Kline, error) {
	if len(record) < 8 {
		return Kline{}, fmt.Errorf("invalid kline record")
	}

	openTime, _ := time.Parse(time.RFC3339, record[1])
	open, _ := strconv.ParseFloat(record[2], 64)
	high, _ := strconv.ParseFloat(record[3], 64)
	low, _ := strconv.ParseFloat(record[4], 64)
	close, _ := strconv.ParseFloat(record[5], 64)
	volume, _ := strconv.ParseFloat(record[6], 64)
	closeTime, _ := time.Parse(time.RFC3339, record[7])

	return Kline{
		Symbol: record[0],
		OpenTime: openTime,
		Open: open,
		High: high,
		Low: low,
		Close: close,
		Volume: volume,
		CloseTime: closeTime,
	}, nil
}

func parseTick(record []string) (Tick, error) {
	if len(record) < 5 {
		return Tick{}, fmt.Errorf("invalid tick record")
	}

	timestamp, _ := time.Parse(time.RFC3339Nano, record[1])
	price, _ := strconv.ParseFloat(record[2], 64)
	volume, _ := strconv.ParseFloat(record[3], 64)

	return Tick{
		Symbol: record[0],
		Timestamp: timestamp,
		Price: price,
		Volume: volume,
		Side: record[4],
	}, nil
}

// Ensure CSVReplayer implements Replayer
var _ Replayer = (*CSVReplayer)(nil)
