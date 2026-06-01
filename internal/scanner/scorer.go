package scanner

import "github.com/sahiltyagi27/stock-market-analysis/internal/analysis"

// Score breakdown:
//   +40  trend   — price above both EMA50 and EMA200
//   +30  R/R     — scaled by quality grade
//   +20  support — proportional to zone touch count (capped at 4)
//   +10  volume  — recent candle volume vs 20-period average

const (
	maxTrend   = 40.0
	maxRR      = 30.0
	maxSupport = 20.0
	maxVolume  = 10.0
)

func score(sig *StockSignal, avgVolume, lastVolume float64) float64 {
	return scoreBreakdown(sig, avgVolume, lastVolume).Total()
}

func scoreBreakdown(sig *StockSignal, avgVolume, lastVolume float64) ScoreBreakdown {
	return ScoreBreakdown{
		Trend:   trendScore(sig.Trend),
		RR:      rrScore(sig.Trade.Quality),
		Support: supportScore(sig.Support),
		Volume:  volumeScore(avgVolume, lastVolume),
	}
}

func (b ScoreBreakdown) Total() float64 {
	return b.Trend + b.RR + b.Support + b.Volume
}

func trendScore(t Trend) float64 {
	switch t {
	case TrendBullish:
		return maxTrend
	case TrendNeutral:
		return maxTrend / 2
	default:
		return 0
	}
}

func rrScore(q analysis.Quality) float64 {
	switch q {
	case analysis.QualityExcellent:
		return maxRR
	case analysis.QualityGood:
		return maxRR * 0.75
	case analysis.QualityFair:
		return maxRR * 0.40
	default:
		return 0
	}
}

func supportScore(z analysis.Zone) float64 {
	touches := z.Touches
	if touches > 4 {
		touches = 4
	}
	return float64(touches) / 4.0 * maxSupport
}

// volumeScore awards full points when the last candle's volume is ≥ 1.5× the
// rolling average, and zero when volume is at or below average.
func volumeScore(avg, last float64) float64 {
	if avg <= 0 {
		return 0
	}
	ratio := last / avg
	if ratio >= 1.5 {
		return maxVolume
	}
	if ratio <= 1.0 {
		return 0
	}
	// Linear interpolation between 1.0× and 1.5×.
	return ((ratio - 1.0) / 0.5) * maxVolume
}
