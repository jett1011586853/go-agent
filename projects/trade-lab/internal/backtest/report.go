package backtest

import (
	"fmt"
)

// ToMarkdown generates a markdown representation of the report
func (r *Report) ToMarkdown() string {
	md := "# Backtest Report\n\n"
	md += fmt.Sprintf("## Summary\n\n")
	md += fmt.Sprintf("| Metric | Value |\n")
	md += fmt.Sprintf("|--------|-------|\n")
	md += fmt.Sprintf("| Initial Capital | %.2f |\n", r.InitialCapital)
	md += fmt.Sprintf("| Final Equity | %.2f |\n", r.FinalEquity)
	md += fmt.Sprintf("| Total Return | %.2f (%.2f%%) |\n", r.TotalReturn, r.TotalReturnPct)
	md += fmt.Sprintf("| Max Drawdown | %.2f (%.2f%%) |\n", r.MaxDrawdown, r.MaxDrawdownPct)
	md += fmt.Sprintf("| Sharpe Ratio | %.2f |\n", r.SharpeRatio)
	md += fmt.Sprintf("| Win Rate | %.2f%% |\n", r.WinRate)
	md += fmt.Sprintf("| Total Trades | %d |\n", r.TotalTrades)
	md += fmt.Sprintf("| Winning Trades | %d |\n", r.WinningTrades)
	md += fmt.Sprintf("| Losing Trades | %d |\n", r.LosingTrades)
	md += "\n"

	if len(r.Trades) > 0 {
		md += "## Trades\n\n"
		md += "| ID | Symbol | Side | Quantity | Price | PNL |\n"
		md += "|----|--------|------|----------|-------|-----|\n"
		for _, t := range r.Trades {
			md += fmt.Sprintf("| %s | %s | %s | %.2f | %.2f | %.2f |\n",
				t.ID, t.Symbol, t.Side, t.Quantity, t.Price, t.PNL)
		}
	}

	return md
}
