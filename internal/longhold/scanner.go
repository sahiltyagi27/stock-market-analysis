// Package longhold implements a "buy demonstrated strength, hold for years"
// strategy: the direct response to ANALYSIS.md §15's multibagger diagnosis.
//
// Every other strategy in this codebase (swing, crossover, meanrev, breakout)
// is short-hold and tactical (days to weeks), and each one's entry logic
// either requires a same-day bullish reversal candle or projects a target off
// a tested resistance zone above price — both of which structurally exclude a
// stock making genuine new all-time highs, which is exactly what a real
// secular compounder spends most of its life doing. This package deliberately
// avoids both: a fresh N-day high is the signal, not a disqualifier, and
// there is no fixed target at all — the position is meant to be held for
// years, exiting only when the long-term trend structure actually breaks.
package longhold

import (
	"errors"
	"fmt"
	"sort"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// Input is one stock's candle data fed into the scanner.
type Input struct {
	Symbol  string
	Candles []models.Candle
}

// Signal is a "buy strength" entry. There is no Target: this strategy has no
// fixed profit-taking level by design — see package doc.
type Signal struct {
	Symbol      string
	Price       float64
	Entry       float64
	SL          float64
	ATR         float64
	VolumeRatio float64
	Score       float64
	Reasons     []string
}

// Options controls the long-hold scanner. The zero value is filled with
// sensible defaults by withDefaults().
type Options struct {
	// HighLookback is the window for the "fresh N-day high" entry trigger.
	// Default: 252 (~1 trading year).
	HighLookback int

	// TrendEMA is the long-term trend filter: price must be above this EMA,
	// and this EMA must itself be rising (not just price above a falling
	// average). Default: 200.
	TrendEMA int

	// TrendSlopeLookback is how many candles back to compare TrendEMA against
	// to confirm it is rising. Default: 20.
	TrendSlopeLookback int

	// VolumeWindow is the lookback for the average-volume baseline.
	// Default: 20.
	VolumeWindow int

	// MinVolumeRatio is the minimum (today's volume / VolumeWindow average)
	// required to confirm the breakout has real participation, not a thin,
	// easily-reversed move. Default: 1.5.
	MinVolumeRatio float64

	// MinCandles is the minimum candle count required before any analysis.
	// Needs HighLookback history plus TrendEMA to seed reliably.
	// Default: HighLookback + TrendEMA (roughly a year of runway on top of
	// the trend filter's own warmup).
	MinCandles int
}

func (o Options) withDefaults() Options {
	out := o
	if out.HighLookback <= 0 {
		out.HighLookback = 252
	}
	if out.TrendEMA <= 0 {
		out.TrendEMA = 200
	}
	if out.TrendSlopeLookback <= 0 {
		out.TrendSlopeLookback = 20
	}
	if out.VolumeWindow <= 0 {
		out.VolumeWindow = 20
	}
	if out.MinVolumeRatio <= 0 {
		out.MinVolumeRatio = 1.5
	}
	if out.MinCandles <= 0 {
		out.MinCandles = out.HighLookback + out.TrendEMA
	}
	return out
}

// Scan runs the long-hold pipeline for every input and returns signals sorted
// by Score descending, with Symbol as a deterministic tiebreak. Filtered
// inputs are collected in the returned map.
func Scan(inputs []Input, opts Options) ([]Signal, map[string]error) {
	o := opts.withDefaults()
	errs := make(map[string]error)
	var signals []Signal

	for _, in := range inputs {
		sig, err := analyzeOne(in, o)
		if err != nil {
			errs[in.Symbol] = err
			continue
		}
		signals = append(signals, *sig)
	}

	sort.SliceStable(signals, func(i, j int) bool {
		if signals[i].Score != signals[j].Score {
			return signals[i].Score > signals[j].Score
		}
		return signals[i].Symbol < signals[j].Symbol
	})

	return signals, errs
}

// analyzeOne runs the full long-hold pipeline for a single symbol.
func analyzeOne(in Input, opts Options) (*Signal, error) {
	if len(in.Candles) == 0 {
		return nil, errors.New("no candles")
	}
	if len(in.Candles) < opts.MinCandles {
		return nil, fmt.Errorf("only %d candles (need %d)", len(in.Candles), opts.MinCandles)
	}

	n := len(in.Candles)
	closes := make([]float64, n)
	highs := make([]float64, n)
	for i, c := range in.Candles {
		closes[i] = c.Close
		highs[i] = c.High
	}
	price := closes[n-1]

	trendSeries, err := analysis.EMA(closes, opts.TrendEMA)
	if err != nil {
		return nil, fmt.Errorf("EMA%d: %w", opts.TrendEMA, err)
	}
	trend := trendSeries[n-1]
	if trend <= 0 {
		return nil, fmt.Errorf("EMA%d not seeded", opts.TrendEMA)
	}
	if price <= trend {
		return nil, fmt.Errorf("price %.2f not above EMA%d %.2f — trend not constructive", price, opts.TrendEMA, trend)
	}
	slopeIdx := n - 1 - opts.TrendSlopeLookback
	if slopeIdx < opts.TrendEMA-1 {
		return nil, fmt.Errorf("insufficient history for EMA%d slope check", opts.TrendEMA)
	}
	if trend <= trendSeries[slopeIdx] {
		return nil, fmt.Errorf("EMA%d flat/declining: current %.2f <= %.2f (%d candles ago)",
			opts.TrendEMA, trend, trendSeries[slopeIdx], opts.TrendSlopeLookback)
	}

	// Fresh N-day high: today's High exceeds the max High over the prior
	// lookback window. On a sustained, uninterrupted uptrend this condition
	// is true every day, not just on a single "breakout day" — that's fine
	// here, since the backtest engine never opens a second position in a
	// symbol it already holds (see portfolio.go), so only the first day this
	// fires actually matters. It only matters as a genuine per-day filter
	// after a stock has pulled back below its prior high and is trying again.
	lookbackStart := n - opts.HighLookback
	if lookbackStart < 0 {
		lookbackStart = 0
	}
	todayHigh := highs[n-1]
	priorHigh := 0.0
	for i := lookbackStart; i < n-1; i++ {
		if highs[i] > priorHigh {
			priorHigh = highs[i]
		}
	}
	if todayHigh <= priorHigh {
		return nil, fmt.Errorf("not a fresh %d-day high: today's high %.2f <= prior high %.2f", opts.HighLookback, todayHigh, priorHigh)
	}

	avgVol, lastVol := volumeStats(in.Candles, opts.VolumeWindow)
	if avgVol <= 0 {
		return nil, errors.New("insufficient volume history")
	}
	volRatio := lastVol / avgVol
	if volRatio < opts.MinVolumeRatio {
		return nil, fmt.Errorf("volume ratio %.2fx below minimum %.2fx — breakout not confirmed", volRatio, opts.MinVolumeRatio)
	}

	// SL is deliberately wide and structural, not a tight ATR buffer: the
	// long-term trend EMA itself. Risk-based position sizing (unchanged from
	// swing/breakout) naturally allocates less capital to a wider stop, which
	// is how this strategy "sizes for volatility" without new sizing logic.
	sl := trend
	if sl <= 0 || sl >= price {
		return nil, fmt.Errorf("invalid SL %.2f for price %.2f", sl, price)
	}

	sig := &Signal{
		Symbol:      in.Symbol,
		Price:       price,
		Entry:       price,
		SL:          sl,
		ATR:         analysis.ATR(in.Candles, 14),
		VolumeRatio: volRatio,
		Score:       score(price, trend, volRatio, opts),
	}
	sig.Reasons = buildReasons(sig, opts)
	return sig, nil
}

func volumeStats(candles []models.Candle, window int) (avg, last float64) {
	n := len(candles)
	if n == 0 {
		return 0, 0
	}
	last = float64(candles[n-1].Volume)
	start := n - 1 - window
	if start < 0 {
		start = 0
	}
	var sum float64
	count := 0
	for i := start; i < n-1; i++ {
		sum += float64(candles[i].Volume)
		count++
	}
	if count == 0 {
		return 0, last
	}
	return sum / float64(count), last
}

// score maps a signal to 0–100, rewarding strong volume confirmation and
// trend distance (how decisively price has cleared its long-term average),
// with volume weighted more heavily — a thin-volume new high is the easiest
// way for this permissive an entry filter to be fooled.
func score(price, trend, volRatio float64, opts Options) float64 {
	extension := (price - trend) / trend * 100
	extScore := clamp(extension/20, 0, 1) * 30 // up to 30 pts, saturates at 20% above trend
	volScore := clamp((volRatio-opts.MinVolumeRatio)/3, 0, 1) * 45
	return extScore + volScore + 25 // +25 base for clearing every gate
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func buildReasons(sig *Signal, opts Options) []string {
	return []string{
		fmt.Sprintf("Fresh %d-day high on %.2fx volume", opts.HighLookback, sig.VolumeRatio),
		fmt.Sprintf("Price %.2f above rising EMA%d (SL %.2f)", sig.Price, opts.TrendEMA, sig.SL),
		"No fixed target — held until the long-term trend structure breaks (see exit-mode trendstop)",
	}
}
