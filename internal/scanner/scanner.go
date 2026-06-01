package scanner

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// errNoCandles is returned when a scanner input has an empty candle slice.
var errNoCandles = errors.New("no candles")

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

	// MaxEMA10ExtensionPct rejects setups that are too far above EMA10.
	// Default: 8.0. Set to < 0 to disable.
	MaxEMA10ExtensionPct float64

	// MaxEMA50ExtensionPct rejects setups that are too far above EMA50.
	// Default: 15.0. Set to < 0 to disable.
	MaxEMA50ExtensionPct float64

	// MaxSupportExtensionPct rejects setups that are too far above the selected
	// support zone high.
	// Default: 5.0. Set to < 0 to disable.
	MaxSupportExtensionPct float64

	// MaxMove10DPct rejects setups that have already moved too much over the
	// last 10 candles.
	// Default: 12.0. Set to < 0 to disable.
	MaxMove10DPct float64

	// MaxBreakoutDistancePct is the maximum distance below resistance for a
	// breakout-watch candidate.
	// Default: 3.0. Set to < 0 to disable.
	MaxBreakoutDistancePct float64

	// MinCandles is the minimum number of candles required before a symbol is
	// analysed. EMA200 needs ≥200 data points to be reliable; fewer candles
	// produce an unstable trend signal and noisy zones.
	// Default: 200.
	MinCandles int

	// ATRPeriod is the lookback for Average True Range used to size the stop
	// loss. When > 0 the SL is placed ATRMultiplier × ATR below the support
	// zone, adapting to each stock's actual volatility rather than a fixed %.
	// Default: 14. Set to a negative value to use the fixed SLBufferPct.
	ATRPeriod int

	// ATRMultiplier scales the ATR for SL placement. Default: 1.5.
	// SL = support.Low − ATRMultiplier × ATR14.
	ATRMultiplier float64

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
	if out.MaxEMA10ExtensionPct == 0 {
		out.MaxEMA10ExtensionPct = 8.0
	}
	if out.MaxEMA50ExtensionPct == 0 {
		out.MaxEMA50ExtensionPct = 15.0
	}
	if out.MaxSupportExtensionPct == 0 {
		out.MaxSupportExtensionPct = 5.0
	}
	if out.MaxMove10DPct == 0 {
		out.MaxMove10DPct = 12.0
	}
	if out.MaxBreakoutDistancePct == 0 {
		out.MaxBreakoutDistancePct = 3.0
	}
	// Require resistance zones to have been tested at least twice historically.
	// A 1-touch zone is just a single-session spike and is not a reliable target.
	// Set ZoneOpts.MinResistanceTouches = 1 to disable.
	if out.ZoneOpts.MinResistanceTouches <= 0 {
		out.ZoneOpts.MinResistanceTouches = 2
	}
	// ATR-based SL: default period 14, multiplier 1.5.
	// Negative ATRPeriod disables ATR and falls back to SLBufferPct.
	if out.ATRPeriod == 0 {
		out.ATRPeriod = 14
	}
	if out.ATRMultiplier <= 0 {
		out.ATRMultiplier = 1.5
	}
	// Minimum candle count: EMA200 needs at least 200 data points to be
	// meaningful; fewer candles produce an unreliable trend signal.
	if out.MinCandles <= 0 {
		out.MinCandles = 200
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

// ScanBreakouts returns stocks sitting just below tested resistance zones.
// These are watchlist candidates for confirmation, not immediate trade signals.
func ScanBreakouts(inputs []Input, opts Options) ([]BreakoutSignal, map[string]error) {
	o := opts.withDefaults()
	errs := make(map[string]error)
	var signals []BreakoutSignal

	for _, in := range inputs {
		sig, err := analyzeBreakout(in, o)
		if err != nil {
			errs[in.Symbol] = err
			continue
		}
		signals = append(signals, *sig)
	}

	sort.Slice(signals, func(i, j int) bool {
		if signals[i].Score != signals[j].Score {
			return signals[i].Score > signals[j].Score
		}
		return signals[i].DistanceToResistancePct < signals[j].DistanceToResistancePct
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
			d.Error = errNoCandles.Error()
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
		return nil, errNoCandles
	}
	if len(in.Candles) < opts.MinCandles {
		return nil, fmt.Errorf("only %d candles available, need %d for reliable EMA200 and zones",
			len(in.Candles), opts.MinCandles)
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
	// Compute ATR and inject into analyzer options so the SL adapts to this
	// stock's volatility. Negative ATRPeriod disables ATR (falls back to fixed buffer).
	analyzerOpts := opts.AnalyzerOpts
	if opts.ATRPeriod > 0 {
		analyzerOpts.ATR = analysis.ATR(in.Candles, opts.ATRPeriod)
		analyzerOpts.ATRMultiplier = opts.ATRMultiplier
	}
	ta, err := analysis.Analyze(price, support, resistance, analyzerOpts)
	if err != nil {
		return nil, fmt.Errorf("trade analyzer: %w", err)
	}

	// R/R filter.
	if ta.Long.RiskReward < opts.MinRR {
		return nil, fmt.Errorf("R/R %.2f below minimum %.2f", ta.Long.RiskReward, opts.MinRR)
	}

	ext := extensionDiagnostics(price, closes, emas, support)
	if err := validateExtension(ext, opts); err != nil {
		return nil, err
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
	sig.Extension = ext
	lastCandle := in.Candles[len(in.Candles)-1]
	sig.Breakdown = scoreBreakdown(sig, avgVol, lastVol, lastCandle.Open, lastCandle.Close)
	sig.Score = sig.Breakdown.Total()
	sig.Reasons = buildReasons(sig, avgVol, lastVol, opts.MinRR)

	return sig, nil
}

func analyzeBreakout(in Input, opts Options) (*BreakoutSignal, error) {
	if len(in.Candles) == 0 {
		return nil, errNoCandles
	}

	closes := extractCloses(in.Candles)
	highs := extractHighs(in.Candles)
	lows := extractLows(in.Candles)

	emas := analysis.ComputeEMAs(closes)
	price := closes[len(closes)-1]
	trend := deriveTrend(price, emas)
	if !isConstructiveBreakoutTrend(price, emas, trend) {
		return nil, fmt.Errorf("trend is %s, not constructive for breakout", trend)
	}

	zones := analysis.FindZones(highs, lows, opts.ZoneOpts)
	support, resistance, err := nearestZones(price, zones)
	if err != nil {
		return nil, err
	}

	distancePct := pctFrom(resistance.Low, price)
	if opts.MaxBreakoutDistancePct > 0 && distancePct > opts.MaxBreakoutDistancePct {
		return nil, fmt.Errorf("resistance %.2f is %.2f%% above price %.2f, max %.1f%%",
			resistance.Low, distancePct, price, opts.MaxBreakoutDistancePct)
	}

	ext := extensionDiagnostics(price, closes, emas, support)
	if err := validateBreakoutExtension(ext, opts); err != nil {
		return nil, err
	}

	avgVol, lastVol := volumeStats(in.Candles, opts.VolumeWindow)
	if opts.MinAvgVolume > 0 && avgVol > 0 && avgVol < float64(opts.MinAvgVolume) {
		return nil, fmt.Errorf("avg daily volume %.0f below minimum %d", avgVol, opts.MinAvgVolume)
	}

	var volumeRatio float64
	if avgVol > 0 {
		volumeRatio = lastVol / avgVol
	}

	sig := &BreakoutSignal{
		Symbol:                  in.Symbol,
		Price:                   price,
		Trend:                   trend,
		EMA:                     emas,
		Support:                 support,
		Resistance:              resistance,
		DistanceToResistancePct: distancePct,
		BreakoutPrice:           resistance.High,
		Extension:               ext,
		Volume: VolumeConfirmation{
			AvgVolume:   avgVol,
			LastVolume:  lastVol,
			VolumeRatio: volumeRatio,
		},
	}
	sig.Score = breakoutScore(sig, opts)
	sig.Reasons = buildBreakoutReasons(sig)
	return sig, nil
}

func isConstructiveBreakoutTrend(price float64, emas analysis.EMAResult, trend Trend) bool {
	if trend == TrendBullish {
		return true
	}
	return price > emas.EMA50 && emas.EMA50 > 0 && emas.EMA200 > 0 && emas.EMA50 >= emas.EMA200
}

func validateBreakoutExtension(ext Extension, opts Options) error {
	var reasons []string
	if opts.MaxEMA10ExtensionPct > 0 && ext.FromEMA10Pct > opts.MaxEMA10ExtensionPct {
		reasons = append(reasons, fmt.Sprintf("EMA10 %.1f%% > max %.1f%%", ext.FromEMA10Pct, opts.MaxEMA10ExtensionPct))
	}
	if opts.MaxEMA50ExtensionPct > 0 && ext.FromEMA50Pct > opts.MaxEMA50ExtensionPct {
		reasons = append(reasons, fmt.Sprintf("EMA50 %.1f%% > max %.1f%%", ext.FromEMA50Pct, opts.MaxEMA50ExtensionPct))
	}
	if opts.MaxMove10DPct > 0 && ext.HasMove10D && ext.Move10DPct > opts.MaxMove10DPct {
		reasons = append(reasons, fmt.Sprintf("10D move %.1f%% > max %.1f%%", ext.Move10DPct, opts.MaxMove10DPct))
	}
	if len(reasons) > 0 {
		return fmt.Errorf("breakout watch extended after recent rally: %s", strings.Join(reasons, "; "))
	}
	return nil
}

func breakoutScore(sig *BreakoutSignal, opts Options) float64 {
	maxDist := opts.MaxBreakoutDistancePct
	if maxDist <= 0 {
		maxDist = 3.0
	}
	proximity := 1 - sig.DistanceToResistancePct/maxDist
	if proximity < 0 {
		proximity = 0
	}
	if proximity > 1 {
		proximity = 1
	}

	touches := sig.Resistance.Touches
	if touches > 4 {
		touches = 4
	}

	trend := 10.0
	if sig.Trend == TrendBullish {
		trend = 20.0
	}

	volume := 0.0
	switch {
	case sig.Volume.VolumeRatio >= 1.5:
		volume = 10.0
	case sig.Volume.VolumeRatio >= 1.0:
		volume = 5.0
	}

	return proximity*40.0 + float64(touches)/4.0*30.0 + trend + volume
}

func buildBreakoutReasons(sig *BreakoutSignal) []string {
	reasons := []string{
		fmt.Sprintf("Price is %.2f%% below resistance zone %.2f–%.2f",
			sig.DistanceToResistancePct, sig.Resistance.Low, sig.Resistance.High),
		fmt.Sprintf("Resistance zone touched %d times", sig.Resistance.Touches),
		fmt.Sprintf("Breakout confirmation above %.2f", sig.BreakoutPrice),
		fmt.Sprintf("Extension: EMA10 %.1f%%, EMA50 %.1f%%, 10D %.1f%%",
			sig.Extension.FromEMA10Pct, sig.Extension.FromEMA50Pct, sig.Extension.Move10DPct),
	}
	if sig.Volume.AvgVolume > 0 {
		reasons = append(reasons, fmt.Sprintf("Volume %.2fx rolling average", sig.Volume.VolumeRatio))
	}
	return reasons
}

func validateExtension(ext Extension, opts Options) error {
	var reasons []string
	if opts.MaxEMA10ExtensionPct > 0 && ext.FromEMA10Pct > opts.MaxEMA10ExtensionPct {
		reasons = append(reasons, fmt.Sprintf("EMA10 %.1f%% > max %.1f%%", ext.FromEMA10Pct, opts.MaxEMA10ExtensionPct))
	}
	if opts.MaxEMA50ExtensionPct > 0 && ext.FromEMA50Pct > opts.MaxEMA50ExtensionPct {
		reasons = append(reasons, fmt.Sprintf("EMA50 %.1f%% > max %.1f%%", ext.FromEMA50Pct, opts.MaxEMA50ExtensionPct))
	}
	if opts.MaxSupportExtensionPct > 0 && ext.FromSupportHighPct > opts.MaxSupportExtensionPct {
		reasons = append(reasons, fmt.Sprintf("support %.1f%% > max %.1f%%", ext.FromSupportHighPct, opts.MaxSupportExtensionPct))
	}
	if opts.MaxMove10DPct > 0 && ext.HasMove10D && ext.Move10DPct > opts.MaxMove10DPct {
		reasons = append(reasons, fmt.Sprintf("10D move %.1f%% > max %.1f%%", ext.Move10DPct, opts.MaxMove10DPct))
	}
	if len(reasons) > 0 {
		return fmt.Errorf("setup extended after recent rally: %s", strings.Join(reasons, "; "))
	}
	return nil
}

// nearestZones returns the highest support zone below price and the lowest
// resistance zone above price.
func nearestZones(price float64, zones analysis.ZoneResult) (support, resistance analysis.Zone, err error) {
	support, foundSupport := highestSupportBelow(price, zones.Support)
	resistance, foundResistance := lowestResistanceAbove(price, zones.Resistance)

	if !foundSupport {
		return analysis.Zone{}, analysis.Zone{}, fmt.Errorf("no support zone below price %.2f", price)
	}
	if !foundResistance {
		return analysis.Zone{}, analysis.Zone{}, fmt.Errorf("no resistance zone above price %.2f", price)
	}
	return support, resistance, nil
}

// highestSupportBelow returns the support zone whose High is below price and
// whose Mid is the highest among all qualifying zones.
func highestSupportBelow(price float64, zones []analysis.Zone) (best analysis.Zone, found bool) {
	for _, z := range zones {
		if z.High < price && (!found || z.Mid > best.Mid) {
			best = z
			found = true
		}
	}
	return best, found
}

// lowestResistanceAbove returns the resistance zone whose Low is above price
// and whose Mid is the lowest among all qualifying zones.
func lowestResistanceAbove(price float64, zones []analysis.Zone) (best analysis.Zone, found bool) {
	for _, z := range zones {
		if z.Low > price && (!found || z.Mid < best.Mid) {
			best = z
			found = true
		}
	}
	return best, found
}

func extensionDiagnostics(price float64, closes []float64, emas analysis.EMAResult, support analysis.Zone) Extension {
	ext := Extension{
		FromEMA10Pct:       pctFrom(price, emas.EMA10),
		FromEMA50Pct:       pctFrom(price, emas.EMA50),
		FromSupportHighPct: pctFrom(price, support.High),
	}

	const lookback = 10
	if len(closes) > lookback {
		base := closes[len(closes)-1-lookback]
		ext.Move10DPct = pctFrom(price, base)
		ext.HasMove10D = base > 0
	}

	return ext
}

func pctFrom(price, base float64) float64 {
	if base <= 0 {
		return 0
	}
	return (price - base) / base * 100
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
	recent := history[start:]
	var sum float64
	for _, c := range recent {
		sum += float64(c.Volume)
	}
	avg = sum / float64(len(recent))
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
