package analysis

import "fmt"

// RSI computes the Relative Strength Index series using Wilder's smoothing
// (the standard used by TradingView, Kite Charts, etc.), matching the
// convention already used by ATR in this package.
//
// The formula:
//
//	change_i  = price_i − price_{i-1}
//	gain_i    = max(change_i, 0)         loss_i = max(−change_i, 0)
//	Seed      = simple average of the first `period` gains and losses
//	avgGain_i = ((period−1) × avgGain_{i-1} + gain_i) / period   (Wilder RMA)
//	avgLoss_i = ((period−1) × avgLoss_{i-1} + loss_i) / period
//	RS_i      = avgGain_i / avgLoss_i
//	RSI_i     = 100 − 100 / (1 + RS_i)
//
// The returned slice has the same length as prices. Indices that cannot be
// computed yet (i < period — the seed needs `period` changes, i.e. period+1
// prices) are left as 0, so callers should treat a 0 as "not seeded" exactly
// like the EMA series does. When avgLoss is 0 (an unbroken up-run) RSI is 100.
func RSI(prices []float64, period int) ([]float64, error) {
	if period < 1 {
		return nil, fmt.Errorf("period must be >= 1, got %d", period)
	}
	if len(prices) == 0 {
		return nil, fmt.Errorf("prices must not be empty")
	}

	out := make([]float64, len(prices))
	// Need period changes (period+1 prices) before the first RSI value at
	// index `period`. With fewer prices, every index stays 0 (not seeded).
	if len(prices) < period+1 {
		return out, nil
	}

	// Seed: simple average of the first `period` gains and losses.
	var sumGain, sumLoss float64
	for i := 1; i <= period; i++ {
		ch := prices[i] - prices[i-1]
		if ch >= 0 {
			sumGain += ch
		} else {
			sumLoss += -ch
		}
	}
	avgGain := sumGain / float64(period)
	avgLoss := sumLoss / float64(period)
	out[period] = rsiFrom(avgGain, avgLoss)

	// Wilder smoothing for the rest.
	for i := period + 1; i < len(prices); i++ {
		ch := prices[i] - prices[i-1]
		gain, loss := 0.0, 0.0
		if ch >= 0 {
			gain = ch
		} else {
			loss = -ch
		}
		avgGain = (avgGain*float64(period-1) + gain) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + loss) / float64(period)
		out[i] = rsiFrom(avgGain, avgLoss)
	}

	return out, nil
}

// rsiFrom converts smoothed average gain/loss into an RSI value. A zero
// average loss (no down moves in the window) yields 100; a zero average gain
// with some loss yields 0.
func rsiFrom(avgGain, avgLoss float64) float64 {
	if avgLoss == 0 {
		if avgGain == 0 {
			return 50 // perfectly flat — neutral
		}
		return 100
	}
	rs := avgGain / avgLoss
	return 100 - 100/(1+rs)
}
