package scanner_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/scanner"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// containsReason checks whether any entry in reasons contains the given substring.
func containsReason(reasons []string, substr string) bool {
	for _, r := range reasons {
		if strings.Contains(r, substr) {
			return true
		}
	}
	return false
}

func floatsClose(a, b float64) bool {
	return math.Abs(a-b) < 1e-6
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeTrendingCandles builds a synthetic OHLCV series that passes the bullish
// scanner filter (price > EMA50 > EMA200, R/R ≥ 2, clear support + resistance zones).
//
// Layout (all prices as multiples of basePrice, bp=100 for illustration):
//
//	Phase 1 – 210 rising candles: bp → 2bp          (seeds EMA200 ≈ 150, EMA50 ≈ 188)
//	Phase 2 – 2 resistance spikes to 2.6bp           (local-max zone ≈ 258–261)
//	Phase 3 – 2 V-dips with bottoms at 1.88–1.89bp  (local-min zone, merged by clusterer)
//	Current  – price at 2bp                          (between support ~1.88bp and resistance ~2.6bp)
//
// R/R for the long: entry=2bp, SL≈1.87bp, target≈2.6bp  →  RR ≈ 4.7 (excellent).
func makeTrendingCandles(symbol string, basePrice float64, baseVolume int64) []models.Candle {
	var candles []models.Candle
	ts := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)

	push := func(close, high, low float64, vol int64) {
		candles = append(candles, models.Candle{
			Symbol: symbol, Timestamp: ts,
			Open: close, High: high, Low: low, Close: close, Volume: vol,
		})
		ts = ts.Add(24 * time.Hour)
	}
	reg := func(p float64) { push(p, p*1.005, p*0.995, baseVolume) }

	// Phase 1: linear rise bp → 2bp over 210 candles.
	for i := range 210 {
		p := basePrice * (1.0 + float64(i+1)/210.0)
		reg(p)
	}

	cur := basePrice * 2.0
	res := basePrice * 2.6 // resistance level

	// Phase 2: resistance zone — two spike candles with regular candles between.
	// Spike Highs (2.6bp and 2.61bp) are strictly above all neighbours' Highs (~2.01bp).
	for range 3 {
		reg(cur)
	}
	push(cur, res, cur*0.995, baseVolume) // spike 1
	for range 3 {
		reg(cur)
	}
	push(cur, res*1.004, cur*0.995, baseVolume) // spike 2
	for range 3 {
		reg(cur)
	}

	// Phase 3: two V-dips creating the support zone.
	// Each bottom candle has Low strictly below its four nearest neighbours.
	//   neighbours' Lows:  cur*0.945, cur*0.94  |  bottom  |  cur*0.94, cur*0.945
	bot1 := cur * 0.940 // Low of bottom candle 1
	bot2 := cur * 0.944 // Low of bottom candle 2 (within 2% → same zone)

	// V-dip 1
	reg(cur * 0.975)
	push(cur*0.96, cur*0.965, cur*0.945, baseVolume) // Low=cur*0.945
	push(cur*0.95, cur*0.955, cur*0.940, baseVolume) // Low=cur*0.940 → but this IS bot1; need neighbour lower check
	// bottom: Low = bot1 - small delta so it is strictly below cur*0.940 neighbour
	push(cur*0.945, cur*0.95, bot1-basePrice*0.01, baseVolume*2) // strict local min
	push(cur*0.95, cur*0.955, cur*0.940, baseVolume)
	push(cur*0.96, cur*0.965, cur*0.945, baseVolume)
	reg(cur * 0.975)
	for range 3 {
		reg(cur)
	}

	// V-dip 2
	reg(cur * 0.975)
	push(cur*0.96, cur*0.965, cur*0.945, baseVolume)
	push(cur*0.95, cur*0.955, cur*0.940, baseVolume)
	push(cur*0.945, cur*0.95, bot2-basePrice*0.01, baseVolume*2) // strict local min, near bot1
	push(cur*0.95, cur*0.955, cur*0.940, baseVolume)
	push(cur*0.96, cur*0.965, cur*0.945, baseVolume)
	reg(cur * 0.975)
	for range 3 {
		reg(cur)
	}

	// Current price back at cur.
	reg(cur)

	return candles
}

// makeBearishCandles produces a clearly downtrending series (price below both EMAs).
func makeBearishCandles(symbol string, basePrice float64) []models.Candle {
	var candles []models.Candle
	ts := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	price := basePrice * 2
	for range 280 {
		price -= basePrice * 0.003
		candles = append(candles, models.Candle{
			Symbol: symbol, Timestamp: ts,
			Open: price - 1, High: price + 1, Low: price - 2, Close: price, Volume: 100000,
		})
		ts = ts.Add(24 * time.Hour)
	}
	return candles
}

func makeBenchmarkCandlesLike(stock []models.Candle, start, end float64) []models.Candle {
	out := make([]models.Candle, len(stock))
	denom := float64(len(stock) - 1)
	for i, c := range stock {
		close := start
		if denom > 0 {
			close = start + (end-start)*float64(i)/denom
		}
		out[i] = models.Candle{
			Symbol:    "NIFTY50",
			Timestamp: c.Timestamp,
			Open:      close,
			High:      close + 1,
			Low:       close - 1,
			Close:     close,
			Volume:    0,
		}
	}
	return out
}

var defaultOpts = scanner.Options{
	MinRR:                  2.0,
	VolumeWindow:           20,
	MaxEMA10ExtensionPct:   -1,
	MaxEMA50ExtensionPct:   -1,
	MaxSupportExtensionPct: -1,
	MaxMove10DPct:          -1,
	// Disable the new quality filters for the synthetic test fixture so existing
	// tests remain focused on the specific behaviour they were written to verify.
	// Dedicated tests below verify each new filter in isolation.
	MaxRiskPct:         -1,
	MinRiskPct:         -1,
	AllowBearishCandle: true, // preserve penalty-only behaviour in existing tests
	EMA200SlopePeriod:  -1,
}

func withoutExtensionGuards(opts scanner.Options) scanner.Options {
	opts.MaxEMA10ExtensionPct = -1
	opts.MaxEMA50ExtensionPct = -1
	opts.MaxSupportExtensionPct = -1
	opts.MaxMove10DPct = -1
	// Also disable the new quality filters so tests built on the synthetic
	// fixture (which has intentionally wide zones) are not affected by them.
	opts.MaxRiskPct = -1
	opts.MinRiskPct = -1
	opts.AllowBearishCandle = true
	opts.EMA200SlopePeriod = -1
	return opts
}

// ---------------------------------------------------------------------------
// Scan — happy path
// ---------------------------------------------------------------------------

func TestScan_BullishSignalReturned(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Fatal("expected at least one signal for a bullish stock, got none")
	}
	if signals[0].Symbol != "AAPL" {
		t.Errorf("symbol = %q, want AAPL", signals[0].Symbol)
	}
}

func TestScan_RelativeStrengthPassesWhenStockOutperformsBenchmark(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	opts := defaultOpts
	opts.RelativeStrengthLookback = 20
	opts.MinRelativeStrengthPct = 0
	opts.BenchmarkSymbol = "NIFTY50"
	opts.BenchmarkCandles = makeBenchmarkCandlesLike(candles, 100, 101)

	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, opts)
	if len(signals) == 0 {
		t.Fatal("expected relative-strength leader to pass")
	}
	if signals[0].RelativeStrength.Lookback != 20 {
		t.Fatalf("relative strength lookback = %d, want 20", signals[0].RelativeStrength.Lookback)
	}
	if signals[0].RelativeStrength.OutperformancePct < 0 {
		t.Fatalf("outperformance = %.2f, want >= 0", signals[0].RelativeStrength.OutperformancePct)
	}
}

func TestScan_RelativeStrengthRejectsUnderperformer(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	opts := defaultOpts
	opts.RelativeStrengthLookback = 20
	opts.MinRelativeStrengthPct = 0
	opts.BenchmarkSymbol = "NIFTY50"
	opts.BenchmarkCandles = makeBenchmarkCandlesLike(candles, 100, 500)

	signals, errs := scanner.ScanWithErrors([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, opts)
	if len(signals) != 0 {
		t.Fatal("expected relative-strength laggard to be filtered")
	}
	if errs["AAPL"] == nil || !strings.Contains(errs["AAPL"].Error(), "relative strength") {
		t.Fatalf("expected relative strength rejection, got %v", errs["AAPL"])
	}
}

func TestScan_SectorStrengthPassesWhenSectorOutperformsBenchmark(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	opts := defaultOpts
	opts.SectorStrengthLookback = 20
	opts.MinSectorStrengthPct = 0
	opts.BenchmarkSymbol = "NIFTY50"
	opts.BenchmarkCandles = makeBenchmarkCandlesLike(candles, 100, 101)
	opts.SectorIndexBySymbol = map[string]string{"AAPL": "NIFTYIT"}
	opts.SectorIndexCandles = map[string][]models.Candle{
		"NIFTYIT": makeBenchmarkCandlesLike(candles, 100, 120),
	}

	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, opts)
	if len(signals) == 0 {
		t.Fatal("expected stock in strong sector to pass")
	}
	if signals[0].SectorStrength.SectorIndexSymbol != "NIFTYIT" {
		t.Fatalf("sector index = %q, want NIFTYIT", signals[0].SectorStrength.SectorIndexSymbol)
	}
	if signals[0].SectorStrength.OutperformancePct < 0 {
		t.Fatalf("sector outperformance = %.2f, want >= 0", signals[0].SectorStrength.OutperformancePct)
	}
}

func TestScan_SectorStrengthRejectsWeakSector(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	opts := defaultOpts
	opts.SectorStrengthLookback = 20
	opts.MinSectorStrengthPct = 0
	opts.BenchmarkSymbol = "NIFTY50"
	opts.BenchmarkCandles = makeBenchmarkCandlesLike(candles, 100, 150)
	opts.SectorIndexBySymbol = map[string]string{"AAPL": "NIFTYIT"}
	opts.SectorIndexCandles = map[string][]models.Candle{
		"NIFTYIT": makeBenchmarkCandlesLike(candles, 100, 101),
	}

	signals, errs := scanner.ScanWithErrors([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, opts)
	if len(signals) != 0 {
		t.Fatal("expected weak sector to be filtered")
	}
	if errs["AAPL"] == nil || !strings.Contains(errs["AAPL"].Error(), "sector strength") {
		t.Fatalf("expected sector strength rejection, got %v", errs["AAPL"])
	}
}

func TestScan_SectorStrengthStrictRejectsMissingSectorCandles(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	opts := defaultOpts
	opts.SectorStrengthLookback = 20
	opts.SectorStrengthStrict = true
	opts.BenchmarkSymbol = "NIFTY50"
	opts.BenchmarkCandles = makeBenchmarkCandlesLike(candles, 100, 101)
	opts.SectorIndexBySymbol = map[string]string{"AAPL": "NIFTYIT"}

	signals, errs := scanner.ScanWithErrors([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, opts)
	if len(signals) != 0 {
		t.Fatal("expected strict missing sector data to be filtered")
	}
	if errs["AAPL"] == nil || !strings.Contains(errs["AAPL"].Error(), "no candles for mapped sector index") {
		t.Fatalf("expected strict missing sector data rejection, got %v", errs["AAPL"])
	}
}

func TestScan_TrendIsBullish(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	if signals[0].Trend != scanner.TrendBullish {
		t.Errorf("trend = %q, want bullish", signals[0].Trend)
	}
}

func TestScan_PriceAboveBothEMAs(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	s := signals[0]
	if s.Price <= s.EMA.EMA50 {
		t.Errorf("price %.2f not above EMA50 %.2f", s.Price, s.EMA.EMA50)
	}
	if s.Price <= s.EMA.EMA200 {
		t.Errorf("price %.2f not above EMA200 %.2f", s.Price, s.EMA.EMA200)
	}
}

func TestScan_TradeIsLong(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	if signals[0].Trade.Direction != "long" {
		t.Errorf("trade direction = %q, want long", signals[0].Trade.Direction)
	}
}

func TestScan_RRAboveMinimum(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	if signals[0].Trade.RiskReward < defaultOpts.MinRR {
		t.Errorf("R/R %.2f below MinRR %.2f", signals[0].Trade.RiskReward, defaultOpts.MinRR)
	}
}

func TestScan_ScoreIsPositive(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	if signals[0].Score <= 0 {
		t.Errorf("score = %.2f, want > 0", signals[0].Score)
	}
}

func TestScan_ScoreBreakdownSumsToScore(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Fatal("expected at least one signal")
	}

	s := signals[0]
	total := s.Breakdown.Trend + s.Breakdown.RR + s.Breakdown.Support + s.Breakdown.Volume + s.Breakdown.CandleDir
	if !floatsClose(total, s.Score) {
		t.Errorf("breakdown total = %.2f, score = %.2f", total, s.Score)
	}
	if s.Breakdown.Trend == 0 || s.Breakdown.RR == 0 || s.Breakdown.Support == 0 {
		t.Errorf("expected trend, R/R, and support components to be populated, got %+v", s.Breakdown)
	}
	if s.Breakdown.AvgVolume == 0 || s.Breakdown.LastVolume == 0 || s.Breakdown.VolumeRatio == 0 {
		t.Errorf("expected volume diagnostics to be populated, got %+v", s.Breakdown)
	}
}

func TestScan_ExtensionDiagnosticsPopulated(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Fatal("expected at least one signal")
	}

	ext := signals[0].Extension
	if ext.FromEMA10Pct == 0 || ext.FromEMA50Pct == 0 || ext.FromSupportHighPct == 0 {
		t.Errorf("expected extension diagnostics to be populated, got %+v", ext)
	}
	if !ext.HasMove10D {
		t.Errorf("expected 10-day move diagnostic to be available, got %+v", ext)
	}
}

// ---------------------------------------------------------------------------
// Scan — filters
// ---------------------------------------------------------------------------

func TestScan_BearishStockFiltered(t *testing.T) {
	candles := makeBearishCandles("BEAR", 200)
	signals := scanner.Scan([]scanner.Input{{Symbol: "BEAR", Candles: candles}}, defaultOpts)
	if len(signals) != 0 {
		t.Errorf("expected bearish stock to be filtered, got signal: %+v", signals[0])
	}
}

func TestDiagnose_FilteredStockIncludesEMAAndReason(t *testing.T) {
	candles := makeBearishCandles("BEAR", 200)
	diags := scanner.Diagnose([]scanner.Input{{Symbol: "BEAR", Candles: candles}}, defaultOpts)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}
	d := diags[0]
	if d.Symbol != "BEAR" {
		t.Errorf("symbol = %q, want BEAR", d.Symbol)
	}
	if d.Price == 0 {
		t.Error("price should be populated")
	}
	if d.EMA.EMA10 == 0 || d.EMA.EMA50 == 0 || d.EMA.EMA200 == 0 {
		t.Errorf("EMA values should be populated, got %+v", d.EMA)
	}
	if d.Trend != scanner.TrendBearish {
		t.Errorf("trend = %q, want bearish", d.Trend)
	}
	if !strings.Contains(d.Error, "trend is bearish") {
		t.Errorf("error = %q, want bearish trend reason", d.Error)
	}
}

func TestScan_EmptyCandlesSkipped(t *testing.T) {
	signals, errs := scanner.ScanWithErrors([]scanner.Input{{Symbol: "NONE", Candles: nil}}, defaultOpts)
	if len(signals) != 0 {
		t.Error("expected no signals for empty candles")
	}
	if _, ok := errs["NONE"]; !ok {
		t.Error("expected error recorded for NONE")
	}
}

func TestScan_RRFilterApplied(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	// Set MinRR so high that no signal can pass.
	opts := withoutExtensionGuards(scanner.Options{MinRR: 100.0, VolumeWindow: 20})
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, opts)
	if len(signals) != 0 {
		t.Errorf("expected R/R filter to reject signal, got R/R=%.2f", signals[0].Trade.RiskReward)
	}
}

// ---------------------------------------------------------------------------
// Scan — multiple stocks, ranking
// ---------------------------------------------------------------------------

func TestScan_RankedByScoreDescending(t *testing.T) {
	inputs := []scanner.Input{
		{Symbol: "AAA", Candles: makeTrendingCandles("AAA", 100, 500_000)},
		{Symbol: "BBB", Candles: makeTrendingCandles("BBB", 200, 2_000_000)},
		{Symbol: "CCC", Candles: makeTrendingCandles("CCC", 50, 100_000)},
	}
	signals := scanner.Scan(inputs, defaultOpts)
	for i := 1; i < len(signals); i++ {
		if signals[i].Score > signals[i-1].Score {
			t.Errorf("signals not sorted: [%d].Score=%.2f > [%d].Score=%.2f",
				i, signals[i].Score, i-1, signals[i-1].Score)
		}
	}
}

func TestScan_MixedPortfolio(t *testing.T) {
	inputs := []scanner.Input{
		{Symbol: "BULL", Candles: makeTrendingCandles("BULL", 150, 1_000_000)},
		{Symbol: "BEAR", Candles: makeBearishCandles("BEAR", 200)},
		{Symbol: "EMPTY", Candles: nil},
	}
	signals, errs := scanner.ScanWithErrors(inputs, defaultOpts)

	// Only the bullish stock should produce a signal.
	for _, s := range signals {
		if s.Symbol != "BULL" {
			t.Errorf("unexpected signal for %s", s.Symbol)
		}
	}
	// Errors recorded for filtered/skipped symbols.
	if _, ok := errs["EMPTY"]; !ok {
		t.Error("expected error for EMPTY")
	}
}

// ---------------------------------------------------------------------------
// Reasons
// ---------------------------------------------------------------------------

func TestReasons_PresentOnSuccessfulSignal(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	if len(signals[0].Reasons) == 0 {
		t.Error("expected Reasons to be populated, got empty slice")
	}
}

func TestReasons_EMAReasonPresent(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	if !containsReason(signals[0].Reasons, "EMA50") || !containsReason(signals[0].Reasons, "EMA200") {
		t.Errorf("expected EMA trend reason, got: %v", signals[0].Reasons)
	}
}

func TestReasons_RRReasonPresent(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	if !containsReason(signals[0].Reasons, "Risk/Reward") {
		t.Errorf("expected R/R reason, got: %v", signals[0].Reasons)
	}
}

func TestReasons_RRReasonContainsMinRR(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	opts := withoutExtensionGuards(scanner.Options{MinRR: 3.0, VolumeWindow: 20})
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, opts)
	if len(signals) == 0 {
		t.Skip("no signal produced for MinRR=3.0")
	}
	if !containsReason(signals[0].Reasons, "3.00") {
		t.Errorf("expected MinRR 3.00 in R/R reason, got: %v", signals[0].Reasons)
	}
}

func TestReasons_SupportTouchReasonPresent(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	if !containsReason(signals[0].Reasons, "Support zone touched") {
		t.Errorf("expected support touch reason, got: %v", signals[0].Reasons)
	}
}

func TestReasons_TradeQualityReasonPresent(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	if !containsReason(signals[0].Reasons, "Trade quality:") {
		t.Errorf("expected trade quality reason, got: %v", signals[0].Reasons)
	}
}

func TestReasons_VolumeReasonPresentWhenAboveAverage(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	// Spike the last candle's volume well above average.
	candles[len(candles)-1].Volume = 5_000_000
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	if !containsReason(signals[0].Reasons, "Volume") {
		t.Errorf("expected volume reason for above-average volume, got: %v", signals[0].Reasons)
	}
}

func TestReasons_VolumeReasonAbsentWhenBelowAverage(t *testing.T) {
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	// Drop the last candle's volume well below average.
	candles[len(candles)-1].Volume = 1
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	if containsReason(signals[0].Reasons, "Volume") {
		t.Errorf("expected no volume reason for below-average volume, got: %v", signals[0].Reasons)
	}
}

func TestReasons_SingularTouchGrammar(t *testing.T) {
	// The support zone fixture always has touches=2. We test grammar by examining
	// the raw reason builder via a signal where we can observe the touch count.
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	s := signals[0]
	if s.Support.Touches == 1 {
		if !containsReason(s.Reasons, "touched 1 time") || containsReason(s.Reasons, "touched 1 times") {
			t.Errorf("expected singular 'time', got: %v", s.Reasons)
		}
	} else {
		if !containsReason(s.Reasons, "times") {
			t.Errorf("expected plural 'times' for %d touches, got: %v", s.Support.Touches, s.Reasons)
		}
	}
}

func TestReasons_CountIsAtLeastFour(t *testing.T) {
	// Trend + R/R + Support + Quality are always present = minimum 4 reasons.
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	// Use low volume so the 5th (volume) reason is absent — confirms floor is exactly 4.
	candles[len(candles)-1].Volume = 1
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	if len(signals[0].Reasons) < 4 {
		t.Errorf("expected at least 4 reasons, got %d: %v", len(signals[0].Reasons), signals[0].Reasons)
	}
}

func TestReasons_Deterministic(t *testing.T) {
	// Running the scanner twice on the same input must yield identical reasons.
	candles := makeTrendingCandles("AAPL", 100, 1_000_000)
	input := []scanner.Input{{Symbol: "AAPL", Candles: candles}}
	s1 := scanner.Scan(input, defaultOpts)
	s2 := scanner.Scan(input, defaultOpts)
	if len(s1) == 0 || len(s2) == 0 {
		t.Skip("no signal produced")
	}
	r1, r2 := s1[0].Reasons, s2[0].Reasons
	if len(r1) != len(r2) {
		t.Fatalf("reason count differs: %d vs %d", len(r1), len(r2))
	}
	for i := range r1 {
		if r1[i] != r2[i] {
			t.Errorf("reason[%d] differs: %q vs %q", i, r1[i], r2[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Scorer — unit tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// EMA margin filter
// ---------------------------------------------------------------------------

func TestScan_EMAMarginFilter_FiltersWhenTooClose(t *testing.T) {
	// makeTrendingCandles produces price ≈ 2×basePrice, EMA200 ≈ 1.5×basePrice
	// → gap ≈ 33%. A 50% margin requirement should reject the signal.
	candles := makeTrendingCandles("TEST", 100, 1_000_000)
	opts := withoutExtensionGuards(scanner.Options{MinRR: 2.0, VolumeWindow: 20, EMAMarginPct: 50.0})
	signals := scanner.Scan([]scanner.Input{{Symbol: "TEST", Candles: candles}}, opts)
	if len(signals) != 0 {
		t.Errorf("expected 0 signals with 50%% EMA margin (gap ≈33%%), got %d", len(signals))
	}
}

func TestScan_EMAMarginFilter_PassesWhenAboveMargin(t *testing.T) {
	// Same candles: gap ≈ 33% >> 1% → signal should be produced.
	candles := makeTrendingCandles("TEST", 100, 1_000_000)
	opts := withoutExtensionGuards(scanner.Options{MinRR: 2.0, VolumeWindow: 20, EMAMarginPct: 1.0})
	signals := scanner.Scan([]scanner.Input{{Symbol: "TEST", Candles: candles}}, opts)
	if len(signals) == 0 {
		t.Error("expected signal with 1%% EMA margin and ~33%% gap above EMA200, got none")
	}
}

func TestScan_EMAMarginFilter_DisabledWhenNegative(t *testing.T) {
	// EMAMarginPct < 0 disables the filter; the default-opt gap check is bypassed.
	candles := makeTrendingCandles("TEST", 100, 1_000_000)
	opts := withoutExtensionGuards(scanner.Options{MinRR: 2.0, VolumeWindow: 20, EMAMarginPct: -1.0})
	signals := scanner.Scan([]scanner.Input{{Symbol: "TEST", Candles: candles}}, opts)
	if len(signals) == 0 {
		t.Error("expected signal with EMA margin disabled (EMAMarginPct=-1), got none")
	}
}

// ---------------------------------------------------------------------------
// Minimum average volume (liquidity) filter
// ---------------------------------------------------------------------------

func TestScan_MinAvgVolumeFilter_FiltersIlliquid(t *testing.T) {
	// makeTrendingCandles with baseVolume=50_000 → rolling avg ≈ 50k.
	// MinAvgVolume=200_000 must reject it.
	candles := makeTrendingCandles("ILLIQ", 100, 50_000)
	opts := withoutExtensionGuards(scanner.Options{MinRR: 2.0, VolumeWindow: 20, MinAvgVolume: 200_000})
	signals := scanner.Scan([]scanner.Input{{Symbol: "ILLIQ", Candles: candles}}, opts)
	if len(signals) != 0 {
		t.Errorf("expected illiquid stock (avg vol ~50k) to be filtered at threshold 200k, got signal")
	}
}

func TestScan_MinAvgVolumeFilter_PassesLiquidStock(t *testing.T) {
	// avg vol ≈ 1_000_000 >> 200_000 minimum.
	candles := makeTrendingCandles("LIQUID", 100, 1_000_000)
	opts := withoutExtensionGuards(scanner.Options{MinRR: 2.0, VolumeWindow: 20, MinAvgVolume: 200_000})
	signals := scanner.Scan([]scanner.Input{{Symbol: "LIQUID", Candles: candles}}, opts)
	if len(signals) == 0 {
		t.Error("expected liquid stock (avg vol 1M) to pass at threshold 200k, got no signal")
	}
}

func TestScan_MinAvgVolumeFilter_DisabledWhenZero(t *testing.T) {
	// MinAvgVolume=0 disables the filter entirely; low-volume stock should
	// pass all other filters and produce a signal.
	candles := makeTrendingCandles("LOW", 100, 50_000)
	opts := withoutExtensionGuards(scanner.Options{MinRR: 2.0, VolumeWindow: 20, MinAvgVolume: 0})
	signals := scanner.Scan([]scanner.Input{{Symbol: "LOW", Candles: candles}}, opts)
	if len(signals) == 0 {
		t.Error("expected MinAvgVolume=0 to disable the filter, got no signals")
	}
}

// ---------------------------------------------------------------------------
// Late-rally extension filter
// ---------------------------------------------------------------------------

func TestScan_ExtensionFilter_FiltersWhenTooFarAboveSupport(t *testing.T) {
	candles := makeTrendingCandles("EXTENDED", 100, 1_000_000)
	opts := scanner.Options{
		MinRR:                  2.0,
		VolumeWindow:           20,
		MaxEMA10ExtensionPct:   -1,
		MaxEMA50ExtensionPct:   -1,
		MaxSupportExtensionPct: 5.0,
		MaxMove10DPct:          -1,
		// Disable quality filters — this test only checks the extension guard.
		MaxRiskPct:         -1,
		MinRiskPct:         -1,
		AllowBearishCandle: true,
		EMA200SlopePeriod:  -1,
	}

	signals, errs := scanner.ScanWithErrors([]scanner.Input{{Symbol: "EXTENDED", Candles: candles}}, opts)
	if len(signals) != 0 {
		t.Fatalf("expected extension filter to reject setup, got %d signals", len(signals))
	}
	if err := errs["EXTENDED"]; err == nil || !strings.Contains(err.Error(), "setup extended after recent rally") {
		t.Fatalf("expected extension rejection reason, got %v", err)
	}
}

func TestScan_ExtensionFilter_DisabledWhenNegative(t *testing.T) {
	candles := makeTrendingCandles("EXTENDED", 100, 1_000_000)
	opts := scanner.Options{
		MinRR:                  2.0,
		VolumeWindow:           20,
		MaxEMA10ExtensionPct:   -1,
		MaxEMA50ExtensionPct:   -1,
		MaxSupportExtensionPct: -1,
		MaxMove10DPct:          -1,
		// Disable quality filters so the synthetic fixture is not blocked by them.
		MaxRiskPct:         -1,
		MinRiskPct:         -1,
		AllowBearishCandle: true,
		EMA200SlopePeriod:  -1,
	}

	signals := scanner.Scan([]scanner.Input{{Symbol: "EXTENDED", Candles: candles}}, opts)
	if len(signals) == 0 {
		t.Fatal("expected signal when extension guards are disabled, got none")
	}
}

// ---------------------------------------------------------------------------
// Breakout watch scanner
// ---------------------------------------------------------------------------

func TestScanBreakouts_ReturnsCandidateNearResistance(t *testing.T) {
	candles := makeTrendingCandles("BREAK", 100, 1_000_000)
	candles[len(candles)-1].Close = 255
	candles[len(candles)-1].Open = 255
	candles[len(candles)-1].High = 256
	candles[len(candles)-1].Low = 252

	opts := withoutExtensionGuards(scanner.Options{
		MinRR:                  2.0,
		VolumeWindow:           20,
		MaxBreakoutDistancePct: 3.0,
	})
	signals, errs := scanner.ScanBreakouts([]scanner.Input{{Symbol: "BREAK", Candles: candles}}, opts)
	if len(signals) != 1 {
		t.Fatalf("expected one breakout candidate, got %d, errs=%v", len(signals), errs)
	}
	if signals[0].DistanceToResistancePct > 3.0 {
		t.Errorf("distance = %.2f, want <= 3.0", signals[0].DistanceToResistancePct)
	}
	if signals[0].Resistance.Touches < 2 {
		t.Errorf("resistance touches = %d, want >= 2", signals[0].Resistance.Touches)
	}
}

func TestScanBreakouts_FiltersWhenTooFarFromResistance(t *testing.T) {
	candles := makeTrendingCandles("FAR", 100, 1_000_000)
	opts := withoutExtensionGuards(scanner.Options{
		MinRR:                  2.0,
		VolumeWindow:           20,
		MaxBreakoutDistancePct: 3.0,
	})

	signals, errs := scanner.ScanBreakouts([]scanner.Input{{Symbol: "FAR", Candles: candles}}, opts)
	if len(signals) != 0 {
		t.Fatalf("expected no breakout candidates, got %d", len(signals))
	}
	if err := errs["FAR"]; err == nil || !strings.Contains(err.Error(), "above price") {
		t.Fatalf("expected distance rejection, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Minimum candle count guard
// ---------------------------------------------------------------------------

func TestScan_MinCandlesFilter_RejectsInsufficient(t *testing.T) {
	candles := makeTrendingCandles("TEST", 100, 1_000_000)
	// Require one more candle than available — must reject.
	opts := defaultOpts
	opts.MinCandles = len(candles) + 1
	signals := scanner.Scan([]scanner.Input{{Symbol: "TEST", Candles: candles}}, opts)
	if len(signals) != 0 {
		t.Errorf("expected MinCandles filter to reject with %d candles (min %d), got a signal",
			len(candles), len(candles)+1)
	}
}

func TestScan_MinCandlesFilter_PassesWhenMet(t *testing.T) {
	candles := makeTrendingCandles("TEST", 100, 1_000_000)
	// Exactly enough candles — must pass.
	opts := defaultOpts
	opts.MinCandles = len(candles)
	signals := scanner.Scan([]scanner.Input{{Symbol: "TEST", Candles: candles}}, opts)
	if len(signals) == 0 {
		t.Errorf("expected signal when candle count (%d) equals MinCandles", len(candles))
	}
}

// ---------------------------------------------------------------------------
// Bearish candle direction penalty
// ---------------------------------------------------------------------------

func TestScan_BearishCandlePenalty_Applied(t *testing.T) {
	candles := makeTrendingCandles("TEST", 100, 1_000_000)
	// Force the last candle to be bearish (open > close).
	candles[len(candles)-1].Open = candles[len(candles)-1].Close * 1.02

	// AllowBearishCandle: true — disable the hard filter so we can verify the
	// soft −5 score penalty is still applied when bearish candles are permitted.
	opts := defaultOpts
	opts.AllowBearishCandle = true
	signals := scanner.Scan([]scanner.Input{{Symbol: "TEST", Candles: candles}}, opts)
	if len(signals) == 0 {
		t.Skip("no signal produced — cannot test penalty")
	}
	if signals[0].Breakdown.CandleDir >= 0 {
		t.Errorf("expected negative CandleDir for bearish candle, got %.1f", signals[0].Breakdown.CandleDir)
	}
}

// TestScan_BearishCandle_HardRejected verifies that the default (AllowBearishCandle=false)
// rejects a setup whose signal candle closed below its open.
func TestScan_BearishCandle_HardRejected(t *testing.T) {
	candles := makeTrendingCandles("TEST", 100, 1_000_000)
	candles[len(candles)-1].Open = candles[len(candles)-1].Close * 1.02 // bearish

	// Use defaultOpts but with AllowBearishCandle explicitly false (hard filter ON).
	opts := defaultOpts
	opts.AllowBearishCandle = false
	signals, errs := scanner.ScanWithErrors([]scanner.Input{{Symbol: "TEST", Candles: candles}}, opts)
	if len(signals) != 0 {
		t.Fatalf("expected bearish signal candle to be hard-rejected, got %d signals", len(signals))
	}
	if err := errs["TEST"]; err == nil || !strings.Contains(err.Error(), "bearish") {
		t.Fatalf("expected bearish-candle rejection error, got %v", err)
	}
}

func TestScan_BullishCandle_NoPenalty(t *testing.T) {
	// makeTrendingCandles last candle has open == close (flat) — no penalty.
	candles := makeTrendingCandles("TEST", 100, 1_000_000)
	signals := scanner.Scan([]scanner.Input{{Symbol: "TEST", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced — cannot test penalty")
	}
	if signals[0].Breakdown.CandleDir != 0 {
		t.Errorf("expected CandleDir=0 for flat/bullish candle, got %.1f", signals[0].Breakdown.CandleDir)
	}
}

func TestScan_ScoreMaxIs100(t *testing.T) {
	// Score components: 40+30+20+10 = 100 max.
	// A bullish stock with excellent R/R, 4+ touch support, and high volume
	// should approach but not exceed 100.
	candles := makeTrendingCandles("AAPL", 100, 2_000_000)
	// Spike last candle volume to 3× average to max out volume score.
	candles[len(candles)-1].Volume = 6_000_000
	signals := scanner.Scan([]scanner.Input{{Symbol: "AAPL", Candles: candles}}, defaultOpts)
	if len(signals) == 0 {
		t.Skip("no signal produced")
	}
	if signals[0].Score > 100 {
		t.Errorf("score %.2f exceeds maximum of 100", signals[0].Score)
	}
}

// ---------------------------------------------------------------------------
// MaxRiskPct filter
// ---------------------------------------------------------------------------

// TestScan_MaxRiskPct_RejectsHighRisk sets a very tight cap (0.1 %) that the
// synthetic fixture (risk ≈ 7–9 %) cannot satisfy.
func TestScan_MaxRiskPct_RejectsHighRisk(t *testing.T) {
	candles := makeTrendingCandles("TEST", 100, 1_000_000)
	opts := defaultOpts
	opts.AllowBearishCandle = true // keep bearish-candle filter out of the way
	opts.MaxRiskPct = 0.1          // absurdly tight — must reject
	signals, errs := scanner.ScanWithErrors([]scanner.Input{{Symbol: "TEST", Candles: candles}}, opts)
	if len(signals) != 0 {
		t.Fatalf("expected MaxRiskPct=0.1%% to reject signal, got %d signals", len(signals))
	}
	if err := errs["TEST"]; err == nil || !strings.Contains(err.Error(), "exceeds max") {
		t.Fatalf("expected risk-cap rejection error, got %v", err)
	}
}

// TestScan_MaxRiskPct_DisabledWhenNegative verifies that MaxRiskPct < 0 turns
// the filter off entirely.
func TestScan_MaxRiskPct_DisabledWhenNegative(t *testing.T) {
	candles := makeTrendingCandles("TEST", 100, 1_000_000)
	opts := defaultOpts
	opts.MaxRiskPct = -1 // explicitly disabled
	signals := scanner.Scan([]scanner.Input{{Symbol: "TEST", Candles: candles}}, opts)
	if len(signals) == 0 {
		t.Error("expected signal when MaxRiskPct is disabled (-1), got none")
	}
}

// ---------------------------------------------------------------------------
// MinRiskPct filter
// ---------------------------------------------------------------------------

// TestScan_MinRiskPct_RejectsTightStop verifies that a very high minimum
// (e.g. 99 %) rejects any realistic setup.
func TestScan_MinRiskPct_RejectsTightStop(t *testing.T) {
	candles := makeTrendingCandles("TEST", 100, 1_000_000)
	opts := defaultOpts
	opts.AllowBearishCandle = true
	opts.MinRiskPct = 99.0 // nothing can satisfy this
	signals, errs := scanner.ScanWithErrors([]scanner.Input{{Symbol: "TEST", Candles: candles}}, opts)
	if len(signals) != 0 {
		t.Fatalf("expected MinRiskPct=99%% to reject signal, got %d signals", len(signals))
	}
	if err := errs["TEST"]; err == nil || !strings.Contains(err.Error(), "below min") {
		t.Fatalf("expected min-risk rejection error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// EMA200 slope filter
// ---------------------------------------------------------------------------

// TestScan_EMA200Slope_PassesRisingTrend confirms the synthetic fixture (which
// has a genuinely rising EMA200) is not filtered.
func TestScan_EMA200Slope_PassesRisingTrend(t *testing.T) {
	candles := makeTrendingCandles("TEST", 100, 1_000_000)
	opts := defaultOpts
	opts.EMA200SlopePeriod = 20
	signals := scanner.Scan([]scanner.Input{{Symbol: "TEST", Candles: candles}}, opts)
	if len(signals) == 0 {
		t.Error("expected signal when EMA200 is rising, got none")
	}
}

// TestScan_EMA200Slope_RejectsDeclining builds a series that starts rising
// (to satisfy the EMA200 requirement) but then gradually declines so the
// EMA200 is lower today than it was 20 candles ago.
func TestScan_EMA200Slope_RejectsDeclining(t *testing.T) {
	// Phase 1 — strong rise: 220 candles so EMA200 is well-seeded and rising.
	// Phase 2 — long decline: 60 candles that bring price down to ensure the
	//   EMA200 today is lower than EMA200[today−20].
	var candles []models.Candle
	ts := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	add := func(o, h, l, c float64) {
		candles = append(candles, models.Candle{
			Symbol: "SLOPE", Timestamp: ts,
			Open: o, High: h, Low: l, Close: c, Volume: 500_000,
		})
		ts = ts.Add(24 * time.Hour)
	}

	// Rise: 100 → 300 over 220 candles.
	for i := range 220 {
		p := 100 + float64(i)*200/220
		add(p, p*1.005, p*0.995, p)
	}
	// Decline: 300 → 150 over 60 candles, pulling EMA200 down.
	for i := range 60 {
		p := 300 - float64(i)*150/60
		add(p, p*1.005, p*0.995, p)
	}

	opts := defaultOpts
	opts.EMAMarginPct = -1 // disable — price may be below EMA200
	opts.EMA200SlopePeriod = 20
	opts.AllowBearishCandle = true

	_, errs := scanner.ScanWithErrors([]scanner.Input{{Symbol: "SLOPE", Candles: candles}}, opts)
	// Either filtered early (trend not bullish) or caught by EMA200 slope.
	// At least one rejection must have occurred.
	if _, filtered := errs["SLOPE"]; !filtered {
		t.Fatal("expected declining series to be filtered, but a signal was produced")
	}
}

// TestScan_EMA200Slope_DisabledWhenNegative confirms that EMA200SlopePeriod ≤ 0
// turns the check off.
func TestScan_EMA200Slope_DisabledWhenNegative(t *testing.T) {
	candles := makeTrendingCandles("TEST", 100, 1_000_000)
	opts := defaultOpts
	opts.EMA200SlopePeriod = -1
	signals := scanner.Scan([]scanner.Input{{Symbol: "TEST", Candles: candles}}, opts)
	if len(signals) == 0 {
		t.Error("expected signal when EMA200 slope filter is disabled (-1), got none")
	}
}
