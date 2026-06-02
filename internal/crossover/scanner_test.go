package crossover

import (
	"strings"
	"testing"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// ── Fixtures ──────────────────────────────────────────────────────────────────

func makeCandle(ts time.Time, o, h, l, c float64, vol int64) models.Candle {
	return models.Candle{Timestamp: ts, Open: o, High: h, Low: l, Close: c, Volume: vol}
}

// makeCrossoverCandles builds a series that reliably produces an EMA7×EMA21
// crossover near the tail:
//
//	Phase 1 (40 candles): flat at basePrice       → EMA7 ≈ EMA21 ≈ base
//	Phase 2  (8 candles): declining to base×0.80  → EMA7 < EMA21
//	Phase 3  (6 candles): surging to base×1.30    → EMA7 crosses above EMA21
//
// tailExtra appends that many flat candles at base×1.25 after the surge, so
// tests for "stale crossover" can push the event deep into the past.
func makeCrossoverCandles(basePrice float64, baseVol int64, tailExtra int) []models.Candle {
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	adv := func() time.Time { t := ts; ts = ts.Add(24 * time.Hour); return t }
	flat := func(p float64) models.Candle {
		return makeCandle(adv(), p, p*1.005, p*0.995, p, baseVol)
	}

	var cc []models.Candle

	// Phase 1: 40 flat candles.
	for range 40 {
		cc = append(cc, flat(basePrice))
	}

	// Phase 2: 8 declining candles.
	for i := range 8 {
		p := basePrice * (1.0 - float64(i+1)*0.025)
		cc = append(cc, flat(p))
	}

	// Phase 3: 6 surging candles — doubled volume for volume tests.
	surge := basePrice * 1.20
	for i := range 6 {
		p := surge + float64(i)*basePrice*0.02
		cc = append(cc, makeCandle(adv(), p*0.998, p*1.01, p*0.985, p, baseVol*2))
	}

	// Optional tail.
	for range tailExtra {
		cc = append(cc, flat(basePrice*1.25))
	}

	return cc
}

// lenientOpts returns Options with wide filters so individual tests can assert
// on exactly one condition at a time.
func lenientOpts() Options {
	return Options{
		MaxCrossoverAge: 99,
		MinRR:           -1, // negative = disabled (0 would trigger the 1.5 default)
		MinCandles:      30,
		VolumeWindow:    10,
		MinTargetPct:    -1, // disabled; dedicated tests verify it separately
		ZoneOpts:        analysis.ZoneOptions{MinResistanceTouches: 1},
	}
}

// ── findCrossoverIdx ──────────────────────────────────────────────────────────

func TestFindCrossoverIdx_DetectsCrossover(t *testing.T) {
	// Build two parallel slices with a known crossover at index 25.
	n := 30
	ema7 := make([]float64, n)
	ema21 := make([]float64, n)
	for i := range n {
		ema7[i] = 100
		ema21[i] = 100
	}
	// Indices 0-24: EMA7 below EMA21.
	for i := range 25 {
		ema7[i] = 98
		ema21[i] = 102
	}
	// Index 25+: EMA7 above EMA21 — crossover at 25.
	for i := 25; i < n; i++ {
		ema7[i] = 105
		ema21[i] = 101
	}
	idx := findCrossoverIdx(ema7, ema21, n, n)
	if idx != 25 {
		t.Fatalf("expected crossover at 25, got %d", idx)
	}
}

func TestFindCrossoverIdx_NoCrossover_WhenAlwaysAbove(t *testing.T) {
	n := 30
	ema7 := make([]float64, n)
	ema21 := make([]float64, n)
	for i := range n {
		ema7[i] = 110  // always above — never crossed
		ema21[i] = 100
	}
	idx := findCrossoverIdx(ema7, ema21, n, n)
	if idx != -1 {
		t.Fatalf("expected no crossover (-1), got %d", idx)
	}
}

func TestFindCrossoverIdx_RespectsMaxAge(t *testing.T) {
	// Crossover at n-10; maxAge=2 must not find it.
	n := 35
	ema7 := make([]float64, n)
	ema21 := make([]float64, n)
	for i := range n {
		if i < n-10 {
			ema7[i] = 98; ema21[i] = 102
		} else {
			ema7[i] = 105; ema21[i] = 101
		}
	}
	idx := findCrossoverIdx(ema7, ema21, n, 2)
	if idx != -1 {
		t.Fatalf("crossover age ~10 should be outside maxAge=2, got idx=%d", idx)
	}
}

// ── nearestResistanceAtLeast ──────────────────────────────────────────────────

func TestNearestResistanceAtLeast_SkipsTooClose(t *testing.T) {
	zones := []analysis.Zone{
		{Low: 101, Mid: 101.5, High: 102}, // ~1.5% above price 100 — too close
		{Low: 108, Mid: 109, High: 110},   // ~9% above — the real target
	}
	// minTarget = 104 (4% above 100) should skip the 101.5 zone, pick 109.
	target, found := nearestResistanceAtLeast(100, 104, zones)
	if !found {
		t.Fatal("expected a qualifying zone above minTarget")
	}
	if target != 109 {
		t.Fatalf("expected target 109 (skipping too-close 101.5), got %.2f", target)
	}
}

func TestNearestResistanceAtLeast_NoneQualify(t *testing.T) {
	zones := []analysis.Zone{
		{Low: 101, Mid: 101.5, High: 102}, // only a too-close zone exists
	}
	_, found := nearestResistanceAtLeast(100, 104, zones)
	if found {
		t.Fatal("expected no qualifying zone when all are below minTarget")
	}
}

func TestNearestResistanceAtLeast_ZeroMinBehavesAsNearest(t *testing.T) {
	zones := []analysis.Zone{
		{Low: 108, Mid: 109, High: 110},
		{Low: 101, Mid: 101.5, High: 102}, // nearest above price
	}
	target, found := nearestResistanceAtLeast(100, 0, zones)
	if !found || target != 101.5 {
		t.Fatalf("expected nearest target 101.5 with minTarget=0, got %.2f (found=%v)", target, found)
	}
}

// ── Scan — high-level ─────────────────────────────────────────────────────────

func TestScan_DetectsFreshCrossover(t *testing.T) {
	cc := makeCrossoverCandles(100, 500_000, 0)
	opts := lenientOpts()
	opts.MaxCrossoverAge = 8 // fixture crossover is ~4-5 candles from end
	signals, _ := Scan([]Input{{Symbol: "TEST", Candles: cc}}, opts)
	if len(signals) == 0 {
		t.Fatal("expected a crossover signal, got none")
	}
	sig := signals[0]
	if sig.EMA7 <= sig.EMA21 {
		t.Errorf("current EMA7 (%.4f) must be above EMA21 (%.4f)", sig.EMA7, sig.EMA21)
	}
	if sig.CrossoverAge < 0 || sig.CrossoverAge > 8 {
		t.Errorf("CrossoverAge=%d outside [0, 8]", sig.CrossoverAge)
	}
}

func TestScan_RejectsWhenCrossoverTooOld(t *testing.T) {
	// tailExtra=15 pushes the crossover >15 candles into the past.
	cc := makeCrossoverCandles(100, 500_000, 15)
	opts := lenientOpts()
	opts.MaxCrossoverAge = 2
	_, errs := Scan([]Input{{Symbol: "TEST", Candles: cc}}, opts)
	if _, rejected := errs["TEST"]; !rejected {
		t.Fatal("expected stale crossover to be rejected")
	}
}

func TestScan_RejectsWhenEMA7FellBack(t *testing.T) {
	// After the surge, add candles at a very low price to pull EMA7 back
	// below EMA21.
	cc := makeCrossoverCandles(100, 500_000, 0)
	ts := cc[len(cc)-1].Timestamp.Add(24 * time.Hour)
	for range 15 {
		cc = append(cc, makeCandle(ts, 50, 51, 49, 50, 500_000))
		ts = ts.Add(24 * time.Hour)
	}
	opts := lenientOpts()
	_, errs := Scan([]Input{{Symbol: "TEST", Candles: cc}}, opts)
	if _, rejected := errs["TEST"]; !rejected {
		t.Fatal("expected EMA7-below-EMA21 to be rejected")
	}
}

func TestScan_SLIsPreviousCandleLow(t *testing.T) {
	cc := makeCrossoverCandles(100, 500_000, 0)
	opts := lenientOpts()
	opts.MaxCrossoverAge = 8
	signals, _ := Scan([]Input{{Symbol: "TEST", Candles: cc}}, opts)
	if len(signals) == 0 {
		t.Skip("no signal — cannot verify SL")
	}
	sig := signals[0]

	// Recompute to find the exact crossover index and expected SL.
	closes := extractCloses(cc)
	ema7s, _ := analysis.EMA(closes, 7)
	ema21s, _ := analysis.EMA(closes, 21)
	xIdx := findCrossoverIdx(ema7s, ema21s, len(closes), 8)
	if xIdx < 1 {
		t.Skip("crossover index < 1 — cannot verify")
	}
	wantSL := cc[xIdx-1].Low
	if sig.SL != wantSL {
		t.Errorf("SL = %.4f, want previous candle low %.4f", sig.SL, wantSL)
	}
}

func TestScan_ScoreInRange(t *testing.T) {
	cc := makeCrossoverCandles(100, 500_000, 0)
	opts := lenientOpts()
	opts.MaxCrossoverAge = 8
	signals, _ := Scan([]Input{{Symbol: "TEST", Candles: cc}}, opts)
	if len(signals) == 0 {
		t.Skip("no signal")
	}
	s := signals[0].Score
	if s < 0 || s > 100 {
		t.Errorf("score %.2f out of [0, 100]", s)
	}
}

func TestScan_MinRRFilters(t *testing.T) {
	cc := makeCrossoverCandles(100, 500_000, 0)
	opts := lenientOpts()
	opts.MaxCrossoverAge = 8
	opts.MinRR = 100 // impossible
	_, errs := Scan([]Input{{Symbol: "TEST", Candles: cc}}, opts)
	if _, rejected := errs["TEST"]; !rejected {
		t.Fatal("expected MinRR=100 to reject all signals")
	}
}

func TestScan_SortedByScoreDesc(t *testing.T) {
	c1 := makeCrossoverCandles(100, 2_000_000, 0) // higher volume → higher score
	c2 := makeCrossoverCandles(100, 10_000, 0)    // lower volume
	opts := lenientOpts()
	opts.MaxCrossoverAge = 8
	signals, _ := Scan([]Input{
		{Symbol: "HIGH", Candles: c1},
		{Symbol: "LOW", Candles: c2},
	}, opts)
	for i := 1; i < len(signals); i++ {
		if signals[i].Score > signals[i-1].Score {
			t.Errorf("not sorted: [%d].Score=%.2f > [%d].Score=%.2f",
				i, signals[i].Score, i-1, signals[i-1].Score)
		}
	}
}

// ── Volume filter (MinCurrentVolMultiple) ─────────────────────────────────────

// TestScan_VolumeFilter_Rejects_LowVolume verifies that a signal is rejected
// when today's candle volume is below the required multiple of the 10-day avg.
func TestScan_VolumeFilter_Rejects_LowVolume(t *testing.T) {
	cc := makeCrossoverCandles(100, 500_000, 0)
	// Force the last candle to have very low volume (far below 3× avg).
	cc[len(cc)-1].Volume = 1
	opts := lenientOpts()
	opts.MaxCrossoverAge = 8
	opts.MinCurrentVolMultiple = 3.0
	opts.CurrentVolWindow = 10
	_, errs := Scan([]Input{{Symbol: "TEST", Candles: cc}}, opts)
	if _, rejected := errs["TEST"]; !rejected {
		t.Fatal("expected low-volume candle to be rejected by MinCurrentVolMultiple filter")
	}
}

// TestScan_VolumeFilter_Passes_HighVolume verifies that a signal passes when
// today's volume is well above the required multiple.
func TestScan_VolumeFilter_Passes_HighVolume(t *testing.T) {
	cc := makeCrossoverCandles(100, 500_000, 0)
	// Spike last candle volume to 5× baseline (well above 3× avg of 500k).
	cc[len(cc)-1].Volume = 2_500_000
	opts := lenientOpts()
	opts.MaxCrossoverAge = 8
	opts.MinCurrentVolMultiple = 3.0
	opts.CurrentVolWindow = 10
	signals, _ := Scan([]Input{{Symbol: "TEST", Candles: cc}}, opts)
	if len(signals) == 0 {
		t.Fatal("expected signal when today's volume is 5× the rolling average")
	}
}

// TestScan_VolumeFilter_DisabledWhenZero confirms MinCurrentVolMultiple=0
// skips the filter entirely.
func TestScan_VolumeFilter_DisabledWhenZero(t *testing.T) {
	cc := makeCrossoverCandles(100, 500_000, 0)
	cc[len(cc)-1].Volume = 1 // tiny volume — would fail if filter were active
	opts := lenientOpts()
	opts.MaxCrossoverAge = 8
	opts.MinCurrentVolMultiple = 0 // disabled
	// We can't assert a signal is produced (it might fail for other reasons),
	// but it should NOT be rejected specifically for volume.
	_, errs := Scan([]Input{{Symbol: "TEST", Candles: cc}}, opts)
	if err, rejected := errs["TEST"]; rejected {
		if strings.Contains(err.Error(), "volume") {
			t.Fatalf("volume filter should be disabled (MinCurrentVolMultiple=0), but got: %v", err)
		}
	}
}

func TestScan_InsufficientCandlesRejected(t *testing.T) {
	// Only 10 candles — below MinCandles=30.
	var cc []models.Candle
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for range 10 {
		cc = append(cc, makeCandle(ts, 100, 101, 99, 100, 100_000))
		ts = ts.Add(24 * time.Hour)
	}
	opts := lenientOpts()
	_, errs := Scan([]Input{{Symbol: "SHORT", Candles: cc}}, opts)
	if _, rejected := errs["SHORT"]; !rejected {
		t.Fatal("expected insufficient candles to be rejected")
	}
}

// ── MinTargetPct filter ───────────────────────────────────────────────────────

// TestScan_MinTargetPct_RejectsWhenNoFarEnoughResistance verifies the signal is
// dropped when every resistance zone sits within MinTargetPct of entry.
func TestScan_MinTargetPct_RejectsWhenNoFarEnoughResistance(t *testing.T) {
	cc := makeCrossoverCandles(100, 500_000, 0)
	opts := lenientOpts()
	opts.MaxCrossoverAge = 8
	// Demand a target 50% above entry — the fixture's resistance is far closer,
	// so no zone qualifies and the signal must be rejected.
	opts.MinTargetPct = 50.0
	_, errs := Scan([]Input{{Symbol: "TEST", Candles: cc}}, opts)
	if err, rejected := errs["TEST"]; !rejected {
		t.Fatal("expected rejection when no resistance is far enough for MinTargetPct")
	} else if !strings.Contains(err.Error(), "at least") {
		t.Fatalf("expected MinTargetPct rejection reason, got: %v", err)
	}
}
