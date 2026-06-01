package analysis_test

import (
	"math"
	"testing"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// makeCandles builds a candle slice where every bar has the same OHLCV shape:
//   - Close = basePrice
//   - High  = Close + halfRange
//   - Low   = Close − halfRange
//   - Open  = Close (flat)
//
// This gives a deterministic True Range of 2 × halfRange for every bar
// (assuming all closes are equal, so the |Close−PrevClose| terms are zero).
func makeCandles(n int, basePrice, halfRange float64) []models.Candle {
	cs := make([]models.Candle, n)
	t := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range cs {
		cs[i] = models.Candle{
			Symbol:    "TEST",
			Timestamp: t,
			Open:      basePrice,
			High:      basePrice + halfRange,
			Low:       basePrice - halfRange,
			Close:     basePrice,
		}
		t = t.Add(24 * time.Hour)
	}
	return cs
}

func TestATR_InsufficientCandles_ReturnsZero(t *testing.T) {
	// Need period+1 candles minimum; 14 candles for period=14 is one short.
	cs := makeCandles(14, 100, 2)
	if got := analysis.ATR(cs, 14); got != 0 {
		t.Errorf("ATR with %d candles (period=14) = %.2f, want 0", len(cs), got)
	}
}

func TestATR_ConstantRange_EqualsRange(t *testing.T) {
	// When every bar has the same range and closes are equal, TR = range.
	// ATR should converge to exactly that range.
	halfRange := 5.0
	wantATR := halfRange * 2 // TR = High - Low = 2*halfRange (no gap between bars)
	cs := makeCandles(100, 200, halfRange)
	got := analysis.ATR(cs, 14)
	if math.Abs(got-wantATR) > 0.01 {
		t.Errorf("ATR = %.2f, want %.2f (±0.01)", got, wantATR)
	}
}

func TestATR_DefaultPeriod_UsedWhenZero(t *testing.T) {
	// period=0 should fall back to 14; result should match explicit period=14.
	cs := makeCandles(50, 100, 3)
	want := analysis.ATR(cs, 14)
	got := analysis.ATR(cs, 0)
	if got != want {
		t.Errorf("ATR(period=0) = %.2f, want %.2f (same as period=14)", got, want)
	}
}

func TestATR_LargerPeriod_SmoothsMoreSlowly(t *testing.T) {
	// After a sudden spike, ATR with a larger period should be smaller
	// (it spreads the spike over more bars) than with a shorter period,
	// assuming the spike is the last candle.
	cs := makeCandles(100, 100, 1) // 99 quiet bars
	// Add one large spike at the end.
	cs = append(cs, models.Candle{
		Symbol: "TEST", Open: 100, High: 150, Low: 50, Close: 100,
	})
	atr14 := analysis.ATR(cs, 14)
	atr5 := analysis.ATR(cs, 5)
	// A shorter period reacts more to the recent spike → atr5 should be larger.
	if atr5 <= atr14 {
		t.Errorf("expected shorter period to produce larger ATR after spike: ATR5=%.2f ATR14=%.2f",
			atr5, atr14)
	}
}

func TestATR_AlwaysNonNegative(t *testing.T) {
	cs := makeCandles(50, 200, 10)
	if got := analysis.ATR(cs, 14); got < 0 {
		t.Errorf("ATR should never be negative, got %.2f", got)
	}
}
