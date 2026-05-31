package analysis

import "fmt"

// Standard EMA periods.
const (
	Period10  = 10
	Period50  = 50
	Period200 = 200
)

// EMAResult holds the current EMA values for the three standard periods.
// A zero value means insufficient data to compute that period.
type EMAResult struct {
	EMA10  float64 `json:"ema_10"`
	EMA50  float64 `json:"ema_50"`
	EMA200 float64 `json:"ema_200"`
}

// EMA computes a single exponential moving average series from prices.
// Returns an error if prices is empty or period < 1.
// The first EMA value is seeded from the SMA of the first `period` prices;
// subsequent values use the standard EMA formula:
//
//	EMA = price * k + prevEMA * (1 - k),  where k = 2 / (period + 1)
func EMA(prices []float64, period int) ([]float64, error) {
	if period < 1 {
		return nil, fmt.Errorf("period must be >= 1, got %d", period)
	}
	if len(prices) == 0 {
		return nil, fmt.Errorf("prices must not be empty")
	}
	if len(prices) < period {
		return nil, fmt.Errorf("need at least %d prices for period %d, got %d", period, period, len(prices))
	}

	k := 2.0 / float64(period+1)
	result := make([]float64, len(prices))

	// Seed with SMA of the first `period` values.
	var sum float64
	for i := range period {
		sum += prices[i]
	}
	result[period-1] = sum / float64(period)

	for i := period; i < len(prices); i++ {
		result[i] = prices[i]*k + result[i-1]*(1-k)
	}

	return result, nil
}

// CurrentEMA returns only the most recent EMA value for the given period.
// Equivalent to EMA(prices, period)[last], without allocating the full slice.
func CurrentEMA(prices []float64, period int) (float64, error) {
	if period < 1 {
		return 0, fmt.Errorf("period must be >= 1, got %d", period)
	}
	if len(prices) < period {
		return 0, fmt.Errorf("need at least %d prices for period %d, got %d", period, period, len(prices))
	}

	k := 2.0 / float64(period+1)

	var ema float64
	for i := range period {
		ema += prices[i]
	}
	ema /= float64(period)

	for i := period; i < len(prices); i++ {
		ema = prices[i]*k + ema*(1-k)
	}
	return ema, nil
}

// ComputeEMAs returns the current EMA10, EMA50, and EMA200 from a price series.
// Periods with insufficient data are returned as 0.
func ComputeEMAs(prices []float64) EMAResult {
	var r EMAResult
	if v, err := CurrentEMA(prices, Period10); err == nil {
		r.EMA10 = v
	}
	if v, err := CurrentEMA(prices, Period50); err == nil {
		r.EMA50 = v
	}
	if v, err := CurrentEMA(prices, Period200); err == nil {
		r.EMA200 = v
	}
	return r
}
