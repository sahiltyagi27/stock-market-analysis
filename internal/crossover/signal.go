// Package crossover implements a scanner that identifies stocks where the
// 7-period EMA has recently crossed above the 21-period EMA.  Unlike the
// support-zone scanner, this is a pure momentum signal — no long-term trend
// filter (EMA50/EMA200) is required.
package crossover

import "time"

// Signal is the output for one symbol when EMA7 has freshly crossed above EMA21.
type Signal struct {
	Symbol string
	Price  float64

	// CrossoverDate is the timestamp of the candle where EMA7 crossed EMA21.
	CrossoverDate time.Time
	// CrossoverAge is the number of candles elapsed since the crossover.
	// 0 = happened on the most recent candle, 1 = one candle ago, 2 = two ago.
	CrossoverAge int

	// Current EMA values (most recent candle).
	EMA7  float64
	EMA21 float64

	// Trade setup.
	Entry float64 // current close price
	// SL is the Low of the candle immediately before the crossover candle.
	SL float64
	// Target is the midpoint of the nearest resistance zone above entry.
	// Zero when no resistance zone was found (signal still shown, R/R = 0).
	Target     float64
	RiskReward float64

	Volume  VolumeStats
	Score   float64
	Reasons []string
}

// VolumeStats describes trading activity on and near the crossover candle.
type VolumeStats struct {
	AvgVolume   float64 // rolling average over the window before the crossover
	CrossVolume float64 // volume on the crossover candle itself
	Ratio       float64 // CrossVolume / AvgVolume; 0 when AvgVolume is 0
}
