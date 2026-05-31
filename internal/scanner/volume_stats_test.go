package scanner

// Internal test file — uses package scanner (not scanner_test) so it can
// reach the unexported volumeStats function directly.

import (
	"testing"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

func makeVolumeCandles(volumes ...int64) []models.Candle {
	out := make([]models.Candle, len(volumes))
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, v := range volumes {
		out[i] = models.Candle{
			Symbol: "X", Timestamp: ts,
			Open: 100, High: 101, Low: 99, Close: 100,
			Volume: v,
		}
		ts = ts.Add(24 * time.Hour)
	}
	return out
}

// Case 1: spike on the last candle — avg must NOT include it.
func TestVolumeStats_SpikeExcludedFromAverage(t *testing.T) {
	candles := makeVolumeCandles(100, 100, 100, 100, 500)
	avg, last := volumeStats(candles, 20)

	if last != 500 {
		t.Errorf("last = %.0f, want 500", last)
	}
	// previous four candles are all 100 → avg must be 100, not 180
	if avg != 100 {
		t.Errorf("avg = %.2f, want 100 (last candle must be excluded)", avg)
	}
}

// Confirm the ratio is 5.0, not 2.78 as the buggy version would give.
func TestVolumeStats_SpikeRatioCorrect(t *testing.T) {
	candles := makeVolumeCandles(100, 100, 100, 100, 500)
	avg, last := volumeStats(candles, 20)
	if avg <= 0 {
		t.Fatal("avg is zero, cannot compute ratio")
	}
	ratio := last / avg
	if ratio != 5.0 {
		t.Errorf("ratio = %.4f, want 5.0", ratio)
	}
}

// Case 2: single candle — no prior history, avg must be 0, no panic.
func TestVolumeStats_SingleCandle(t *testing.T) {
	candles := makeVolumeCandles(1_000_000)
	avg, last := volumeStats(candles, 20)
	if last != 1_000_000 {
		t.Errorf("last = %.0f, want 1000000", last)
	}
	if avg != 0 {
		t.Errorf("avg = %.2f, want 0 for single candle", avg)
	}
}

// Case 3: window larger than available history — uses all prior candles.
func TestVolumeStats_WindowLargerThanHistory(t *testing.T) {
	candles := makeVolumeCandles(200, 400, 300) // window=20, but only 2 prior candles
	avg, last := volumeStats(candles, 20)
	if last != 300 {
		t.Errorf("last = %.0f, want 300", last)
	}
	// Prior candles: 200, 400 → avg = 300
	if avg != 300 {
		t.Errorf("avg = %.2f, want 300", avg)
	}
}

// Case 4: empty slice — no panic, returns zeros.
func TestVolumeStats_EmptyCandles(t *testing.T) {
	avg, last := volumeStats(nil, 20)
	if avg != 0 || last != 0 {
		t.Errorf("expected (0, 0) for empty candles, got (%.2f, %.2f)", avg, last)
	}
}

// Case 5: window exactly equals prior history length.
func TestVolumeStats_WindowEqualsHistory(t *testing.T) {
	candles := makeVolumeCandles(10, 20, 30, 999)
	// window=3 — prior history also has 3 candles
	avg, last := volumeStats(candles, 3)
	if last != 999 {
		t.Errorf("last = %.0f, want 999", last)
	}
	// (10+20+30)/3 = 20
	if avg != 20 {
		t.Errorf("avg = %.2f, want 20", avg)
	}
}

// Case 6: window smaller than prior history — only uses the most recent N.
func TestVolumeStats_WindowSubsetsHistory(t *testing.T) {
	candles := makeVolumeCandles(9999, 100, 100, 100, 500)
	// window=3 → prior history = [9999, 100, 100, 100], use last 3 = [100, 100, 100]
	avg, last := volumeStats(candles, 3)
	if last != 500 {
		t.Errorf("last = %.0f, want 500", last)
	}
	// old spike (9999) must be outside the window
	if avg != 100 {
		t.Errorf("avg = %.2f, want 100 (window should exclude old spike 9999)", avg)
	}
}
