package backtest

import "math"

// Summary aggregates a slice of TradeResult into headline performance stats.
type Summary struct {
	Total    int
	Wins     int
	Losses   int
	Timeouts int

	// WinRate is Wins / (Wins + Losses) as a percentage (0–100).
	// Timeouts are excluded so the metric reflects resolved trades only.
	WinRate float64

	// AvgWinRR is the mean actual R:R of all winning trades.
	AvgWinRR float64
	// AvgLossRR is the mean actual R:R of all losing trades (always ≤ 0).
	AvgLossRR float64

	// ProfitFactor is sumPositiveR / |sumNegativeR| across all trades including
	// timeouts. math.Inf(1) when there are no losing R contributions.
	ProfitFactor float64

	// Expectancy is the expected R per decided trade (wins + losses, no timeouts):
	//   winRate * avgWinRR + lossRate * avgLossRR
	Expectancy float64

	// MaxConsecLoss is the longest consecutive OutcomeLoss run.
	MaxConsecLoss int

	AvgHoldDays float64
}

// Compute builds a Summary from a slice of trade results.
// An empty or nil slice returns a zero-value Summary.
func Compute(results []TradeResult) Summary {
	if len(results) == 0 {
		return Summary{}
	}

	var wins, losses, timeouts int
	var sumWinR, sumLossR float64
	var totalHold int
	var maxConsec, curConsec int

	for _, r := range results {
		totalHold += r.HoldDays

		switch r.Outcome {
		case OutcomeWin:
			wins++
			sumWinR += r.ActualRR
			curConsec = 0

		case OutcomeLoss:
			losses++
			sumLossR += r.ActualRR // negative
			curConsec++
			if curConsec > maxConsec {
				maxConsec = curConsec
			}

		case OutcomeTimeout:
			timeouts++
			// Include timeout R/R in profit-factor accounting but not in win rate.
			if r.ActualRR > 0 {
				sumWinR += r.ActualRR
			} else if r.ActualRR < 0 {
				sumLossR += r.ActualRR
			}
		}
	}

	decidable := wins + losses

	var winRate float64
	if decidable > 0 {
		winRate = float64(wins) / float64(decidable) * 100
	}

	var avgWinRR, avgLossRR float64
	if wins > 0 {
		avgWinRR = sumWinR / float64(wins)
	}
	if losses > 0 {
		avgLossRR = sumLossR / float64(losses)
	}

	var profitFactor float64
	switch {
	case sumLossR < 0:
		profitFactor = sumWinR / (-sumLossR)
	case sumWinR > 0:
		profitFactor = math.Inf(1) // no losses
	}

	// Expectancy per decided trade (excludes timeouts for a clean measure).
	var expectancy float64
	if decidable > 0 {
		wr := float64(wins) / float64(decidable)
		lr := float64(losses) / float64(decidable)
		expectancy = wr*avgWinRR + lr*avgLossRR
	}

	var avgHold float64
	if len(results) > 0 {
		avgHold = float64(totalHold) / float64(len(results))
	}

	return Summary{
		Total:         len(results),
		Wins:          wins,
		Losses:        losses,
		Timeouts:      timeouts,
		WinRate:       winRate,
		AvgWinRR:      avgWinRR,
		AvgLossRR:     avgLossRR,
		ProfitFactor:  profitFactor,
		Expectancy:    expectancy,
		MaxConsecLoss: maxConsec,
		AvgHoldDays:   avgHold,
	}
}
