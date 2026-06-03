package analysis

import (
	"math"
	"testing"
)

func TestRSI_Errors(t *testing.T) {
	if _, err := RSI(nil, 14); err == nil {
		t.Fatal("expected error for empty prices")
	}
	if _, err := RSI([]float64{1, 2, 3}, 0); err == nil {
		t.Fatal("expected error for period < 1")
	}
}

func TestRSI_NotSeededIsZero(t *testing.T) {
	// period+1 prices are required before the first value at index `period`.
	prices := []float64{10, 11, 12} // 3 prices, period 14 → never seeded
	out, err := RSI(prices, 14)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, v := range out {
		if v != 0 {
			t.Fatalf("index %d should be 0 (unseeded), got %v", i, v)
		}
	}
}

func TestRSI_AllGainsIs100(t *testing.T) {
	prices := []float64{1, 2, 3, 4, 5, 6, 7, 8}
	out, err := RSI(prices, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// First seeded index is `period` == 2.
	for i := 2; i < len(out); i++ {
		if math.Abs(out[i]-100) > 1e-9 {
			t.Fatalf("index %d: expected RSI 100 on an unbroken up-run, got %v", i, out[i])
		}
	}
}

func TestRSI_AllLossesIsZero(t *testing.T) {
	prices := []float64{8, 7, 6, 5, 4, 3, 2, 1}
	out, err := RSI(prices, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 2; i < len(out); i++ {
		if math.Abs(out[i]-0) > 1e-9 {
			t.Fatalf("index %d: expected RSI 0 on an unbroken down-run, got %v", i, out[i])
		}
	}
}

// TestRSI_KnownValue checks against the canonical Wilder worked example.
// Using the classic 14-period series from Wilder's book, RSI ≈ 70.46 then 66.25.
func TestRSI_KnownValue(t *testing.T) {
	prices := []float64{
		44.34, 44.09, 44.15, 43.61, 44.33, 44.83, 45.10, 45.42,
		45.84, 46.08, 45.89, 46.03, 45.61, 46.28, 46.28, 46.00,
		46.03, 46.41, 46.22, 45.64,
	}
	out, err := RSI(prices, 14)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Index 14 is the first seeded RSI; index 15 applies one smoothing step.
	if got := out[14]; math.Abs(got-70.46) > 0.5 {
		t.Fatalf("RSI[14]: expected ≈70.46, got %.2f", got)
	}
	if got := out[15]; math.Abs(got-66.25) > 0.6 {
		t.Fatalf("RSI[15]: expected ≈66.25, got %.2f", got)
	}
}

func TestRSI_OversoldDeepDip(t *testing.T) {
	// A steady up-trend followed by a sharp multi-day drop should push RSI(2) low.
	prices := []float64{10, 11, 12, 13, 14, 15, 16, 17, 18, 14, 11, 9}
	out, err := RSI(prices, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	last := out[len(out)-1]
	if last >= 10 {
		t.Fatalf("expected deeply oversold RSI(2) < 10 after sharp drop, got %.2f", last)
	}
}
