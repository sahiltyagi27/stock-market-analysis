package meanrev

import (
	"testing"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// buildCandles turns a slice of closes into candles with a small symmetric
// intraday range around each close, on consecutive days. High/low are set wide
// enough that ATR is non-zero and the SL sits below price.
func buildCandles(closes []float64) []models.Candle {
	out := make([]models.Candle, len(closes))
	base := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, c := range closes {
		rng := c * 0.02 // 2% range
		out[i] = models.Candle{
			Symbol:    "TEST",
			Timestamp: base.AddDate(0, 0, i),
			Open:      c,
			High:      c + rng/2,
			Low:       c - rng/2,
			Close:     c,
			Volume:    1000,
		}
	}
	return out
}

// upTrendThenDip builds 210 candles: a long, steady rise (so EMA200 sits well
// below price and is rising) followed by a sharp multi-day drop that leaves
// price still above EMA200 but RSI(2) deeply oversold.
func upTrendThenDip() []models.Candle {
	closes := make([]float64, 0, 215)
	price := 100.0
	for i := 0; i < 209; i++ {
		price *= 1.004 // ~0.4%/day rise over ~209 days
		closes = append(closes, price)
	}
	// Sharp 3-day washout — still above the slow EMA, but short-term oversold.
	closes = append(closes, price*0.96)
	closes = append(closes, price*0.93)
	closes = append(closes, price*0.91)
	return buildCandles(closes)
}

func TestScan_FiresOnOversoldDipInUptrend(t *testing.T) {
	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: upTrendThenDip()}}, Options{})
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signal, got %d (errs: %v)", len(sigs), errs)
	}
	s := sigs[0]
	if s.RSI >= 10 {
		t.Fatalf("expected oversold RSI(2) < 10, got %.2f", s.RSI)
	}
	if s.Target <= s.Entry {
		t.Fatalf("target %.2f must be above entry %.2f (reversion to mean)", s.Target, s.Entry)
	}
	if s.SL >= s.Entry || s.SL <= 0 {
		t.Fatalf("SL %.2f must be below entry %.2f and positive", s.SL, s.Entry)
	}
	if s.Score < 50 || s.Score > 100 {
		t.Fatalf("score %.1f out of expected 50–100 band", s.Score)
	}
}

func TestScan_RejectsWhenNotOversold(t *testing.T) {
	// Steady rise with no washout → RSI(2) high → no signal.
	closes := make([]float64, 0, 215)
	price := 100.0
	for i := 0; i < 215; i++ {
		price *= 1.004
		closes = append(closes, price)
	}
	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(closes)}}, Options{})
	if len(sigs) != 0 {
		t.Fatalf("expected no signal on an unbroken up-run, got %d", len(sigs))
	}
	if errs["TEST"] == nil {
		t.Fatal("expected a rejection reason for TEST")
	}
}

func TestScan_RejectsDownTrend(t *testing.T) {
	// A long decline keeps price below EMA200 — even when oversold, no dip-buy.
	closes := make([]float64, 0, 215)
	price := 300.0
	for i := 0; i < 209; i++ {
		price *= 0.996 // steady decline
		closes = append(closes, price)
	}
	closes = append(closes, price*0.96, price*0.93, price*0.91)
	sigs, _ := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(closes)}}, Options{})
	if len(sigs) != 0 {
		t.Fatalf("expected no signal in a down-trend (price below EMA200), got %d", len(sigs))
	}
}

func TestScan_RejectsInsufficientHistory(t *testing.T) {
	closes := make([]float64, 50)
	for i := range closes {
		closes[i] = 100 + float64(i)
	}
	sigs, errs := Scan([]Input{{Symbol: "TEST", Candles: buildCandles(closes)}}, Options{})
	if len(sigs) != 0 {
		t.Fatalf("expected no signal with only 50 candles, got %d", len(sigs))
	}
	if errs["TEST"] == nil {
		t.Fatal("expected an insufficient-history error")
	}
}

func TestScan_DeeperOversoldScoresHigher(t *testing.T) {
	// Two symbols, different washout depths — the deeper dip should score higher.
	mild := upTrendThenDip()
	// Build a deeper dip by extending the drop.
	deepCloses := make([]float64, 0, 215)
	price := 100.0
	for i := 0; i < 209; i++ {
		price *= 1.004
		deepCloses = append(deepCloses, price)
	}
	deepCloses = append(deepCloses, price*0.93, price*0.88, price*0.85)
	deep := buildCandles(deepCloses)

	sigs, _ := Scan([]Input{
		{Symbol: "MILD", Candles: mild},
		{Symbol: "DEEP", Candles: deep},
	}, Options{})
	if len(sigs) != 2 {
		t.Fatalf("expected 2 signals, got %d", len(sigs))
	}
	// Scan sorts by score desc; the deeper washout should come first.
	if sigs[0].Symbol != "DEEP" {
		t.Fatalf("expected DEEP (deeper oversold) ranked first, got %s (RSI %.2f) before %s (RSI %.2f)",
			sigs[0].Symbol, sigs[0].RSI, sigs[1].Symbol, sigs[1].RSI)
	}
}
