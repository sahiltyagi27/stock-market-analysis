package backtest

import "math"

// Summary aggregates a slice of TradeResult into headline performance stats.
type Summary struct {
	Total    int
	Wins     int
	Losses   int
	Timeouts int
	// TrailStops is the count of trades exited by the ATR trailing stop.
	// These trades were at some point profitable (highestHigh > entry) before
	// the trailing stop caught the reversal.  ActualRR may be positive
	// (profit protected), zero (breakeven), or slightly negative.
	TrailStops int

	// WinRate is Wins / (Wins + Losses) as a percentage (0–100).
	// Timeouts and trail stops are excluded so the metric reflects clean
	// target/stop outcomes only.
	WinRate float64

	// AvgWinRR is the mean actual R:R of all winning trades.
	AvgWinRR float64
	// AvgLossRR is the mean actual R:R of all losing trades (always ≤ 0).
	AvgLossRR float64
	// AvgTrailStopRR is the mean actual R:R of all trail-stop exits.
	// Positive means the trailing stop captured profit; negative (rare) means
	// it moved above the original SL but the reversal was fast enough to exit
	// below entry.
	AvgTrailStopRR float64

	// ProfitFactor is sumPositiveR / |sumNegativeR| across ALL trades
	// (wins, losses, timeouts, trail stops).  math.Inf(1) when there are
	// no losing R contributions.
	ProfitFactor float64

	// Expectancy is the expected R per trade across all outcomes.
	Expectancy float64

	// MaxConsecLoss is the longest consecutive run of OutcomeLoss exits.
	MaxConsecLoss int

	AvgHoldDays float64
}

// Compute builds a Summary from a slice of trade results.
// An empty or nil slice returns a zero-value Summary.
func Compute(results []TradeResult) Summary {
	if len(results) == 0 {
		return Summary{}
	}

	var wins, losses, timeouts, trailStops int
	// Per-category R sums — used for per-outcome averages only.
	var sumWinR, sumLossR, sumTrailR float64
	// Profit-factor buckets — every positive/negative R contribution goes here.
	var pfPositive, pfNegative float64
	var totalHold int
	var maxConsec, curConsec int

	for _, r := range results {
		totalHold += r.HoldDays

		switch r.Outcome {
		case OutcomeWin:
			wins++
			sumWinR += r.ActualRR
			pfPositive += r.ActualRR
			curConsec = 0

		case OutcomeLoss:
			losses++
			sumLossR += r.ActualRR // negative
			pfNegative += r.ActualRR
			curConsec++
			if curConsec > maxConsec {
				maxConsec = curConsec
			}

		case OutcomeTimeout:
			timeouts++
			// Timeouts contribute to profit factor but not to win rate or averages.
			if r.ActualRR > 0 {
				pfPositive += r.ActualRR
			} else if r.ActualRR < 0 {
				pfNegative += r.ActualRR
			}

		case OutcomeTrailStop:
			trailStops++
			sumTrailR += r.ActualRR
			// Trail stops contribute to profit factor; profitable ones break loss streaks.
			if r.ActualRR > 0 {
				pfPositive += r.ActualRR
				curConsec = 0
			} else if r.ActualRR < 0 {
				pfNegative += r.ActualRR
				curConsec++
				if curConsec > maxConsec {
					maxConsec = curConsec
				}
			} else {
				curConsec = 0 // breakeven — streak reset
			}
		}
	}

	// Win rate: clean target-hit / original-SL outcomes only.
	decidable := wins + losses
	var winRate float64
	if decidable > 0 {
		winRate = float64(wins) / float64(decidable) * 100
	}

	// Per-category averages.
	var avgWinRR, avgLossRR, avgTrailStopRR float64
	if wins > 0 {
		avgWinRR = sumWinR / float64(wins)
	}
	if losses > 0 {
		avgLossRR = sumLossR / float64(losses)
	}
	if trailStops > 0 {
		avgTrailStopRR = sumTrailR / float64(trailStops)
	}

	// Profit factor across all positive / negative R contributions.
	var profitFactor float64
	switch {
	case pfNegative < 0:
		profitFactor = pfPositive / (-pfNegative)
	case pfPositive > 0:
		profitFactor = math.Inf(1) // no losing R at all
	}

	// Expectancy: mean ActualRR across every trade (simplest and unambiguous).
	var totalR float64
	for _, r := range results {
		totalR += r.ActualRR
	}
	var expectancy float64
	if len(results) > 0 {
		expectancy = totalR / float64(len(results))
	}

	var avgHold float64
	if len(results) > 0 {
		avgHold = float64(totalHold) / float64(len(results))
	}

	return Summary{
		Total:          len(results),
		Wins:           wins,
		Losses:         losses,
		Timeouts:       timeouts,
		TrailStops:     trailStops,
		WinRate:        winRate,
		AvgWinRR:       avgWinRR,
		AvgLossRR:      avgLossRR,
		AvgTrailStopRR: avgTrailStopRR,
		ProfitFactor:   profitFactor,
		Expectancy:     expectancy,
		MaxConsecLoss:  maxConsec,
		AvgHoldDays:    avgHold,
	}
}
