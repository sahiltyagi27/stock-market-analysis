package crossover

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

// Options controls scanner behaviour.
type Options struct {
	// MaxCrossoverAge is the maximum number of candles since the crossover.
	// A crossover at age 0 happened on the most recent candle; age 2 means
	// two candles ago.  Default: 2 (last 3 candles total).
	MaxCrossoverAge int

	// MinRR is the minimum risk/reward required for a signal to be emitted.
	// Default: 1.5.
	MinRR float64

	// VolumeWindow is the number of candles before the crossover used to
	// compute the rolling average volume.  Default: 20.
	VolumeWindow int

	// MinCandles is the minimum candle count required before any analysis.
	// EMA21 needs at least 22 candles to be seeded; in practice 50 is safer.
	// Default: 50.
	MinCandles int

	// MinCurrentVolMultiple requires today's candle volume to be at least this
	// many times the rolling average of the previous CurrentVolWindow candles.
	// A fresh crossover confirmed by a high-volume candle is a much stronger
	// signal than one occurring on below-average activity.
	// Default: 0 (disabled).  Typical value: 3.0.
	MinCurrentVolMultiple float64

	// CurrentVolWindow is the lookback (in candles before today) used to
	// compute the rolling average for the MinCurrentVolMultiple check.
	// Default: 10.
	CurrentVolWindow int

	// MinTargetPct is the minimum distance (as a percentage of entry price)
	// the resistance-zone target must sit above entry. Resistance zones closer
	// than this are skipped and the next zone up is considered instead; if no
	// zone is far enough, the signal is rejected. This prevents trivial targets
	// — e.g. a resistance 0.8% above entry that "wins" on noise.
	// Default: 4.0.  Set to < 0 to disable.
	MinTargetPct float64

	// ZoneOpts are forwarded to analysis.FindZones for resistance detection.
	// MinResistanceTouches defaults to 1 (any single-touch zone qualifies as
	// a candidate target — crossover plays are momentum-driven, not zone-driven).
	ZoneOpts analysis.ZoneOptions
}

func (o Options) withDefaults() Options {
	out := o
	if out.MaxCrossoverAge < 0 {
		out.MaxCrossoverAge = 0
	}
	// 0 is a valid explicit value (today only); skip defaulting.
	// Callers who want "last 3 candles" must pass 2 explicitly or leave it for
	// the CLI to set via flag.  withDefaults() does not override a valid 0.
	// We *do* override the common case of an unset Options literal by checking
	// whether all three key fields are zero:
	if out.MaxCrossoverAge == 0 && out.MinRR == 0 && out.VolumeWindow == 0 {
		out.MaxCrossoverAge = 2
	}
	// 0 = apply default; negative = caller explicitly disabled the filter.
	if out.MinRR == 0 {
		out.MinRR = 1.5
	}
	if out.VolumeWindow <= 0 {
		out.VolumeWindow = 20
	}
	if out.MinCandles <= 0 {
		out.MinCandles = 50
	}
	// For crossover plays the target is simply the next resistance encountered —
	// a single-touch zone is sufficient.  Callers can override to 2.
	if out.ZoneOpts.MinResistanceTouches <= 0 {
		out.ZoneOpts.MinResistanceTouches = 1
	}
	// CurrentVolWindow: 0 triggers the default of 10.
	// MinCurrentVolMultiple: 0 means disabled — no default applied.
	if out.CurrentVolWindow <= 0 {
		out.CurrentVolWindow = 10
	}
	// MinTargetPct: 0 triggers the default of 4%; negative disables the filter.
	if out.MinTargetPct == 0 {
		out.MinTargetPct = 4.0
	}
	return out
}

// Scan runs the crossover pipeline for every input and returns signals sorted
// by Score descending (ties broken by CrossoverAge ascending — fresher first).
// Filtered inputs are collected in the returned error map.
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
		return signals[i].CrossoverAge < signals[j].CrossoverAge
	})

	return signals, errs
}

// analyzeOne runs the full crossover pipeline for a single symbol.
func analyzeOne(in Input, opts Options) (*Signal, error) {
	if len(in.Candles) == 0 {
		return nil, errors.New("no candles")
	}
	if len(in.Candles) < opts.MinCandles {
		return nil, fmt.Errorf("only %d candles (need %d)", len(in.Candles), opts.MinCandles)
	}

	closes := extractCloses(in.Candles)
	highs := extractHighs(in.Candles)
	lows := extractLows(in.Candles)
	n := len(closes)

	// Compute full EMA history for both periods.
	ema7, err := analysis.EMA(closes, 7)
	if err != nil {
		return nil, fmt.Errorf("EMA7: %w", err)
	}
	ema21, err := analysis.EMA(closes, 21)
	if err != nil {
		return nil, fmt.Errorf("EMA21: %w", err)
	}

	// EMA7 must currently be above EMA21 — the crossover must be holding.
	if ema7[n-1] <= ema21[n-1] {
		return nil, fmt.Errorf(
			"EMA7 (%.2f) not above EMA21 (%.2f) — no active crossover",
			ema7[n-1], ema21[n-1],
		)
	}

	// Locate the most recent crossover within the allowed age window.
	xIdx := findCrossoverIdx(ema7, ema21, n, opts.MaxCrossoverAge)
	if xIdx < 0 {
		return nil, fmt.Errorf(
			"no EMA7×EMA21 crossover within the last %d candles",
			opts.MaxCrossoverAge+1,
		)
	}

	crossoverAge := (n - 1) - xIdx
	price := closes[n-1]

	// SL: the Low of the candle immediately before the crossover candle.
	// This candle shows the last bearish session before momentum turned.
	if xIdx == 0 {
		return nil, errors.New("crossover on first candle — no prior candle for SL")
	}
	sl := in.Candles[xIdx-1].Low

	if price <= sl {
		return nil, fmt.Errorf(
			"current price %.2f has already fallen to/below SL %.2f",
			price, sl,
		)
	}

	risk := price - sl

	// Target: nearest resistance zone at least MinTargetPct above price.
	// Zones closer than the minimum are skipped in favour of the next one up —
	// a trivially close resistance produces a meaningless ~1% target.
	zones := analysis.FindZones(highs, lows, opts.ZoneOpts)
	minTarget := 0.0
	if opts.MinTargetPct > 0 {
		minTarget = price * (1 + opts.MinTargetPct/100)
	}
	target, targetFound := nearestResistanceAtLeast(price, minTarget, zones.Resistance)
	if !targetFound && opts.MinTargetPct > 0 {
		return nil, fmt.Errorf(
			"no resistance zone at least %.1f%% above entry %.2f (nearest target would be too close)",
			opts.MinTargetPct, price,
		)
	}

	var rr float64
	if targetFound && risk > 0 {
		rr = (target - price) / risk
	}
	// Apply MinRR whenever it is positive — a missing target gives rr=0 which
	// also fails any positive MinRR threshold, so no special-case needed.
	if opts.MinRR > 0 && rr < opts.MinRR {
		return nil, fmt.Errorf(
			"R/R %.2f below minimum %.2f (entry %.2f, SL %.2f, target %.2f)",
			rr, opts.MinRR, price, sl, target,
		)
	}

	// Current-day volume filter: today's candle must be at least
	// MinCurrentVolMultiple × the rolling average of the previous
	// CurrentVolWindow candles.  A high-volume crossover candle confirms that
	// real buying interest is driving the EMA move, not just noise.
	if opts.MinCurrentVolMultiple > 0 {
		todayVol := float64(in.Candles[n-1].Volume)
		start := n - 1 - opts.CurrentVolWindow
		if start < 0 {
			start = 0
		}
		var sum float64
		count := 0
		for k := start; k < n-1; k++ {
			sum += float64(in.Candles[k].Volume)
			count++
		}
		if count > 0 && sum > 0 {
			avg := sum / float64(count)
			if todayVol < opts.MinCurrentVolMultiple*avg {
				return nil, fmt.Errorf(
					"today's volume %.0f is only %.2fx the %d-day avg %.0f (need %.1fx)",
					todayVol, todayVol/avg, opts.CurrentVolWindow, avg,
					opts.MinCurrentVolMultiple,
				)
			}
		}
	}

	vol := computeVolume(in.Candles, xIdx, opts.VolumeWindow)

	sig := &Signal{
		Symbol:        in.Symbol,
		Price:         price,
		CrossoverDate: in.Candles[xIdx].Timestamp,
		CrossoverAge:  crossoverAge,
		EMA7:          ema7[n-1],
		EMA21:         ema21[n-1],
		Entry:         price,
		SL:            sl,
		Target:        target,
		RiskReward:    rr,
		Volume:        vol,
	}
	sig.Score = score(sig)
	sig.Reasons = buildReasons(sig)

	return sig, nil
}

// findCrossoverIdx returns the index of the most recent candle where
// EMA7 crossed above EMA21, searching back at most maxAge candles from the
// end of the slice.  Returns -1 when no such crossover is found.
//
// A crossover at index i requires:
//   - ema7[i] > ema21[i]     (above today)
//   - ema7[i-1] <= ema21[i-1] (at or below yesterday)
//   - both i and i-1 have valid (non-zero) EMA values
func findCrossoverIdx(ema7, ema21 []float64, n, maxAge int) int {
	// Minimum valid index: EMA21 seeds at index 20, so both EMA values at
	// index i-1 are reliable only when i >= 21.
	const minValid = 21
	from := n - 1
	to := n - 1 - maxAge
	if to < minValid {
		to = minValid
	}
	for i := from; i >= to; i-- {
		if ema7[i] > 0 && ema21[i] > 0 &&
			ema7[i-1] > 0 && ema21[i-1] > 0 &&
			ema7[i] > ema21[i] &&
			ema7[i-1] <= ema21[i-1] {
			return i
		}
	}
	return -1
}

// nearestResistanceAtLeast returns the midpoint of the resistance zone with
// the lowest Mid that is both above price and at least minTarget. Passing
// minTarget = 0 makes it behave like "nearest resistance above price".
// Returns (0, false) when no qualifying zone exists.
//
// A zone qualifies when its Mid (the target level) is ≥ minTarget — this lets
// the scanner skip a too-close resistance and pick the next meaningful zone up.
func nearestResistanceAtLeast(price, minTarget float64, zones []analysis.Zone) (float64, bool) {
	var best analysis.Zone
	found := false
	for _, z := range zones {
		if z.Low > price && z.Mid >= minTarget && (!found || z.Mid < best.Mid) {
			best = z
			found = true
		}
	}
	if !found {
		return 0, false
	}
	return best.Mid, true
}

// computeVolume calculates the rolling average volume over the window of
// candles before the crossover and compares it with the crossover candle's
// own volume.
func computeVolume(candles []models.Candle, xIdx, window int) VolumeStats {
	crossVol := float64(candles[xIdx].Volume)

	start := xIdx - window
	if start < 0 {
		start = 0
	}
	var sum float64
	count := 0
	for i := start; i < xIdx; i++ {
		sum += float64(candles[i].Volume)
		count++
	}
	if count == 0 {
		return VolumeStats{CrossVolume: crossVol}
	}
	avg := sum / float64(count)
	var ratio float64
	if avg > 0 {
		ratio = crossVol / avg
	}
	return VolumeStats{AvgVolume: avg, CrossVolume: crossVol, Ratio: ratio}
}

// score computes a 0–100 score for a crossover signal.
//
//	Freshness  40 pts  (age 0→40, 1→30, 2→20)
//	R/R        30 pts  (≥3.0→30, ≥2.0→22, ≥1.5→12)
//	Volume     20 pts  (≥2.0x→20, ≥1.5x→15, ≥1.0x→10, no data→5)
//	EMA gap    10 pts  (EMA7−EMA21 as % of price: ≥1%→10, ≥0.5%→5)
func score(sig *Signal) float64 {
	var s float64

	switch sig.CrossoverAge {
	case 0:
		s += 40
	case 1:
		s += 30
	default:
		s += 20
	}

	switch {
	case sig.RiskReward >= 3.0:
		s += 30
	case sig.RiskReward >= 2.0:
		s += 22
	case sig.RiskReward >= 1.5:
		s += 12
	}

	switch {
	case sig.Volume.AvgVolume == 0:
		s += 5 // no volume history — neutral
	case sig.Volume.Ratio >= 2.0:
		s += 20
	case sig.Volume.Ratio >= 1.5:
		s += 15
	case sig.Volume.Ratio >= 1.0:
		s += 10
	}

	if sig.Price > 0 && sig.EMA21 > 0 {
		gapPct := (sig.EMA7 - sig.EMA21) / sig.Price * 100
		switch {
		case gapPct >= 1.0:
			s += 10
		case gapPct >= 0.5:
			s += 5
		}
	}

	return s
}

func buildReasons(sig *Signal) []string {
	reasons := []string{
		fmt.Sprintf("EMA7 (%.2f) crossed above EMA21 (%.2f) %s",
			sig.EMA7, sig.EMA21, ageText(sig.CrossoverAge)),
		fmt.Sprintf("SL at previous candle low: %.2f (risk %.2f)",
			sig.SL, sig.Entry-sig.SL),
	}
	if sig.Target > 0 {
		reasons = append(reasons, fmt.Sprintf(
			"Nearest resistance target %.2f — R/R %.2f",
			sig.Target, sig.RiskReward))
	}
	if sig.Volume.AvgVolume > 0 {
		reasons = append(reasons, fmt.Sprintf(
			"Crossover candle volume %.2fx rolling average",
			sig.Volume.Ratio))
	}
	return reasons
}

func ageText(age int) string {
	switch age {
	case 0:
		return "today"
	case 1:
		return "1 candle ago"
	default:
		return fmt.Sprintf("%d candles ago", age)
	}
}

// ── OHLCV extraction helpers ──────────────────────────────────────────────────

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
