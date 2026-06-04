package signaleval

import (
	"math"
	"testing"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// mk builds a candle with a given OHLC on day i (flat-ish bars by default).
func mk(i int, o, h, l, c float64) models.Candle {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return models.Candle{Symbol: "T", Timestamp: base.AddDate(0, 0, i), Open: o, High: h, Low: l, Close: c, Volume: 1000}
}

// risingCloses builds n candles that rise steadily so EMA7 stays above EMA21
// (no recross), with tight ranges. Returns the slice.
func risingSeries(n int, start, step float64) []models.Candle {
	out := make([]models.Candle, n)
	p := start
	for i := 0; i < n; i++ {
		p += step
		out[i] = mk(i, p, p+0.2, p-0.2, p)
	}
	return out
}

func TestEvaluate_StopHit(t *testing.T) {
	c := risingSeries(40, 100, 1) // last close ≈ 140
	entryIdx := 30
	entry := c[entryIdx].Open
	sl := entry - 1.0
	// Make the candle right after entry gap down through the stop.
	c[entryIdx+1] = mk(entryIdx+1, entry-0.5, entry, sl-2, sl-1)

	tr := Evaluate(c, entryIdx, sl, Options{})
	if tr.Status != StatusStop {
		t.Fatalf("expected STOP, got %s", tr.Status)
	}
	if tr.NetPct >= 0 {
		t.Fatalf("a stop-out should be a net loss, got %+.2f%%", tr.NetPct)
	}
	if tr.HoldDays != 1 {
		t.Fatalf("expected hold 1, got %d", tr.HoldDays)
	}
}

func TestEvaluate_RecrossExit(t *testing.T) {
	// Rise to seed EMAs above, then a sustained drop forces EMA7 < EMA21
	// without breaching a wide stop. Long tail gives the cross room to happen.
	c := risingSeries(50, 100, 1)
	entryIdx := 30
	entry := c[entryIdx].Open
	sl := entry - 80 // very wide — never hit
	for i := entryIdx + 1; i < len(c); i++ {
		p := c[i-1].Close - 2.0
		c[i] = mk(i, p, p+0.2, p-0.2, p) // steady decline, low stays above sl
	}
	tr := Evaluate(c, entryIdx, sl, Options{})
	if tr.Status != StatusRecross {
		t.Fatalf("expected RECROSS, got %s (hold %d)", tr.Status, tr.HoldDays)
	}
}

func TestEvaluate_OpenMarkToMarket(t *testing.T) {
	c := risingSeries(40, 100, 1) // steady uptrend, EMA7 stays above EMA21
	entryIdx := 30
	entry := c[entryIdx].Open
	sl := entry - 50 // never hit
	tr := Evaluate(c, entryIdx, sl, Options{})
	if tr.Status != StatusOpen {
		t.Fatalf("expected OPEN (no exit in an uptrend), got %s", tr.Status)
	}
	if !tr.Open {
		t.Fatal("Open flag should be true")
	}
	// Marked at last close, which is above entry → positive (gross), but costs apply.
	if tr.Exit != c[len(c)-1].Close {
		t.Fatalf("expected mark at last close %.2f, got %.2f", c[len(c)-1].Close, tr.Exit)
	}
}

func TestEvaluate_Timeout(t *testing.T) {
	c := risingSeries(40, 100, 1)
	entryIdx := 30
	sl := c[entryIdx].Open - 50
	tr := Evaluate(c, entryIdx, sl, Options{MaxHoldDays: 3})
	if tr.Status != StatusTimeout {
		t.Fatalf("expected TIMEOUT at the hold cap, got %s (hold %d)", tr.Status, tr.HoldDays)
	}
	if tr.HoldDays != 3 {
		t.Fatalf("expected hold 3, got %d", tr.HoldDays)
	}
}

func TestEvaluate_CostsReduceReturn(t *testing.T) {
	c := risingSeries(40, 100, 1)
	entryIdx := 30
	sl := c[entryIdx].Open - 50
	gross := Evaluate(c, entryIdx, sl, Options{})
	withCost := Evaluate(c, entryIdx, sl, Options{CostPct: 0.25, SlippagePct: 0.20})
	if !(withCost.NetPct < gross.NetPct) {
		t.Fatalf("costs must reduce net return: gross %+.3f%% vs withCost %+.3f%%", gross.NetPct, withCost.NetPct)
	}
}

func TestSummarize(t *testing.T) {
	trades := []Trade{
		{Symbol: "A", NetPct: 5, Status: StatusRecross},
		{Symbol: "B", NetPct: -2, Status: StatusStop},
		{Symbol: "C", NetPct: 1, Status: StatusOpen},
	}
	s := Summarize(trades)
	if s.Trades != 3 || s.Wins != 2 || s.Open != 1 || s.Stopped != 1 || s.Recross != 1 {
		t.Fatalf("unexpected counts: %+v", s)
	}
	if math.Abs(s.AvgNetPct-(4.0/3.0)) > 1e-9 {
		t.Fatalf("avg net wrong: %v", s.AvgNetPct)
	}
	if s.ClosedTrades != 2 || math.Abs(s.ClosedAvgNetPct-1.5) > 1e-9 {
		t.Fatalf("closed-only stats wrong: trades=%d avg=%v", s.ClosedTrades, s.ClosedAvgNetPct)
	}
	if s.MedianNetPct != 1 {
		t.Fatalf("median wrong: %v", s.MedianNetPct)
	}
}
