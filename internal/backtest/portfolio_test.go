package backtest

import (
	"testing"
	"time"

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
