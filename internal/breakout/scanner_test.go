package breakout

import (
	"testing"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// candleSpec is one day's OHLCV, expressed relative to a symbol's own scale.
type candleSpec struct {
	open, high, low, close float64
	volume                 int64
}

func buildCandles(spec []candleSpec) []models.Candle {
	out := make([]models.Candle, len(spec))
	base := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
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
// flat volume — enough to seed EMA50/EMA200 well below the eventual breakout
// zone without itself forming a competing resistance zone near the top.
func baseUptrend(n int, start float64) []candleSpec {
	out := make([]candleSpec, 0, n)
	price := start
	for i := 0; i < n; i++ {
		price *= 1.003
		out = append(out, candleSpec{open: price, high: price * 1.005, low: price * 0.995, close: price, volume: 1000})
	}
	return out
}

// resistanceZone builds a consolidation range that touches `level` three
// times as a local high (Window=2 apart), with troughs between, then a
// breakout candle that closes above `level` on `breakoutVolume`.
func resistanceZone(level float64, breakoutVolume int64) []candleSpec {
	touch := candleSpec{open: level * 0.995, high: level, low: level * 0.99, close: level * 0.998, volume: 1000}
	trough := candleSpec{open: level * 0.96, high: level * 0.965, low: level * 0.95, close: level * 0.955, volume: 1000}
	rise := candleSpec{open: level * 0.96, high: level * 0.975, low: level * 0.955, close: level * 0.97, volume: 1000}
	breakout := candleSpec{
		open: level * 1.005, high: level * 1.06, low: level * 1.0, close: level * 1.05, volume: breakoutVolume,
	}
	return []candleSpec{
		touch, trough, rise, touch, trough, rise, touch, trough, rise, breakout,
	}
}

func withMinCandles(spec []candleSpec, min int) []candleSpec {
	if len(spec) >= min {
		return spec
	}
	pad := baseUptrend(min-len(spec), spec[0].close/1.5)
	return append(pad, spec...)
}

func TestScan_FiresOnConfirmedBreakout(t *testing.T) {
	spec := append(baseUptrend(200, 100), resistanceZone(180, 3500)...)
	spec = withMinCandles(spec, 210)

	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(spec)}}, Options{})
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signal, got %d (errs: %v)", len(sigs), errs)
	}
	s := sigs[0]
	if s.SL >= s.Entry || s.SL <= 0 {
		t.Fatalf("SL %.2f must be below entry %.2f and positive", s.SL, s.Entry)
	}
	if s.Target <= s.Entry {
		t.Fatalf("target %.2f must be above entry %.2f", s.Target, s.Entry)
	}
	rr := (s.Target - s.Entry) / (s.Entry - s.SL)
	if rr < 2.0 {
		t.Fatalf("R/R %.2f below the 2.0 default minimum", rr)
	}
	if s.VolumeRatio < 1.5 {
		t.Fatalf("volume ratio %.2f below the 1.5 default minimum", s.VolumeRatio)
	}
	if s.Score < 25 || s.Score > 100 {
		t.Fatalf("score %.1f out of expected 25-100 band", s.Score)
	}
}

func TestScan_RejectsWithoutVolumeConfirmation(t *testing.T) {
	// Same shape, but the breakout candle's volume matches the flat baseline —
	// no confirmation, so the breakout could be a fake-out.
	spec := append(baseUptrend(200, 100), resistanceZone(180, 1000)...)
	spec = withMinCandles(spec, 210)

	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(spec)}}, Options{})
	if len(sigs) != 0 {
		t.Fatalf("expected no signal without volume confirmation, got %d", len(sigs))
	}
	if errs["TEST"] == nil {
		t.Fatal("expected a rejection reason for TEST")
	}
}

func TestScan_RejectsWhenNotFreshBreakout(t *testing.T) {
	// Extend the breakout by one more day at an even higher close: the SECOND
	// day is no longer a fresh break — the prior close was already above the
	// resistance zone.
	spec := append(baseUptrend(200, 100), resistanceZone(180, 3500)...)
	spec = withMinCandles(spec, 210)
	last := spec[len(spec)-1]
	spec = append(spec, candleSpec{
		open: last.close * 1.01, high: last.close * 1.03, low: last.close * 0.99, close: last.close * 1.02, volume: 3500,
	})

	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(spec)}}, Options{})
	if len(sigs) != 0 {
		t.Fatalf("expected no signal on the day after the breakout (already extended), got %d", len(sigs))
	}
	if errs["TEST"] == nil {
		t.Fatal("expected a rejection reason for TEST")
	}
}

func TestScan_RejectsDownTrend(t *testing.T) {
	// A long decline keeps price below EMA50/EMA200 — even a local "breakout"
	// candle inside a downtrend should not fire.
	spec := make([]candleSpec, 0, 215)
	price := 300.0
	for i := 0; i < 215; i++ {
		price *= 0.996
		spec = append(spec, candleSpec{open: price, high: price * 1.01, low: price * 0.99, close: price, volume: 1000})
	}
	spec[len(spec)-1].volume = 5000 // even with a volume spike, trend filter must block it

	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(spec)}}, Options{})
	if len(sigs) != 0 {
		t.Fatalf("expected no signal in a downtrend, got %d", len(sigs))
	}
	if errs["TEST"] == nil {
		t.Fatal("expected a rejection reason for TEST")
	}
}

func TestScan_RejectsWhenNoResistanceZone(t *testing.T) {
	// A smooth, unbroken uptrend never forms a tested resistance zone to break.
	spec := baseUptrend(215, 100)

	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(spec)}}, Options{})
	if len(sigs) != 0 {
		t.Fatalf("expected no signal without a resistance zone, got %d", len(sigs))
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
