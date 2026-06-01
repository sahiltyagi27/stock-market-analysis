package backtest

import (
	"testing"

	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// makeCandles builds a minimal candle slice for walkForward tests.
// Each candle has Open=o, High=h, Low=l, Close=c.
func makeCandle(o, h, l, c float64) models.Candle {
	return models.Candle{Open: o, High: h, Low: l, Close: c}
}

// ── walkForward tests ──────────────────────────────────────────────────────────

func TestWalkForward_TargetHit(t *testing.T) {
	candles := []models.Candle{
		makeCandle(100, 102, 99, 101),  // day 1: no resolution
		makeCandle(101, 103, 100, 103), // day 2: no resolution
		makeCandle(103, 115, 102, 115), // day 3: High 115 >= target 110 → WIN
	}
	outcome, exitPrice, holdDays := walkForward(100, 90, 110, candles, 20)

	if outcome != OutcomeWin {
		t.Fatalf("expected Win, got %s", outcome)
	}
	if exitPrice != 110 {
		t.Fatalf("expected exit at target 110, got %.2f", exitPrice)
	}
	if holdDays != 3 {
		t.Fatalf("expected holdDays=3, got %d", holdDays)
	}
}

func TestWalkForward_SLHit(t *testing.T) {
	candles := []models.Candle{
		makeCandle(100, 102, 99, 101),  // day 1: no resolution
		makeCandle(101, 102, 88, 89),   // day 2: Low 88 <= sl 90 → LOSS
		makeCandle(89, 105, 88, 104),   // day 3: never reached
	}
	outcome, exitPrice, holdDays := walkForward(100, 90, 120, candles, 20)

	if outcome != OutcomeLoss {
		t.Fatalf("expected Loss, got %s", outcome)
	}
	if exitPrice != 90 {
		t.Fatalf("expected exit at SL 90, got %.2f", exitPrice)
	}
	if holdDays != 2 {
		t.Fatalf("expected holdDays=2, got %d", holdDays)
	}
}

func TestWalkForward_Timeout(t *testing.T) {
	// candle range never reaches SL (92) or target (120).
	candle := makeCandle(100, 105, 95, 102)
	candles := make([]models.Candle, 5)
	for i := range candles {
		candles[i] = candle
	}

	outcome, _, holdDays := walkForward(100, 92, 120, candles, 5)

	if outcome != OutcomeTimeout {
		t.Fatalf("expected Timeout, got %s", outcome)
	}
	if holdDays != 5 {
		t.Fatalf("expected holdDays=5 (maxHold), got %d", holdDays)
	}
}

// TestWalkForward_SameCandlePessimistic ensures that when a candle triggers
// both SL (Low ≤ SL) and Target (High ≥ Target) on the same bar, the
// pessimistic convention records a loss.
func TestWalkForward_SameCandlePessimistic(t *testing.T) {
	candles := []models.Candle{
		makeCandle(100, 130, 85, 100), // High 130 >= target 120; Low 85 <= sl 90
	}
	outcome, exitPrice, holdDays := walkForward(100, 90, 120, candles, 20)

	if outcome != OutcomeLoss {
		t.Fatalf("expected pessimistic Loss, got %s", outcome)
	}
	if exitPrice != 90 {
		t.Fatalf("expected exit at SL 90, got %.2f", exitPrice)
	}
	if holdDays != 1 {
		t.Fatalf("expected holdDays=1, got %d", holdDays)
	}
}

// TestWalkForward_ActualRR_Win checks that the caller can compute a correct
// positive ActualRR from the returned exitPrice.
func TestWalkForward_ExitPrice_Win(t *testing.T) {
	candles := []models.Candle{
		makeCandle(100, 125, 99, 125), // High 125 >= target 120 → WIN at 120
	}
	_, exitPrice, _ := walkForward(100, 90, 120, candles, 20)
	// actualRR = (120 - 100) / (100 - 90) = 2.0
	risk := 100.0 - 90.0
	rr := (exitPrice - 100.0) / risk
	if rr != 2.0 {
		t.Fatalf("expected ActualRR=2.0, got %.4f", rr)
	}
}

// TestWalkForward_ExhaustsHistory verifies a graceful timeout when we run out
// of candles before SL or Target is hit.
func TestWalkForward_ExhaustsHistory(t *testing.T) {
	candle := makeCandle(100, 105, 95, 103)
	candles := []models.Candle{candle, candle} // only 2 candles, maxHold=20

	outcome, exitPrice, holdDays := walkForward(100, 90, 120, candles, 20)

	if outcome != OutcomeTimeout {
		t.Fatalf("expected Timeout from exhausted history, got %s", outcome)
	}
	if exitPrice != candle.Close {
		t.Fatalf("expected exit at last close %.2f, got %.2f", candle.Close, exitPrice)
	}
	if holdDays != 2 {
		t.Fatalf("expected holdDays=2 (len of candles), got %d", holdDays)
	}
}

// TestWalkForward_EmptyCandles handles the degenerate zero-candle case.
func TestWalkForward_EmptyCandles(t *testing.T) {
	outcome, _, holdDays := walkForward(100, 90, 120, nil, 20)
	if outcome != OutcomeTimeout {
		t.Fatalf("expected Timeout for empty candles, got %s", outcome)
	}
	if holdDays != 1 {
		t.Fatalf("expected holdDays=1, got %d", holdDays)
	}
}

// TestWalkForward_HitOnFirstCandle ensures holdDays=1 when resolved immediately.
func TestWalkForward_HitOnFirstCandle(t *testing.T) {
	candles := []models.Candle{
		makeCandle(100, 122, 99, 121), // High 122 >= target 120
	}
	_, _, holdDays := walkForward(100, 90, 120, candles, 20)
	if holdDays != 1 {
		t.Fatalf("expected holdDays=1, got %d", holdDays)
	}
}
