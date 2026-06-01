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

// noTrail is a convenience shorthand used by tests that don't care about the
// trailing-stop feature: passes atr=0 which disables trailing regardless of mult.
const noTrail = 0.0

// ── walkForward — fixed SL tests ──────────────────────────────────────────────

func TestWalkForward_TargetHit(t *testing.T) {
	candles := []models.Candle{
		makeCandle(100, 102, 99, 101),  // day 1: no resolution
		makeCandle(101, 103, 100, 103), // day 2: no resolution
		makeCandle(103, 115, 102, 115), // day 3: High 115 >= target 110 → WIN
	}
	outcome, exitPrice, holdDays := walkForward(100, 90, 110, noTrail, candles, 20, 1.5)

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
		makeCandle(100, 102, 99, 101), // day 1: no resolution
		makeCandle(101, 102, 88, 89),  // day 2: Low 88 <= sl 90 → LOSS
		makeCandle(89, 105, 88, 104),  // day 3: never reached
	}
	outcome, exitPrice, holdDays := walkForward(100, 90, 120, noTrail, candles, 20, 1.5)

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
	candle := makeCandle(100, 105, 95, 102)
	candles := make([]models.Candle, 5)
	for i := range candles {
		candles[i] = candle
	}
	outcome, _, holdDays := walkForward(100, 92, 120, noTrail, candles, 5, 1.5)
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
	outcome, exitPrice, holdDays := walkForward(100, 90, 120, noTrail, candles, 20, 1.5)

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

func TestWalkForward_ExitPrice_Win(t *testing.T) {
	candles := []models.Candle{
		makeCandle(100, 125, 99, 125), // High 125 >= target 120 → WIN at 120
	}
	_, exitPrice, _ := walkForward(100, 90, 120, noTrail, candles, 20, 1.5)
	risk := 100.0 - 90.0
	rr := (exitPrice - 100.0) / risk
	if rr != 2.0 {
		t.Fatalf("expected ActualRR=2.0, got %.4f", rr)
	}
}

func TestWalkForward_ExhaustsHistory(t *testing.T) {
	candle := makeCandle(100, 105, 95, 103)
	candles := []models.Candle{candle, candle}
	outcome, exitPrice, holdDays := walkForward(100, 90, 120, noTrail, candles, 20, 1.5)
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

func TestWalkForward_EmptyCandles(t *testing.T) {
	outcome, _, holdDays := walkForward(100, 90, 120, noTrail, nil, 20, 1.5)
	if outcome != OutcomeTimeout {
		t.Fatalf("expected Timeout for empty candles, got %s", outcome)
	}
	if holdDays != 1 {
		t.Fatalf("expected holdDays=1, got %d", holdDays)
	}
}

func TestWalkForward_HitOnFirstCandle(t *testing.T) {
	candles := []models.Candle{
		makeCandle(100, 122, 99, 121), // High 122 >= target 120
	}
	_, _, holdDays := walkForward(100, 90, 120, noTrail, candles, 20, 1.5)
	if holdDays != 1 {
		t.Fatalf("expected holdDays=1, got %d", holdDays)
	}
}

// ── walkForward — ATR trailing stop tests ─────────────────────────────────────

// TestWalkForward_Trail_ProtectsProfitOnReversal verifies the core scenario:
// trade goes profitable, trailing stop rises, then a reversal hits the trail.
//
//	entry=100, sl=85, atr=5, trailMult=2.0 → trailBuffer=10
//	day1: High=115 > entry=100 → trailing activates; trailSL=105 > sl=85 → stopLevel=105
//	day2: Low=104 < stopLevel=105 → OutcomeTrailStop at 105 (+1.0R)
func TestWalkForward_Trail_ProtectsProfitOnReversal(t *testing.T) {
	candles := []models.Candle{
		makeCandle(101, 115, 100, 112), // day 1: High 115, not stopped
		makeCandle(110, 112, 104, 105), // day 2: Low 104 < trailSL 105 → trail exit
	}
	outcome, exitPrice, holdDays := walkForward(100, 85, 150, 5, candles, 20, 2.0)

	if outcome != OutcomeTrailStop {
		t.Fatalf("expected TrailStop, got %s", outcome)
	}
	// trailSL after day1: highestHigh=115, 115-10=105; stopLevel=105
	if exitPrice != 105 {
		t.Fatalf("expected exit at trailing stop 105, got %.2f", exitPrice)
	}
	if holdDays != 2 {
		t.Fatalf("expected holdDays=2, got %d", holdDays)
	}
	// ActualRR would be (105-100)/(100-85) = 5/15 ≈ +0.33R (profitable)
}

// TestWalkForward_Trail_DoesNotActivateBeforeEntry verifies that the trailing
// stop remains at the original SL when price never exceeds the entry.
//
//	entry=100, sl=90 — stock dips to 91 (above SL, below entry): no trailing
//	day2: stock rises to 130 → trailing activates, trailSL=120
//	day3: Low=119 < 120 → trail exit
func TestWalkForward_Trail_DoesNotActivateBeforeEntry(t *testing.T) {
	candles := []models.Candle{
		makeCandle(99, 99, 91, 95),    // day 1: High 99 < entry 100 → no trailing yet
		makeCandle(96, 130, 95, 125),  // day 2: High 130 → trailing activates; trailSL=120
		makeCandle(124, 125, 119, 120), // day 3: Low 119 < trailSL 120 → trail exit
	}
	// entry=100, sl=90, atr=5, mult=2.0 → buffer=10
	outcome, exitPrice, _ := walkForward(100, 90, 160, 5, candles, 20, 2.0)

	if outcome != OutcomeTrailStop {
		t.Fatalf("expected TrailStop, got %s (exitPrice=%.2f)", outcome, exitPrice)
	}
	if exitPrice != 120 {
		t.Fatalf("expected trail exit at 120, got %.2f", exitPrice)
	}
}

// TestWalkForward_Trail_OriginalSLStillFires confirms that if the stock never
// goes above entry, a drop to the original SL records OutcomeLoss, not TrailStop.
func TestWalkForward_Trail_OriginalSLStillFires(t *testing.T) {
	candles := []models.Candle{
		makeCandle(100, 99, 91, 95), // High 99 < entry 100 → no trailing
		makeCandle(95, 96, 88, 89),  // Low 88 <= sl 90 → plain loss
	}
	outcome, exitPrice, _ := walkForward(100, 90, 150, 5, candles, 20, 2.0)

	if outcome != OutcomeLoss {
		t.Fatalf("expected Loss (trailing never activated), got %s", outcome)
	}
	if exitPrice != 90 {
		t.Fatalf("expected exit at original SL 90, got %.2f", exitPrice)
	}
}

// TestWalkForward_Trail_TargetStillWins confirms target takes priority over
// trailing when they do NOT fire on the same candle.
func TestWalkForward_Trail_TargetStillWins(t *testing.T) {
	candles := []models.Candle{
		makeCandle(101, 115, 100, 112), // day 1: trailing activates, trailSL=105
		makeCandle(112, 160, 111, 155), // day 2: High 160 >= target 150 → WIN
	}
	outcome, exitPrice, _ := walkForward(100, 85, 150, 5, candles, 20, 2.0)

	if outcome != OutcomeWin {
		t.Fatalf("expected Win, got %s", outcome)
	}
	if exitPrice != 150 {
		t.Fatalf("expected exit at target 150, got %.2f", exitPrice)
	}
}

// TestWalkForward_Trail_SameCandlePessimistic: trailing stop and target both
// hit on the same candle → pessimistic → trailing stop wins (not target).
func TestWalkForward_Trail_SameCandlePessimistic(t *testing.T) {
	// day 1: High=115 > entry=100 → trailing activates; trailSL=105
	// day 2: Low=103 < trailSL=105 AND High=160 >= target=150
	// Pessimistic: stop is checked first → TrailStop
	candles := []models.Candle{
		makeCandle(101, 115, 100, 112),
		makeCandle(112, 160, 103, 140), // Low=103 < trailSL=105 AND High=160 >= 150
	}
	outcome, _, _ := walkForward(100, 85, 150, 5, candles, 20, 2.0)

	if outcome != OutcomeTrailStop {
		t.Fatalf("expected pessimistic TrailStop (not Win), got %s", outcome)
	}
}

// TestWalkForward_Trail_DisabledWhenMultNegative verifies that mult ≤ 0
// disables trailing entirely, even when atr > 0.
func TestWalkForward_Trail_DisabledWhenMultNegative(t *testing.T) {
	candles := []models.Candle{
		makeCandle(101, 130, 100, 125), // High 130 → would activate trail if enabled
		makeCandle(125, 132, 88, 90),   // Low 88 <= sl 90 → plain loss
	}
	outcome, exitPrice, _ := walkForward(100, 90, 160, 5, candles, 20, -1)

	if outcome != OutcomeLoss {
		t.Fatalf("expected Loss (trailing disabled), got %s", outcome)
	}
	if exitPrice != 90 {
		t.Fatalf("expected exit at original SL 90, got %.2f", exitPrice)
	}
}

// TestWalkForward_Trail_DisabledWhenATRZero verifies that atr=0 disables
// trailing regardless of the multiplier.
func TestWalkForward_Trail_DisabledWhenATRZero(t *testing.T) {
	candles := []models.Candle{
		makeCandle(101, 130, 100, 125),
		makeCandle(125, 132, 88, 90), // Low 88 <= sl 90 → plain loss
	}
	outcome, _, _ := walkForward(100, 90, 160, 0 /* atr=0 */, candles, 20, 2.0)

	if outcome != OutcomeLoss {
		t.Fatalf("expected Loss (atr=0 disables trailing), got %s", outcome)
	}
}
