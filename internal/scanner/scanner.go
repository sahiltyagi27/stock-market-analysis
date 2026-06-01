package scanner

import (
	"fmt"
	"sort"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// Options controls scanner sensitivity.
type Options struct {
	// MinRR is the minimum risk/reward ratio required to emit a signal.
	// Default: 2.0.
	MinRR float64

	// EMAMarginPct is the minimum percentage gap required between the current
	// price and EMA200 before a stock qualifies as bullish.
	// A stock where price is barely above EMA200 is fragile and can flip
	// non-bullish on the next tick.
	// Default: 1.0 (price must be at least 1% above EMA200).
	// Set to 0 to disable the check entirely.
	EMAMarginPct float64

	// MinAvgVolume is the minimum rolling average daily volume (over VolumeWindow
	// candles) required for a symbol to qualify. Illiquid stocks are hard to exit
	// cleanly and their zone levels are less reliable.
	// Default: 0 (disabled). A typical useful value is 200_000.
	MinAvgVolume int64

	// ZoneOpts are passed through to FindZones.
	ZoneOpts analysis.ZoneOptions

	// AnalyzerOpts are passed through to Analyze.
	AnalyzerOpts analysis.AnalyzerOptions

	// VolumeWindow is the number of candles used to compute the average volume.
	// Default: 20.
	VolumeWindow int
}

func (o *Options) withDefaults() Options {
	out := *o
	if out.MinRR <= 0 {
		out.MinRR = 2.0
	}
	if out.EMAMarginPct == 0 {
		out.EMAMarginPct = 1.0 // default: price must be ≥1% above EMA200
	}
	if out.VolumeWindow <= 0 {
		out.VolumeWindow = 20
	}
	// Require resistance zones to have been tested at least twice historically.
	// A 1-touch zone is just a single-session spike and is not a reliable target.
	// Set ZoneOpts.MinResistanceTouches = 1 to disable.
	if out.ZoneOpts.MinResistanceTouches <= 0 {
		out.ZoneOpts.MinResistanceTouches = 2
	}
	return out
}

// Scan runs the full analysis pipeline for every input and returns signals
// that pass the bullish filter, ranked by score descending.
//
// Bullish filter:
//   - Price > EMA50  AND  Price > EMA200
//   - Long trade R/R >= MinRR
//
// Symbols with insufficient candle history or no valid zone pair are silently
// skipped; use ScanWithErrors to surface those failures.
func Scan(inputs []Input, opts Options) []StockSignal {
	signals, _ := ScanWithErrors(inputs, opts)
	return signals
}

// ScanWithErrors is the same as Scan but also returns a map of symbol → error
// for every input that was skipped.
func ScanWithErrors(inputs []Input, opts Options) ([]StockSignal, map[string]error) {
	o := opts.withDefaults()
	errs := make(map[string]error)
	var signals []StockSignal

	for _, in := range inputs {
		sig, err := analyzeOne(in, o)
		if err != nil {
			errs[in.Symbol] = err
			continue
		}
		signals = append(signals, *sig)
	}

	sort.Slice(signals, func(i, j int) bool {
		return signals[i].Score > signals[j].Score
	})
	return signals, errs
}

// Diagnose returns price, EMA, trend, and any scanner rejection reason for
// every input. It is useful for explaining why symbols did not become signals.
func Diagnose(inputs []Input, opts Options) []Diagnostic {
	o := opts.withDefaults()
	out := make([]Diagnostic, 0, len(inputs))

	for _, in := range inputs {
		d := Diagnostic{Symbol: in.Symbol}
		if len(in.Candles) == 0 {
			d.Error = "no candles"
			out = append(out, d)
			continue
		}

		closes := extractCloses(in.Candles)
		d.Price = closes[len(closes)-1]
		d.EMA = analysis.ComputeEMAs(closes)
		d.Trend = deriveTrend(d.Price, d.EMA)

		if _, err := analyzeOne(in, o); err != nil {
			d.Error = err.Error()
		}
		out = append(out, d)
	}
	return out
}

// analyzeOne runs the pipeline for a single stock and applies the bullish filter.
func analyzeOne(in Input, opts Options) (*StockSignal, error) {
	if len(in.Candles) == 0 {
		return nil, fmt.Errorf("no candles")
	}

	closes := extractCloses(in.Candles)
	highs := extractHighs(in.Candles)
	lows := extractLows(in.Candles)

	// --- EMA ---
	emas := analysis.ComputeEMAs(closes)
	price := closes[len(closes)-1]

	trend := deriveTrend(price, emas)

	// Bullish filter: must be above both EMA50 and EMA200.
	if trend != TrendBullish {
		return nil, fmt.Errorf("trend is %s, not bullish", trend)
	}

	// EMA margin filter: price must be at least EMAMarginPct% above EMA200.
	// Stocks sitting barely above EMA200 are fragile — a single tick can flip
	// them non-bullish, making the signal unreliable.
	if opts.EMAMarginPct > 0 && emas.EMA200 > 0 {
		gapPct := (price - emas.EMA200) / emas.EMA200 * 100
		if gapPct < opts.EMAMarginPct {
			return nil, fmt.Errorf(
				"price %.2f is only %.2f%% above EMA200 (%.2f), minimum %.1f%% required",
				price, gapPct, emas.EMA200, opts.EMAMarginPct,
			)
		}
	}

	// --- Zones ---
	zones := analysis.FindZones(highs, lows, opts.ZoneOpts)

	support, resistance, err := nearestZones(price, zones)
	if err != nil {
		return nil, err
	}

	// --- Trade setup ---
	ta, err := analysis.Analyze(price, support, resistance, opts.AnalyzerOpts)
	if err != nil {
		return nil, fmt.Errorf("trade analyzer: %w", err)
	}

	// R/R filter.
	if ta.Long.RiskReward < opts.MinRR {
		return nil, fmt.Errorf("R/R %.2f below minimum %.2f", ta.Long.RiskReward, opts.MinRR)
	}

	// --- Volume ---
	avgVol, lastVol := volumeStats(in.Candles, opts.VolumeWindow)

	// Liquidity filter: skip symbols whose rolling average volume is below the
	// minimum. Guard on avgVol > 0 so we don't reject stocks with only one
	// candle (no prior history to average over).
	if opts.MinAvgVolume > 0 && avgVol > 0 && avgVol < float64(opts.MinAvgVolume) {
		return nil, fmt.Errorf("avg daily volume %.0f below minimum %d", avgVol, opts.MinAvgVolume)
	}

	sig := &StockSignal{
		Symbol:     in.Symbol,
		Price:      price,
		Trend:      trend,
		EMA:        emas,
		Support:    support,
		Resistance: resistance,
		Trade:      *ta.Long,
	}
	sig.Breakdown = scoreBreakdown(sig, avgVol, lastVol)
	sig.Score = sig.Breakdown.Total()
	sig.Reasons = buildReasons(sig, avgVol, lastVol, opts.MinRR)

	return sig, nil
}

// nearestZones returns the highest support zone below price and the lowest
// resistance zone above price.
func nearestZones(price float64, zones analysis.ZoneResult) (support, resistance analysis.Zone, err error) {
	var foundSupport, foundResistance bool

	// Support zones are sorted strongest-first; find the highest one below price.
	for _, z := range zones.Support {
		if z.High < price {
			if !foundSupport || z.Mid > support.Mid {
				support = z
				foundSupport = true
			}
		}
	}
	// Resistance zones: find the lowest one above price.
	for _, z := range zones.Resistance {
		if z.Low > price {
			if !foundResistance || z.Mid < resistance.Mid {
				resistance = z
				foundResistance = true
			}
		}
	}

	if !foundSupport {
		return analysis.Zone{}, analysis.Zone{}, fmt.Errorf("no support zone below price %.2f", price)
	}
	if !foundResistance {
		return analysis.Zone{}, analysis.Zone{}, fmt.Errorf("no resistance zone above price %.2f", price)
	}
	return support, resistance, nil
}

func deriveTrend(price float64, emas analysis.EMAResult) Trend {
	above50 := emas.EMA50 > 0 && price > emas.EMA50
	above200 := emas.EMA200 > 0 && price > emas.EMA200
	below50 := emas.EMA50 > 0 && price < emas.EMA50
	below200 := emas.EMA200 > 0 && price < emas.EMA200

	switch {
	case above50 && above200:
		return TrendBullish
	case below50 && below200:
		return TrendBearish
	default:
		return TrendNeutral
	}
}

func volumeStats(candles []models.Candle, window int) (avg, last float64) {
	if len(candles) == 0 {
		return 0, 0
	}
	last = float64(candles[len(candles)-1].Volume)

	// Exclude the latest candle from the average — it is the candle being
	// evaluated, so including it would understate spikes and overstate dips.
	history := candles[:len(candles)-1]
	if len(history) == 0 {
		// Only one candle: no prior history to average over.
		return 0, last
	}

	start := len(history) - window
	if start < 0 {
		start = 0
	}
	window_ := history[start:]
	var sum float64
	for _, c := range window_ {
		sum += float64(c.Volume)
	}
	avg = sum / float64(len(window_))
	return avg, last
}

func extractCloses(candles []models.Candle) []float64 {
	out := make([]float64, len(candles))
	for i, c := range candles {
		out[i] = c.Close
	}
	return out
}

func extractHighs(candles []models.Candle) []float64 {
	out := make([]float64, len(candles))
	for i, c := range candles {
		out[i] = c.High
	}
	return out
}

func extractLows(candles []models.Candle) []float64 {
	out := make([]float64, len(candles))
	for i, c := range candles {
		out[i] = c.Low
	}
	return out
}
