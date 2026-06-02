package scanner

import "fmt"

// buildReasons returns a deterministic, human-readable slice of strings that
// explain why a signal was selected. It is called after all filters pass, so
// every field of sig is guaranteed to be populated.
//
// Order is fixed:
//  1. Trend confirmation (always present)
//  2. Risk/Reward (always present — signals below MinRR are filtered before this)
//  3. Support zone strength (always present)
//  4. Trade quality grade (always present)
//  5. Relative strength (only when benchmark data was supplied)
//  6. Sector strength (only when sector map/data was supplied)
//  7. Volume confirmation (only when lastVolume > avgVolume)
func buildReasons(
	sig *StockSignal,
	avgVolume float64,
	lastVolume float64,
	minRR float64,
) []string {
	reasons := make([]string, 0, 5)

	// 1. Trend
	reasons = append(reasons, fmt.Sprintf(
		"Price above EMA50 (%.2f) and EMA200 (%.2f)",
		sig.EMA.EMA50, sig.EMA.EMA200,
	))

	// 2. Risk/Reward
	reasons = append(reasons, fmt.Sprintf(
		"Risk/Reward %.2f exceeds minimum %.2f",
		sig.Trade.RiskReward, minRR,
	))

	// 3. Support strength
	times := "times"
	if sig.Support.Touches == 1 {
		times = "time"
	}
	reasons = append(reasons, fmt.Sprintf(
		"Support zone touched %d %s",
		sig.Support.Touches, times,
	))

	// 4. Trade quality
	reasons = append(reasons, fmt.Sprintf(
		"Trade quality: %s",
		sig.Trade.Quality,
	))

	// 5. Relative strength
	if sig.RelativeStrength.Lookback > 0 {
		rs := sig.RelativeStrength
		reasons = append(reasons, fmt.Sprintf(
			"Relative strength %.2f%% vs %s over %d candles",
			rs.OutperformancePct,
			rs.BenchmarkSymbol,
			rs.Lookback,
		))
	}

	// 6. Sector strength
	if sig.SectorStrength.Lookback > 0 {
		ss := sig.SectorStrength
		reasons = append(reasons, fmt.Sprintf(
			"Sector strength %.2f%%: %s vs %s over %d candles",
			ss.OutperformancePct,
			ss.SectorIndexSymbol,
			ss.BenchmarkSymbol,
			ss.Lookback,
		))
	}

	// 7. Volume — only when above rolling average
	if avgVolume > 0 && lastVolume > avgVolume {
		ratio := lastVolume / avgVolume
		reasons = append(reasons, fmt.Sprintf(
			"Volume %.1fx above rolling average",
			ratio,
		))
	}

	return reasons
}
