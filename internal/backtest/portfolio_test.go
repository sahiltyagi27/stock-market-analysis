package backtest

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/crossover"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// pfTestCandle is a small helper for portfolio exit tests.
func pfTestCandle(o, h, l, c float64) models.Candle {
	return models.Candle{Open: o, High: h, Low: l, Close: c}
}

// buildSymData wraps candles + EMA series into a *symData for checkExit tests.
func buildSymData(candles []models.Candle, ema7, ema21 []float64) *symData {
	di := make(map[string]int, len(candles))
	for i := range candles {
		di[time.Time{}.AddDate(0, 0, i).Format(dayFmt)] = i
	}
	return &symData{candles: candles, ema7: ema7, ema21: ema21, dateIdx: di}
}

func TestCheckExit_StopLossFirst(t *testing.T) {
	// Candle hits both SL and a target — SL must win (pessimistic).
	sd := buildSymData(
		[]models.Candle{pfTestCandle(100, 130, 85, 100)},
		[]float64{110}, []float64{105},
	)
	pos := &pfPosition{entry: 100, sl: 90, target: 120, entryIdx: -1}
	price, outcome, exited := checkExit(pos, sd, 0, PortfolioOptions{ExitMode: "target"})
	if !exited || outcome != OutcomeLoss || price != 90 {
		t.Fatalf("expected SL loss at 90, got exited=%v outcome=%s price=%.2f", exited, outcome, price)
	}
}

func TestCheckExit_TargetMode(t *testing.T) {
	sd := buildSymData(
		[]models.Candle{pfTestCandle(100, 125, 99, 124)},
		[]float64{110}, []float64{105},
	)
	pos := &pfPosition{entry: 100, sl: 90, target: 120, entryIdx: -1}
	price, outcome, exited := checkExit(pos, sd, 0, PortfolioOptions{ExitMode: "target"})
	if !exited || outcome != OutcomeWin || price != 120 {
		t.Fatalf("expected target win at 120, got exited=%v outcome=%s price=%.2f", exited, outcome, price)
	}
}

func TestCheckExit_EMARecross(t *testing.T) {
	// EMA7 below EMA21 at idx 1 → exit at close. idx must be > entryIdx.
	sd := buildSymData(
		[]models.Candle{pfTestCandle(100, 105, 99, 102), pfTestCandle(102, 106, 100, 101)},
		[]float64{110, 104}, // ema7
		[]float64{105, 106}, // ema21: at idx1, 104 < 106 → recross
	)
	pos := &pfPosition{entry: 100, sl: 90, target: 200, entryIdx: 0}
	price, outcome, exited := checkExit(pos, sd, 1, PortfolioOptions{ExitMode: "ema"})
	if !exited || outcome != OutcomeWin || price != 101 {
		t.Fatalf("expected EMA-recross exit at close 101, got exited=%v outcome=%s price=%.2f", exited, outcome, price)
	}
}

func TestCheckExit_EMA_NoExitOnEntryDay(t *testing.T) {
	// Even if EMA7<EMA21 on the entry candle, no exit (idx == entryIdx).
	sd := buildSymData(
		[]models.Candle{pfTestCandle(100, 105, 99, 102)},
		[]float64{104}, []float64{106},
	)
	pos := &pfPosition{entry: 100, sl: 90, target: 200, entryIdx: 0}
	_, _, exited := checkExit(pos, sd, 0, PortfolioOptions{ExitMode: "ema"})
	if exited {
		t.Fatal("must not exit on the entry day itself")
	}
}

func TestCheckExit_MaxHoldTimeout(t *testing.T) {
	// No SL/target/recross, but hold cap reached.
	sd := buildSymData(
		[]models.Candle{pfTestCandle(100, 105, 99, 103), pfTestCandle(103, 106, 100, 104)},
		[]float64{110, 110}, []float64{105, 105}, // ema7 stays above ema21
	)
	pos := &pfPosition{entry: 100, sl: 90, target: 200, entryIdx: 0}
	price, outcome, exited := checkExit(pos, sd, 1, PortfolioOptions{ExitMode: "ema", MaxHoldDays: 1})
	if !exited || outcome != OutcomeTimeout || price != 104 {
		t.Fatalf("expected timeout at close 104, got exited=%v outcome=%s price=%.2f", exited, outcome, price)
	}
}

func TestCostMath_SlippageDirection(t *testing.T) {
	// Buys fill higher, sells fill lower.
	if got := buyFill(100, 0.002); got != 100.2 {
		t.Fatalf("buyFill = %.4f, want 100.2", got)
	}
	if got := sellFill(100, 0.002); got != 99.8 {
		t.Fatalf("sellFill = %.4f, want 99.8", got)
	}
	// Zero slippage = candle price.
	if buyFill(100, 0) != 100 || sellFill(100, 0) != 100 {
		t.Fatal("zero slippage must leave price unchanged")
	}
}

func TestCostMath_RoundTripLosesToCosts(t *testing.T) {
	// Buy and sell 10 shares at the same price 100, with cost but no slippage.
	// A frictionless round-trip nets zero; with cost it must lose money.
	legCost := 0.25 / 100 / 2 // 0.25% round-trip
	out := cashOut(10, 100, legCost)
	in := cashIn(10, 100, legCost)
	if in >= out {
		t.Fatalf("round-trip should lose to costs: out=%.4f in=%.4f", out, in)
	}
	// Frictionless: in == out.
	if cashIn(10, 100, 0) != cashOut(10, 100, 0) {
		t.Fatal("frictionless round-trip must net zero")
	}
}

func TestPortfolioOptions_CostHelpers(t *testing.T) {
	o := PortfolioOptions{CostPct: 0.25, SlippagePct: 0.20}
	if o.slip() != 0.002 {
		t.Fatalf("slip = %.5f, want 0.002", o.slip())
	}
	if o.legCost() != 0.00125 {
		t.Fatalf("legCost = %.6f, want 0.00125", o.legCost())
	}
}

func TestPositionNotional_EqualSlice(t *testing.T) {
	// Negative RiskPct = equal 1/N slices = equity / MaxPositions, capped at cash.
	// (RunPortfolio now defaults RiskPct 0 → 1.0, so equal-slice is opt-in.)
	o := PortfolioOptions{MaxPositions: 5, RiskPct: -1}
	got := positionNotional(100000, 100000, 100, 90, o)
	if got != 20000 {
		t.Fatalf("equal slice = %.2f, want 20000", got)
	}
	// Capped at available cash.
	got = positionNotional(100000, 15000, 100, 90, o)
	if got != 15000 {
		t.Fatalf("cash-capped = %.2f, want 15000", got)
	}
}

func TestPositionNotional_RiskBased(t *testing.T) {
	// risk 1%, stop 5% away → notional = equity*0.01/0.05 = 20% of equity.
	o := PortfolioOptions{MaxPositions: 5, RiskPct: 1, MaxWeightPct: 25}
	got := positionNotional(100000, 100000, 100, 95, o)
	if got != 20000 {
		t.Fatalf("risk-based = %.2f, want 20000 (20%%)", got)
	}
	// Tighter stop (2% away) → 50% raw, capped at MaxWeightPct 25% = 25000.
	got = positionNotional(100000, 100000, 100, 98, o)
	if got != 25000 {
		t.Fatalf("max-weight-capped = %.2f, want 25000", got)
	}
	// Wide stop (10% away) → 10% of equity = 10000 (smaller position).
	got = positionNotional(100000, 100000, 100, 90, o)
	if got != 10000 {
		t.Fatalf("wide-stop = %.2f, want 10000 (10%%)", got)
	}
}

func TestStrategyHealthy(t *testing.T) {
	// Disabled / warmup → always healthy.
	if !strategyHealthy([]float64{-1, -1}, PortfolioOptions{StrategyHealthWindow: 0}) {
		t.Fatal("window 0 must be always healthy")
	}
	if !strategyHealthy([]float64{-1, -1}, PortfolioOptions{StrategyHealthWindow: 3}) {
		t.Fatal("warmup (fewer than window) must be healthy")
	}
	// avgr: last 3 = [-1,-1,+1] → mean -0.33 < 0 → unhealthy.
	if strategyHealthy([]float64{2, 2, -1, -1, 1}, PortfolioOptions{StrategyHealthWindow: 3, StrategyHealthMode: "avgr", StrategyHealthMin: 0}) {
		t.Fatal("negative mean R over window must be unhealthy")
	}
	// avgr: last 3 = [+3,-1,+1] → mean +1 ≥ 0 → healthy.
	if !strategyHealthy([]float64{-5, 3, -1, 1}, PortfolioOptions{StrategyHealthWindow: 3, StrategyHealthMode: "avgr", StrategyHealthMin: 0}) {
		t.Fatal("positive mean R over window must be healthy")
	}
	// pf: last 4 = [+3,+1,-1,-1] → PF = 4/2 = 2.0 ≥ 1.2 → healthy.
	if !strategyHealthy([]float64{3, 1, -1, -1}, PortfolioOptions{StrategyHealthWindow: 4, StrategyHealthMode: "pf", StrategyHealthMin: 1.2}) {
		t.Fatal("PF 2.0 must clear min 1.2")
	}
	// pf: [+1,-1,-1,-1] → PF = 1/3 ≈ 0.33 < 1.2 → unhealthy.
	if strategyHealthy([]float64{1, -1, -1, -1}, PortfolioOptions{StrategyHealthWindow: 4, StrategyHealthMode: "pf", StrategyHealthMin: 1.2}) {
		t.Fatal("PF 0.33 must fail min 1.2")
	}
}

// syntheticCrossSeries builds a rising-but-oscillating price series that fires
// repeated, *winning* EMA7×EMA21 crossover signals — enough up-drift that each
// crossover trade is profitable, enough ripple that crossovers actually occur.
func syntheticCrossSeries(sym string, n int, phase float64) []models.Candle {
	out := make([]models.Candle, n)
	base := time.Date(2022, 1, 3, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		c := 1000.0 + float64(i)*2.5 + 70*math.Sin((float64(i)+phase)/5.0)
		out[i] = models.Candle{
			Symbol: sym, Timestamp: base.AddDate(0, 0, i),
			Open: c * 0.999, High: c * 1.015, Low: c * 0.985, Close: c, Volume: 500000,
		}
	}
	return out
}

func crossPortfolioOpts() PortfolioOptions {
	return PortfolioOptions{
		From:        time.Date(2022, 4, 1, 0, 0, 0, 0, time.UTC),
		To:          time.Date(2022, 11, 1, 0, 0, 0, 0, time.UTC),
		ExitMode:    "ema",
		MaxHoldDays: 12,
		EngineOpts: Options{Mode: "crossover", CrossoverOpts: crossover.Options{
			MaxCrossoverAge: 2, MinRR: 0.1, MinTargetPct: -1, MinRiskPct: -1,
			MinCandles: 50, VolumeWindow: 20,
		}},
	}
}

func crossPortfolioData() map[string][]models.Candle {
	data := map[string][]models.Candle{}
	for i, sym := range []string{"AAA", "BBB", "CCC", "DDD", "EEE", "FFF"} {
		data[sym] = syntheticCrossSeries(sym, 300, float64(i)*2)
	}
	return data
}

// TestHealthGate_ShadowReopensClosedGate is the regression test for the
// one-way-door fix. With the health gate seeded CLOSED:
//   - without shadow, no trade ever closes → the window never refreshes → the
//     gate is locked shut forever → zero trades;
//   - with shadow, the strategy keeps simulating winning trades while flat,
//     their R refreshes the window, and the gate reopens → real trades happen.
func TestHealthGate_ShadowReopensClosedGate(t *testing.T) {
	data := crossPortfolioData()

	// Sanity: with the gate disabled the synthetic data does produce signals.
	base := crossPortfolioOpts()
	base.StrategyHealthWindow = 0
	if tr, _ := RunPortfolio(context.Background(), data, base); len(tr) == 0 {
		t.Fatal("synthetic data produced no trades even with the gate off — test fixture is broken")
	}

	seededClosed := func(shadow bool) int {
		o := crossPortfolioOpts()
		o.StrategyHealthWindow = 5
		o.StrategyHealthMode = "avgr"
		o.StrategyHealthMin = 0
		o.HealthSeed = []float64{-1, -1, -1, -1, -1} // start the gate CLOSED
		o.StrategyHealthShadow = shadow
		tr, _ := RunPortfolio(context.Background(), data, o)
		return len(tr)
	}

	if locked := seededClosed(false); locked != 0 {
		t.Fatalf("without shadow a gate seeded closed must stay locked (0 trades), got %d", locked)
	}
	if reopened := seededClosed(true); reopened == 0 {
		t.Fatal("with shadow the gate must reopen once shadow trades turn healthy, but it produced 0 trades")
	}
}

func TestCheckExit_NoExit(t *testing.T) {
	sd := buildSymData(
		[]models.Candle{pfTestCandle(100, 105, 99, 103), pfTestCandle(103, 106, 100, 104)},
		[]float64{110, 110}, []float64{105, 105},
	)
	pos := &pfPosition{entry: 100, sl: 90, target: 200, entryIdx: 0}
	_, _, exited := checkExit(pos, sd, 1, PortfolioOptions{ExitMode: "ema"})
	if exited {
		t.Fatal("expected no exit when SL/target/recross/timeout none triggered")
	}
}

func TestComputeSwingLows_DetectsConfirmedLocalMin(t *testing.T) {
	// Lows: 100, 95, 90, 95, 100 — index 2 (90) is the only confirmed local
	// min within a ±1 window (both neighbours strictly higher).
	candles := []models.Candle{
		pfTestCandle(100, 100, 100, 100),
		pfTestCandle(95, 95, 95, 95),
		pfTestCandle(90, 90, 90, 90),
		pfTestCandle(95, 95, 95, 95),
		pfTestCandle(100, 100, 100, 100),
	}
	got := computeSwingLows(candles, 1)
	want := []float64{0, 0, 90, 0, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %.2f, want %.2f (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestComputeSwingLows_TiesDoNotCount(t *testing.T) {
	// A flat double-bottom within the window: neither candle at the tied low
	// is a strict local min (this scanner requires an unambiguous single low,
	// not a double-bottom, matching analysis.FindZones' tie-handling). The
	// two tied lows must be within ±window of each other to see one another.
	candles := []models.Candle{
		pfTestCandle(100, 100, 100, 100),
		pfTestCandle(90, 90, 90, 90),
		pfTestCandle(95, 95, 95, 95),
		pfTestCandle(90, 90, 90, 90),
		pfTestCandle(100, 100, 100, 100),
	}
	got := computeSwingLows(candles, 2)
	for i, v := range got {
		if v != 0 {
			t.Fatalf("index %d: expected no confirmed low on a tie, got %.2f", i, v)
		}
	}
}

func TestUpdateTrailingStop_RatchetsUpOnlyAfterConfirmation(t *testing.T) {
	// Swing low of 95 sits at index 2; with window=1 it confirms at index 3.
	candles := []models.Candle{
		pfTestCandle(100, 100, 100, 100), // 0: entry day
		pfTestCandle(97, 97, 97, 97),     // 1
		pfTestCandle(95, 95, 95, 95),     // 2: the low
		pfTestCandle(98, 98, 98, 98),     // 3: confirms index 2 (window=1)
		pfTestCandle(101, 101, 101, 101), // 4
	}
	sd := &symData{candles: candles, swingLow: computeSwingLows(candles, 1)}
	pos := &pfPosition{entry: 100, sl: 90, initialSL: 90, entryIdx: 0}

	for idx := 1; idx <= 2; idx++ {
		updateTrailingStop(pos, sd, idx, 1)
		if pos.sl != 90 {
			t.Fatalf("idx %d: stop should not move before confirmation, got %.2f", idx, pos.sl)
		}
	}
	updateTrailingStop(pos, sd, 3, 1)
	if pos.sl != 95 {
		t.Fatalf("idx 3: expected stop ratcheted to confirmed swing low 95, got %.2f", pos.sl)
	}
	// A later, lower confirmed low must never pull the stop back down.
	pos.sl = 95
	updateTrailingStop(pos, sd, 4, 1)
	if pos.sl != 95 {
		t.Fatalf("stop must only ratchet up, got %.2f", pos.sl)
	}
}

func TestCheckExit_StructureMode_HoldsThroughPullbackAboveStop(t *testing.T) {
	// A pullback candle whose Low sits above the trailing stop, with EMA7 also
	// below EMA21 (which would exit under "ema" mode) — structure mode must
	// hold anyway; only the trailing SL and MaxHoldDays end a structure trade.
	sd := buildSymData(
		[]models.Candle{pfTestCandle(100, 100, 100, 100), pfTestCandle(100, 102, 96, 99)},
		[]float64{110, 90}, []float64{105, 105},
	)
	pos := &pfPosition{entry: 100, sl: 90, initialSL: 90, target: 200, entryIdx: 0}
	_, _, exited := checkExit(pos, sd, 1, PortfolioOptions{ExitMode: "structure"})
	if exited {
		t.Fatal("structure mode should hold through a pullback that doesn't breach the trailing stop")
	}
}

func TestCheckExit_StructureMode_TrailStopOutcome(t *testing.T) {
	// A ratcheted stop (above entry) gets hit — this is a protected-profit
	// exit, not a hard loss, so it must report OutcomeTrailStop.
	sd := buildSymData(
		[]models.Candle{pfTestCandle(100, 100, 100, 100), pfTestCandle(110, 112, 104, 108)},
		[]float64{110, 110}, []float64{105, 105},
	)
	pos := &pfPosition{entry: 100, sl: 105, initialSL: 90, target: 200, entryIdx: 0}
	price, outcome, exited := checkExit(pos, sd, 1, PortfolioOptions{ExitMode: "structure"})
	if !exited || outcome != OutcomeTrailStop || price != 105 {
		t.Fatalf("expected trail-stop exit at 105, got exited=%v outcome=%s price=%.2f", exited, outcome, price)
	}
}

func TestCheckExit_TrendStopMode_HoldsWhileAboveEMA(t *testing.T) {
	// Even a deep pullback that would trigger an "ema" (EMA7<EMA21) exit must
	// be tolerated under trendstop, as long as the close stays above the
	// long trend EMA — this mode is meant to survive the 50-60% mid-journey
	// drawdowns ANALYSIS.md S15 found in real multibagger price histories.
	candles := []models.Candle{
		pfTestCandle(100, 100, 100, 100),
		pfTestCandle(60, 65, 55, 62), // a brutal pullback, still above trendStopEMA
	}
	sd := &symData{
		candles:      candles,
		ema7:         []float64{110, 70}, // would trigger "ema" mode's recross exit
		ema21:        []float64{105, 105},
		trendStopEMA: []float64{50, 50},
	}
	pos := &pfPosition{entry: 100, sl: 40, initialSL: 40, entryIdx: 0}
	_, _, exited := checkExit(pos, sd, 1, PortfolioOptions{ExitMode: "trendstop"})
	if exited {
		t.Fatal("trendstop mode should hold through a deep pullback that stays above the trend EMA")
	}
}

func TestCheckExit_TrendStopMode_ExitsBelowEMA(t *testing.T) {
	candles := []models.Candle{
		pfTestCandle(100, 100, 100, 100),
		pfTestCandle(48, 50, 45, 47), // close breaks below trendStopEMA
	}
	sd := &symData{
		candles:      candles,
		ema7:         []float64{110, 60},
		ema21:        []float64{105, 105},
		trendStopEMA: []float64{50, 50},
	}
	pos := &pfPosition{entry: 100, sl: 40, initialSL: 40, entryIdx: 0}
	price, outcome, exited := checkExit(pos, sd, 1, PortfolioOptions{ExitMode: "trendstop"})
	if !exited || outcome != OutcomeTrailStop || price != 47 {
		t.Fatalf("expected trend-stop exit at 47, got exited=%v outcome=%s price=%.2f", exited, outcome, price)
	}
}
