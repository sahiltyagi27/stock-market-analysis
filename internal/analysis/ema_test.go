package analysis_test

import (
	"math"
	"testing"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
)

const epsilon = 1e-6

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < epsilon
}

// --- EMA (full series) ---

func TestEMA_SeedEqualsFirstSMA(t *testing.T) {
	// First EMA value must equal the SMA of the seed window.
	prices := []float64{10, 20, 30, 40, 50}
	got, err := analysis.EMA(prices, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Seed = SMA(10,20,30) = 20
	if !approxEqual(got[2], 20.0) {
		t.Errorf("seed EMA = %.6f, want 20.0", got[2])
	}
}

func TestEMA_KnownValues(t *testing.T) {
	// Manually verified 3-period EMA for a short series.
	// k = 2/(3+1) = 0.5
	// prices: 2, 4, 6, 8, 10
	// seed (idx 2): SMA(2,4,6) = 4
	// idx 3: 8*0.5 + 4*0.5 = 6
	// idx 4: 10*0.5 + 6*0.5 = 8
	prices := []float64{2, 4, 6, 8, 10}
	got, err := analysis.EMA(prices, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[int]float64{2: 4, 3: 6, 4: 8}
	for i, w := range want {
		if !approxEqual(got[i], w) {
			t.Errorf("EMA[%d] = %.6f, want %.6f", i, got[i], w)
		}
	}
}

func TestEMA_Period1_EqualsInput(t *testing.T) {
	// k = 2/(1+1) = 1, so EMA always equals the latest price.
	prices := []float64{5, 10, 15}
	got, err := analysis.EMA(prices, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, p := range prices {
		if !approxEqual(got[i], p) {
			t.Errorf("EMA[%d] = %.6f, want %.6f", i, got[i], p)
		}
	}
}

func TestEMA_LengthMatchesInput(t *testing.T) {
	prices := make([]float64, 25)
	for i := range prices {
		prices[i] = float64(i + 1)
	}
	got, err := analysis.EMA(prices, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(prices) {
		t.Errorf("len(EMA) = %d, want %d", len(got), len(prices))
	}
}

func TestEMA_InsufficientData(t *testing.T) {
	_, err := analysis.EMA([]float64{1, 2, 3}, 10)
	if err == nil {
		t.Error("expected error for insufficient data, got nil")
	}
}

func TestEMA_EmptyPrices(t *testing.T) {
	_, err := analysis.EMA([]float64{}, 5)
	if err == nil {
		t.Error("expected error for empty prices, got nil")
	}
}

func TestEMA_InvalidPeriod(t *testing.T) {
	_, err := analysis.EMA([]float64{1, 2, 3}, 0)
	if err == nil {
		t.Error("expected error for period=0, got nil")
	}
}

// --- CurrentEMA ---

func TestCurrentEMA_MatchesLastEMA(t *testing.T) {
	prices := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}
	period := 5
	full, _ := analysis.EMA(prices, period)
	current, err := analysis.CurrentEMA(prices, period)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approxEqual(current, full[len(full)-1]) {
		t.Errorf("CurrentEMA = %.6f, want %.6f", current, full[len(full)-1])
	}
}

func TestCurrentEMA_ExactlyPeriodLength(t *testing.T) {
	// With exactly `period` prices, CurrentEMA == SMA of all prices.
	prices := []float64{10, 20, 30}
	got, err := analysis.CurrentEMA(prices, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approxEqual(got, 20.0) {
		t.Errorf("CurrentEMA = %.6f, want 20.0", got)
	}
}

// --- ComputeEMAs ---

func TestComputeEMAs_AllPeriods(t *testing.T) {
	// 200 ascending prices so all three periods can be computed.
	prices := make([]float64, 200)
	for i := range prices {
		prices[i] = float64(i + 1)
	}
	r := analysis.ComputeEMAs(prices)
	if r.EMA10 == 0 {
		t.Error("EMA10 should be non-zero")
	}
	if r.EMA50 == 0 {
		t.Error("EMA50 should be non-zero")
	}
	if r.EMA200 == 0 {
		t.Error("EMA200 should be non-zero")
	}
	// For a monotonically increasing series EMA10 > EMA50 > EMA200
	// because EMA10 reacts fastest to recent (higher) prices.
	if !(r.EMA10 > r.EMA50 && r.EMA50 > r.EMA200) {
		t.Errorf("expected EMA10 > EMA50 > EMA200, got %.4f / %.4f / %.4f",
			r.EMA10, r.EMA50, r.EMA200)
	}
}

func TestComputeEMAs_PartialData(t *testing.T) {
	// Only 15 prices — EMA10 computable, EMA50 and EMA200 should be zero.
	prices := make([]float64, 15)
	for i := range prices {
		prices[i] = float64(i + 1)
	}
	r := analysis.ComputeEMAs(prices)
	if r.EMA10 == 0 {
		t.Error("EMA10 should be non-zero with 15 prices")
	}
	if r.EMA50 != 0 {
		t.Errorf("EMA50 should be zero with 15 prices, got %.4f", r.EMA50)
	}
	if r.EMA200 != 0 {
		t.Errorf("EMA200 should be zero with 15 prices, got %.4f", r.EMA200)
	}
}

func TestComputeEMAs_InsufficientForAll(t *testing.T) {
	prices := []float64{1, 2, 3}
	r := analysis.ComputeEMAs(prices)
	if r.EMA10 != 0 || r.EMA50 != 0 || r.EMA200 != 0 {
		t.Errorf("all EMAs should be zero with only 3 prices, got %+v", r)
	}
}

func TestComputeEMAs_ConstantPrices(t *testing.T) {
	// EMA of a flat series must equal that constant.
	prices := make([]float64, 200)
	for i := range prices {
		prices[i] = 42.0
	}
	r := analysis.ComputeEMAs(prices)
	for _, v := range []float64{r.EMA10, r.EMA50, r.EMA200} {
		if !approxEqual(v, 42.0) {
			t.Errorf("expected EMA = 42.0 for constant prices, got %.6f", v)
		}
	}
}
