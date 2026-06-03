package paper

import (
	"testing"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

func TestRiskNotional(t *testing.T) {
	c := Config{MaxPositions: 5, RiskPct: 1, MaxWeightPct: 25}
	// risk 1%, stop 5% away → 20% of equity.
	if got := riskNotional(100000, 100000, 100, 95, c); got != 20000 {
		t.Fatalf("risk-based = %.2f, want 20000", got)
	}
	// tight stop (2%) → 50% raw, capped at 25%.
	if got := riskNotional(100000, 100000, 100, 98, c); got != 25000 {
		t.Fatalf("cap = %.2f, want 25000", got)
	}
	// cash-limited.
	if got := riskNotional(100000, 8000, 100, 95, c); got != 8000 {
		t.Fatalf("cash cap = %.2f, want 8000", got)
	}
	// equal-slice when RiskPct <= 0.
	eq := Config{MaxPositions: 4, RiskPct: 0}
	if got := riskNotional(100000, 100000, 100, 90, eq); got != 25000 {
		t.Fatalf("equal slice = %.2f, want 25000", got)
	}
}

func TestHealthyAvgR(t *testing.T) {
	if !healthyAvgR([]float64{-1, -1}, 0, 0) {
		t.Fatal("window 0 = always healthy")
	}
	if !healthyAvgR([]float64{-1, -1}, 5, 0) {
		t.Fatal("warmup = healthy")
	}
	if healthyAvgR([]float64{2, -1, -1, -1}, 3, 0) {
		t.Fatal("negative window mean = unhealthy")
	}
	if !healthyAvgR([]float64{-5, 3, -1, 1}, 3, 0) {
		t.Fatal("positive window mean = healthy")
	}
}

func TestFills(t *testing.T) {
	if buyFill(100, 0.002) != 100.2 || sellFill(100, 0.002) != 99.8 {
		t.Fatal("slippage direction wrong")
	}
}

func TestExitDecision(t *testing.T) {
	mk := func(o, h, l, c float64) models.Candle {
		return models.Candle{Open: o, High: h, Low: l, Close: c, Timestamp: time.Now()}
	}
	pos := store.PaperPosition{SL: 90}

	// SL hit (today low ≤ SL) → loss at SL.
	cc := []models.Candle{mk(100, 102, 88, 95)}
	if tr, oc, ex := exitDecision(pos, cc, cc[0]); !ex || oc != "loss" || tr != 90 {
		t.Fatalf("SL exit: got ex=%v oc=%s tr=%.2f", ex, oc, tr)
	}

	// EMA7 < EMA21 with no SL hit → exit at close.
	// Build a falling series so EMA7 dips below EMA21 at the end.
	var falling []models.Candle
	price := 200.0
	for i := 0; i < 40; i++ {
		price -= 1
		falling = append(falling, mk(price, price+1, price-0.5, price))
	}
	last := falling[len(falling)-1]
	posFar := store.PaperPosition{SL: 1} // SL far away so only EMA triggers
	if _, oc, ex := exitDecision(posFar, falling, last); !ex || oc != "exit" {
		t.Fatalf("EMA exit expected, got ex=%v oc=%s", ex, oc)
	}

	// Rising series, SL far → no exit.
	var rising []models.Candle
	price = 100
	for i := 0; i < 40; i++ {
		price += 1
		rising = append(rising, mk(price, price+1, price-0.5, price))
	}
	if _, _, ex := exitDecision(store.PaperPosition{SL: 1}, rising, rising[len(rising)-1]); ex {
		t.Fatal("rising series should not exit")
	}
}
