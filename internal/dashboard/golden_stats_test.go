package dashboard

import (
	"database/sql"
	"encoding/json"
	"os"
	"testing"
)

// TestGoldenStats pins the exact HTML output of the stats pipeline for a
// synthetic trade set. Run before AND after the refactor and diff the JSON.
func TestGoldenStats(t *testing.T) {
	mk := func(ts int64, price, size, result float64, strat, src, outcome, slug string, paper bool) TradeView {
		tv := TradeView{
			Ts: ts, MarketID: "0xm" + outcome, TokenID: "tok" + slug, Side: "BUY",
			Outcome: outcome, Price: price, SizeUSDC: size, Paper: paper,
			Result:   sql.NullFloat64{Float64: result, Valid: true},
			Question: "Will " + slug + " happen by 2027?", Slug: slug,
			SourceJSON: src, Strategy: strat,
			DateStr: "2026-09-20 12:00",
		}
		tv.HasResult = true
		shares := size / price
		tv.Recovered = shares * result
		tv.Gain = tv.Recovered - size
		if result >= 0.99 {
			tv.ResultStr = "WIN"
		} else {
			tv.ResultStr = "LOSS"
		}
		return tv
	}
	src2 := `[{"address":"0xaaa","name":"Alice","outcome":"Yes","size":10,"price":0.4},{"address":"0xbbb","name":"","outcome":"Yes","size":20,"price":0.45}]`
	trades := []TradeView{
		mk(1758000000, 0.15, 50, 1.0, "deep_value", "", "Yes", "alpha-election", true),
		mk(1758100000, 0.85, 100, 0.0, "momentum", src2, "No", "beta-award", false),
		mk(1758200000, 0.50, 30, 0.0, "", "[]", "Over", "gamma-game", true),
		mk(1758300000, 0.95, 200, 1.0, "copy_trading", src2, "buy", "delta-sports", false),
		mk(1758400000, 0.05, 10, 1.0, "arbitrage", `bad json`, "true", "epsilon", true),
		mk(1758500000, 0.45, 75, 0.0, "underhunter", `[{"address":"0xaaa","name":"","outcome":"No","size":5,"price":0.4}]`, "No", "zeta", false),
		mk(1758600000, 0.25, 60, 1.0, "deep_value", "", "", "eta", true),
		mk(1758700000, 0.65, 40, 0.0, "copy_trading", `[{"address":"0xbbb","name":"Bob","outcome":"Yes","size":9,"price":0.6}]`, "Yes", "theta", false),
		mk(1758800000, 0.35, 45, 1.0, "whale", "", "TRUE", "iota", true),
		mk(1758900000, 0.75, 80, 0.0, "kelly", "", "over", "kappa", false),
	}
	data := &DashboardData{RecentTrades: trades}
	computeAndRenderStats(data)

	// Also exercise the balance-row renderer on the same trades.
	rows := renderBalanceRows(trades)

	out := map[string]string{
		"StatsOverview":     string(data.StatsOverview),
		"TraderStatsRows":   string(data.TraderStatsRows),
		"TraderDetailRows":  string(data.TraderDetailRows),
		"PriceStatsRows":    string(data.PriceStatsRows),
		"DowStatsRows":      string(data.DowStatsRows),
		"HourStatsRows":     string(data.HourStatsRows),
		"MarketStatsRows":   string(data.MarketStatsRows),
		"StrategyStatsRows": string(data.StrategyStatsRows),
		"PaperLiveStatsRow": string(data.PaperLiveStatsRow),
		"PnLTimelineLabels": string(data.PnLTimelineLabels),
		"PnLTimelineData":   string(data.PnLTimelineData),
		"TraderChartLabels": string(data.TraderChartLabels),
		"TraderChartPnL":    string(data.TraderChartPnL),
		"TraderChartWins":   string(data.TraderChartWins),
		"PriceChartLabels":  string(data.PriceChartLabels),
		"PriceChartPnL":     string(data.PriceChartPnL),
		"DowChartLabels":    string(data.DowChartLabels),
		"DowChartPnL":       string(data.DowChartPnL),
		"HourChartLabels":   string(data.HourChartLabels),
		"HourChartPnL":      string(data.HourChartPnL),
		"WinLossData":       string(data.WinLossData),
		"Perf":              string(rune(int(data.PerfAvgWin*100+data.PerfAvgLoss*7+data.PerfProfitFactor*13+float64(data.PerfStreakWins)*3+float64(data.PerfStreakLosses)))) + "|",
		"BalanceRows":       string(rows),
	}
	// numeric perf fields serialized properly
	perf := []float64{data.PerfAvgWin, data.PerfAvgLoss, data.PerfProfitFactor, float64(data.PerfStreakWins), float64(data.PerfStreakLosses)}
	pj, _ := json.Marshal(perf)
	out["Perf"] = string(pj)

	path := os.Getenv("GOLDEN_OUT")
	if path == "" {
		// Normal suite run: just exercise the pipeline (panic == failure).
		return
	}
	b, _ := json.MarshalIndent(out, "", " ")
	os.WriteFile(path, b, 0o644)
}
