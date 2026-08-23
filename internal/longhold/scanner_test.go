package longhold

import (
	"testing"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

type candleSpec struct {
	open, high, low, close float64
	volume                 int64
}

func buildCandles(spec []candleSpec) []models.Candle {
	out := make([]models.Candle, len(spec))
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, s := range spec {
		out[i] = models.Candle{
			Symbol:    "TEST",
			Timestamp: base.AddDate(0, 0, i),
			Open:      s.open,
			High:      s.high,
			Low:       s.low,
			Close:     s.close,
			Volume:    s.volume,
		}
	}
	return out
}

// baseUptrend builds n days of a steady, low-volatility rise from `start`,
// flat volume — seeds a rising EMA200 without itself qualifying as a fresh
// high relative to the immediately preceding days (each day only edges
// slightly above the last).
func baseUptrend(n int, start float64) []candleSpec {
	out := make([]candleSpec, 0, n)
	price := start
	for i := 0; i < n; i++ {
		price *= 1.0015
		out = append(out, candleSpec{open: price, high: price * 1.005, low: price * 0.995, close: price, volume: 1000})
	}
	return out
}

func TestScan_FiresOnFreshHighWithVolumeInUptrend(t *testing.T) {
	spec := baseUptrend(460, 100)
	last := spec[len(spec)-1]
	breakout := candleSpec{
		open: last.close, high: last.high * 1.05, low: last.close * 0.99, close: last.high * 1.04, volume: 3500,
	}
	spec = append(spec, breakout)

	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(spec)}}, Options{})
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signal on a fresh high with volume, got %d (errs: %v)", len(sigs), errs)
	}
	s := sigs[0]
	if s.SL <= 0 || s.SL >= s.Entry {
		t.Fatalf("SL %.2f must be positive and below entry %.2f", s.SL, s.Entry)
	}
	if s.VolumeRatio < 1.5 {
		t.Fatalf("volume ratio %.2f below the 1.5 default minimum", s.VolumeRatio)
	}
	if s.Score < 25 || s.Score > 100 {
		t.Fatalf("score %.1f out of expected 25-100 band", s.Score)
	}
}

func TestScan_RejectsWithoutVolumeConfirmation(t *testing.T) {
	spec := baseUptrend(460, 100)
	last := spec[len(spec)-1]
	// Same breakout shape, but volume matches the flat baseline — no
	// confirmation this new high has real participation behind it.
	breakout := candleSpec{
		open: last.close, high: last.high * 1.05, low: last.close * 0.99, close: last.high * 1.04, volume: 1000,
	}
	spec = append(spec, breakout)

	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(spec)}}, Options{})
	if len(sigs) != 0 {
		t.Fatalf("expected no signal without volume confirmation, got %d", len(sigs))
	}
	if errs["TEST"] == nil {
		t.Fatal("expected a rejection reason for TEST")
	}
}

func TestScan_RejectsWhenNotActuallyAFreshHigh(t *testing.T) {
	// A stock consolidating just below a recent peak — today's high doesn't
	// clear the prior lookback-window high, so this isn't "buying strength"
	// yet, it's still basing.
	spec := baseUptrend(460, 100)
	peak := spec[len(spec)-1].high * 1.1
	spec = append(spec,
		candleSpec{open: peak * 0.98, high: peak, low: peak * 0.95, close: peak * 0.99, volume: 3000},
		candleSpec{open: peak * 0.97, high: peak * 0.98, low: peak * 0.94, close: peak * 0.96, volume: 3000},
	)

	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(spec)}}, Options{})
	if len(sigs) != 0 {
		t.Fatalf("expected no signal when today isn't a fresh high, got %d", len(sigs))
	}
	if errs["TEST"] == nil {
		t.Fatal("expected a rejection reason for TEST")
	}
}

func TestScan_RejectsDownTrend(t *testing.T) {
	spec := make([]candleSpec, 0, 465)
	price := 300.0
	for i := 0; i < 465; i++ {
		price *= 0.9985
		spec = append(spec, candleSpec{open: price, high: price * 1.01, low: price * 0.99, close: price, volume: 1000})
	}
	// Even a same-day volume spike shouldn't matter — the trend filter must
	// block a downtrend regardless of what the high-lookback/volume checks say.
	spec[len(spec)-1].volume = 5000
	spec[len(spec)-1].high = spec[len(spec)-1].high * 1.1

	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(spec)}}, Options{})
	if len(sigs) != 0 {
		t.Fatalf("expected no signal in a downtrend, got %d", len(sigs))
	}
	if errs["TEST"] == nil {
		t.Fatal("expected a rejection reason for TEST")
	}
}

func TestScan_RejectsFlatTrendEMA(t *testing.T) {
	// Price sits above the trend EMA and makes a fresh high, but the EMA
	// itself is still declining — a short bounce off a long decline, not yet
	// a real trend change. Uses a faster test-specific EMA (30) so the slope
	// condition is cleanly isolable: a 30-day EMA responds to the bounce
	// quickly enough that price clears it, but its own trajectory (now vs.
	// TrendSlopeLookback candles ago) is still catching up from the decline.
	opts := Options{HighLookback: 15, TrendEMA: 30, TrendSlopeLookback: 20}
	spec := make([]candleSpec, 0, 400)
	price := 300.0
	for i := 0; i < 300; i++ {
		price *= 0.994 // steady decline, 300 -> ~50
		spec = append(spec, candleSpec{open: price, high: price * 1.005, low: price * 0.995, close: price, volume: 1000})
	}
	for i := 0; i < 12; i++ {
		price *= 1.02 // a short, sharp bounce
		spec = append(spec, candleSpec{open: price, high: price * 1.01, low: price * 0.99, close: price, volume: 1000})
	}
	spec = append(spec, candleSpec{open: price, high: price * 1.06, low: price * 0.99, close: price * 1.05, volume: 5000})

	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(spec)}}, opts)
	if len(sigs) != 0 {
		t.Fatalf("expected no signal when the trend EMA isn't rising, got %d", len(sigs))
	}
	if errs["TEST"] == nil {
		t.Fatal("expected a rejection reason for TEST")
	}
}

func TestScan_RejectsShortHistory(t *testing.T) {
	spec := baseUptrend(50, 100)
	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(spec)}}, Options{})
	if len(sigs) != 0 {
		t.Fatalf("expected no signal with insufficient history, got %d", len(sigs))
	}
	if errs["TEST"] == nil {
		t.Fatal("expected a rejection reason for TEST")
	}
}
