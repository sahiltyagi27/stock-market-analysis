package analysis

import (
	"math"

	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// ATR computes the Average True Range over the last `period` candles using
// Wilder's smoothing (also called RMA / SMMA), which is the standard used by
// most charting platforms (TradingView, Kite Charts, etc.).
//
// The formula:
//
//	True Range_i = max(High_i − Low_i, |High_i − Close_{i-1}|, |Close_{i-1} − Low_i|)
//	Seed         = simple average of the first `period` True Ranges
//	ATR_i        = ((period−1) × ATR_{i-1} + TR_i) / period
//
// Returns 0 when there are fewer than period+1 candles (not enough data).
// Passing period ≤ 0 defaults to 14.
func ATR(candles []models.Candle, period int) float64 {
	if period <= 0 {
		period = 14
	}
	// Need at least period+1 candles: one prev-close for each of the period
	// True Ranges in the seed window, plus at least one prior candle.
	if len(candles) < period+1 {
		return 0
	}

	// Compute True Ranges for every candle after the first.
	trs := make([]float64, len(candles)-1)
	for i := 1; i < len(candles); i++ {
		c := candles[i]
		pc := candles[i-1].Close
		tr := math.Max(c.High-c.Low,
			math.Max(math.Abs(c.High-pc), math.Abs(pc-c.Low)))
		trs[i-1] = tr
	}

	// Seed: simple average of the first `period` True Ranges.
	atr := 0.0
	for i := 0; i < period; i++ {
		atr += trs[i]
	}
	atr /= float64(period)

	// Wilder's smoothing over the remaining True Ranges.
	for i := period; i < len(trs); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}

	return round2(atr)
}
