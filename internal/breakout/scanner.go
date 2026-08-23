// Package breakout scans for confirmed resistance-zone breakouts: a stock in
// a constructive uptrend that closes above a well-tested resistance zone on
// above-average volume. Unlike scanner.BreakoutSignal (a "watch below
// resistance" list), this package fires the trade itself — SL, target, R/R —
// so it can be walked forward by the backtest engine like swing/crossover/meanrev.
//
// REJECTED as a paper-trading strategy (see ANALYSIS.md §13): underperforms
// swing on expectancy/profit-factor at default parameters, and widening the
// stop to close that gap trades it for a 47-trade max losing streak (vs
// swing's 9) at 54x the trade frequency. Kept in-tree for reproducibility and
// as a standalone research tool via `cmd/backtest --mode breakout`.
package breakout

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

// Signal is a confirmed breakout trade setup.
type Signal struct {
	Symbol      string
	Price       float64
	Entry       float64
	SL          float64
	Target      float64
	ATR         float64
	VolumeRatio float64
	Score       float64
	Reasons     []string
}

// Options controls breakout scanner behaviour. The zero value is filled with
// sensible defaults by withDefaults().
type Options struct {
	// ZoneOpts controls support/resistance zone detection. MinResistanceTouches
	// defaults to 2 here (a 1-touch zone is a single spike, not a tested level).
	ZoneOpts analysis.ZoneOptions

	// MinRR is the minimum reward/risk when no resistance zone exists above
	// price to size the target from (falls back to entry + MinRR × risk).
	// Default: 2.0.
	MinRR float64

	// StopATRMultiplier sets the buffer below the broken resistance zone's Low
	// (now acting as support): SL = zone.Low − StopATRMultiplier × ATR.
	// Default: 1.0 (tighter than swing's pullback stop — a breakout failing to
	// hold its own former resistance is a fast invalidation, not a wide dip).
	StopATRMultiplier float64

	// ATRPeriod is the ATR lookback for the stop. Default: 14.
	ATRPeriod int

	// VolumeWindow is the lookback for the average-volume baseline. Default: 20.
	VolumeWindow int

	// MinVolumeRatio is the minimum (today's volume / VolumeWindow average)
	// required to confirm a breakout. A break on thin volume is easily faked.
	// Default: 1.5.
	MinVolumeRatio float64

	// MinCandles is the minimum candle count required before any analysis.
	// EMA200 needs 200 candles to seed reliably. Default: 210.
	MinCandles int
}

func (o Options) withDefaults() Options {
	out := o
	if out.ZoneOpts.MinResistanceTouches <= 0 {
		out.ZoneOpts.MinResistanceTouches = 2
	}
	if out.MinRR <= 0 {
		out.MinRR = 2.0
	}
	if out.StopATRMultiplier <= 0 {
		out.StopATRMultiplier = 1.0
	}
	if out.ATRPeriod <= 0 {
		out.ATRPeriod = 14
	}
	if out.VolumeWindow <= 0 {
		out.VolumeWindow = 20
	}
	if out.MinVolumeRatio <= 0 {
		out.MinVolumeRatio = 1.5
	}
	if out.MinCandles <= 0 {
		out.MinCandles = 210
	}
	return out
}

// Scan runs the breakout pipeline for every input and returns signals sorted
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

// analyzeOne runs the full breakout pipeline for a single symbol.
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
	lows := make([]float64, n)
	for i, c := range in.Candles {
		closes[i] = c.Close
		highs[i] = c.High
		lows[i] = c.Low
	}
	price := closes[n-1]
	prevClose := closes[n-2]

	// Constructive trend filter: price above both EMA50 and EMA200 — the same
	// bullish-trend bar the swing/meanrev scanners use.
	ema := analysis.ComputeEMAs(closes)
	if ema.EMA50 <= 0 || ema.EMA200 <= 0 {
		return nil, errors.New("EMA50/EMA200 not seeded")
	}
	if price <= ema.EMA50 || price <= ema.EMA200 {
		return nil, fmt.Errorf("price %.2f not above EMA50 %.2f / EMA200 %.2f — trend not constructive", price, ema.EMA50, ema.EMA200)
	}

	// Zone detection excludes today's candle so the resistance zone being
	// broken was actually tested by prior price action, not formed by the
	// breakout candle itself.
	zones := analysis.FindZones(highs[:n-1], lows[:n-1], opts.ZoneOpts)

	broken, found := highestResistanceBelow(price, zones.Resistance)
	if !found {
		return nil, errors.New("no tested resistance zone below price to have broken out from")
	}
	if prevClose > broken.High {
		return nil, fmt.Errorf("price was already above resistance %.2f–%.2f on the prior close — not a fresh breakout", broken.Low, broken.High)
	}

	atr := analysis.ATR(in.Candles, opts.ATRPeriod)
	if atr <= 0 {
		return nil, errors.New("ATR unavailable")
	}
	sl := broken.Low - opts.StopATRMultiplier*atr
	if sl <= 0 || sl >= price {
		return nil, fmt.Errorf("invalid SL %.2f for price %.2f", sl, price)
	}

	target, hasNextZone := lowestResistanceAbove(price, zones.Resistance)
	if !hasNextZone || target-price < opts.MinRR*(price-sl) {
		// No next resistance zone, or it's too close to clear MinRR — size the
		// target directly off the risk instead.
		target = price + opts.MinRR*(price-sl)
	}

	rr := (target - price) / (price - sl)
	if rr < opts.MinRR {
		return nil, fmt.Errorf("R/R %.2f below minimum %.2f", rr, opts.MinRR)
	}

	avgVol, lastVol := volumeStats(in.Candles, opts.VolumeWindow)
	if avgVol <= 0 {
		return nil, errors.New("insufficient volume history")
	}
	volRatio := lastVol / avgVol
	if volRatio < opts.MinVolumeRatio {
		return nil, fmt.Errorf("volume ratio %.2fx below minimum %.2fx — breakout not confirmed", volRatio, opts.MinVolumeRatio)
	}

	sig := &Signal{
		Symbol:      in.Symbol,
		Price:       price,
		Entry:       price,
		SL:          sl,
		Target:      target,
		ATR:         atr,
		VolumeRatio: volRatio,
		Score:       score(rr, volRatio, broken.Touches, opts),
	}
	sig.Reasons = buildReasons(sig, broken, rr, opts)
	return sig, nil
}

// highestResistanceBelow returns the resistance zone with the highest High
// that still sits at or below price — the zone price most recently broke
// through.
func highestResistanceBelow(price float64, zones []analysis.Zone) (best analysis.Zone, found bool) {
	for _, z := range zones {
		if z.High > price {
			continue
		}
		if !found || z.High > best.High {
			best = z
			found = true
		}
	}
	return best, found
}

// lowestResistanceAbove returns the Low of the nearest resistance zone
// strictly above price — the next ceiling a breakout would need to clear,
// used directly as the target price.
func lowestResistanceAbove(price float64, zones []analysis.Zone) (target float64, found bool) {
	for _, z := range zones {
		if z.Low <= price {
			continue
		}
		if !found || z.Low < target {
			target = z.Low
			found = true
		}
	}
	return target, found
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

// score maps a signal to 0–100, rewarding strong R/R, strong volume
// confirmation, and a well-tested (multi-touch) resistance zone.
func score(rr, volRatio float64, touches int, opts Options) float64 {
	rrScore := clamp((rr-opts.MinRR)/opts.MinRR, 0, 1) * 40      // up to 40 pts for R/R well above minimum
	volScore := clamp((volRatio-opts.MinVolumeRatio)/2, 0, 1) * 35 // up to 35 pts for strong volume
	touchScore := clamp(float64(touches-opts.ZoneOpts.MinResistanceTouches)/3, 0, 1) * 25 // up to 25 pts for a well-tested zone
	return rrScore + volScore + touchScore + 25 // +25 base for clearing every gate
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

func buildReasons(sig *Signal, broken analysis.Zone, rr float64, opts Options) []string {
	return []string{
		fmt.Sprintf("Closed above resistance %.2f–%.2f (%d touches) on %.2fx volume",
			broken.Low, broken.High, broken.Touches, sig.VolumeRatio),
		fmt.Sprintf("SL %.2f = former resistance low %.2f − %.1f×ATR(%.2f)",
			sig.SL, broken.Low, opts.StopATRMultiplier, sig.ATR),
		fmt.Sprintf("Target %.2f, R/R %.2f", sig.Target, rr),
	}
}
