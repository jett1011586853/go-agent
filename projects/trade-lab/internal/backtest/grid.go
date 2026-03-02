package backtest

import (
	"context"
	"sync"

	"trade-lab/internal/eventbus"
	"trade-lab/internal/strategy"
)

// ParamRange defines a parameter range for grid search
type ParamRange struct {
	Name string
	Values []interface{}
}

// GridSearchResult represents a single grid search result
type GridSearchResult struct {
	Params map[string]interface{}
	Report *Report
}

// GridSearch performs parameter grid search
type GridSearch struct {
	config Config
	busFactory func() eventbus.Bus
	strategyFactory func(params map[string]interface{}) strategy.Strategy
	paramRanges []ParamRange
	results []GridSearchResult
}

// NewGridSearch creates a new grid search
func NewGridSearch(config Config, busFactory func() eventbus.Bus, strategyFactory func(params map[string]interface{}) strategy.Strategy) *GridSearch {
	return &GridSearch{
		config: config,
		busFactory: busFactory,
		strategyFactory: strategyFactory,
		paramRanges: make([]ParamRange, 0),
		results: make([]GridSearchResult, 0),
	}
}

// AddParamRange adds a parameter range
func (g *GridSearch) AddParamRange(name string, values []interface{}) {
	g.paramRanges = append(g.paramRanges, ParamRange{Name: name, Values: values})
}

// Run executes the grid search
func (g *GridSearch) Run(ctx context.Context) ([]GridSearchResult, error) {
	paramCombinations := g.generateCombinations()

	var mu sync.Mutex
	var wg sync.WaitGroup
	errChan := make(chan error, len(paramCombinations))

	for _, params := range paramCombinations {
		wg.Add(1)
		go func(p map[string]interface{}) {
			defer wg.Done()

			strat := g.strategyFactory(p)
			bus := g.busFactory()
			engine := NewEngine(g.config, strat, bus)

			if _, err := engine.Run(ctx); err != nil {
				errChan <- err
				return
			}

			report := engine.GenerateReport()

			mu.Lock()
			g.results = append(g.results, GridSearchResult{
				Params: p,
				Report: report,
			})
			mu.Unlock()
		}(params)
	}

	wg.Wait()
	close(errChan)

	if err := <-errChan; err != nil {
		return nil, err
	}

	return g.results, nil
}

func (g *GridSearch) generateCombinations() []map[string]interface{} {
	if len(g.paramRanges) == 0 {
		return []map[string]interface{}{{}}
	}

	var combinations []map[string]interface{}
	g.combine(0, make(map[string]interface{}), &combinations)
	return combinations
}

func (g *GridSearch) combine(idx int, current map[string]interface{}, combinations *[]map[string]interface{}) {
	if idx >= len(g.paramRanges) {
		params := make(map[string]interface{})
		for k, v := range current {
			params[k] = v
		}
		*combinations = append(*combinations, params)
		return
	}

	paramRange := g.paramRanges[idx]
	for _, v := range paramRange.Values {
		current[paramRange.Name] = v
		g.combine(idx+1, current, combinations)
	}
}

// BestResult returns the best result by Sharpe ratio
func (g *GridSearch) BestResult() *GridSearchResult {
	if len(g.results) == 0 {
		return nil
	}

	best := g.results[0]
	for _, r := range g.results {
		if r.Report.SharpeRatio > best.Report.SharpeRatio {
			best = r
		}
	}
	return &best
}
