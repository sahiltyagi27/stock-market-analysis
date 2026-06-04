// Package signaleval scores the forward performance of recorded signals.
//
// It answers one question honestly: if you had acted on a signal, what would
// you actually have made — entering at the next session's open, managing the
// trade with the validated EMA-recross exit (plus a protective stop and an
// optional time cap), and paying realistic costs and slippage?
//
// It is deliberately separate from the backtest engine: the backtest generates
// its own signals from end-of-day candles, whereas this package takes signals
// that were *already produced* (e.g. the intraday live-scan rows persisted in
// scan_results) and walks each one forward through real candles. Trades whose
// exit has not yet triggered are reported as still-open and marked to the last
// available close, so the same report sharpens as the data matures.
package signaleval

import (
	"sort"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// Status describes how a simulated trade ended.
type Status string

const (
	StatusStop    Status = "STOP"    // protective stop hit
	StatusRecross Status = "RECROSS" // EMA fast crossed back below slow (exit)
	StatusTimeout Status = "TIMEOUT" // max-hold cap reached
	StatusOpen    Status = "OPEN"    // not yet exited — marked to last close
)

// Options configures the forward evaluation.
type Options struct {
	// CostPct is the round-trip transaction cost as a percent of notional,
	// split evenly across the buy and sell legs. e.g. 0.25.
	CostPct float64
	// SlippagePct is the adverse per-leg fill haircut. e.g. 0.20.
	SlippagePct float64
	// EMAFast/EMASlow define the recross exit (exit when fast < slow after
	// entry). Defaults: 7 / 21.
	EMAFast int
	EMASlow int
	// MaxHoldDays force-closes a still-running trade after this many candles
	// (TIMEOUT). 0 = no cap (hold until stop or recross).
	MaxHoldDays int
}

func (o Options) withDefaults() Options {
	out := o
	if out.EMAFast <= 0 {
		out.EMAFast = 7
	}
	if out.EMASlow <= 0 {
		out.EMASlow = 21
	}
	return out
}

func (o Options) legCost() float64 { return o.CostPct / 100 / 2 }
func (o Options) slip() float64    { return o.SlippagePct / 100 }

// Trade is the outcome of evaluating one signal forward.
type Trade struct {
	Symbol     string
	EntryIdx   int
	EntryDate  string
	ExitIdx    int
	ExitDate   string
	Entry      float64 // raw next-open price (before slippage/cost)
	SL         float64
	Exit       float64 // raw exit price (before slippage/cost)
	Status     Status
	HoldDays   int
	NetPct     float64 // net of costs + slippage, both legs
	Open       bool    // true when Status == StatusOpen
}

// Evaluate simulates a single trade. The signal occurred on or before
// candles[entryIdx-1]; entry is taken at candles[entryIdx].Open. sl is the
// protective stop level (absolute price). It walks forward from the entry
// candle and returns the resulting Trade. entryIdx must be in range and the
// stop below entry; otherwise an Open trade marked at entry is returned with a
// zero net (caller should pre-validate).
func Evaluate(candles []models.Candle, entryIdx int, sl float64, opts Options) Trade {
	o := opts.withDefaults()
	n := len(candles)

	entryRaw := candles[entryIdx].Open
	if entryRaw <= 0 {
		entryRaw = candles[entryIdx].Close
	}

	// EMA series for the recross exit.
	closes := make([]float64, n)
	for i, c := range candles {
		closes[i] = c.Close
	}
	fast, _ := analysis.EMA(closes, o.EMAFast)
	slow, _ := analysis.EMA(closes, o.EMASlow)

	t := Trade{
		Symbol:   candles[entryIdx].Symbol,
		EntryIdx: entryIdx,
		EntryDate: candles[entryIdx].Timestamp.Format("2006-01-02"),
		Entry:    entryRaw,
		SL:       sl,
	}

	exitRaw := closes[n-1]
	status := StatusOpen
	exitIdx := n - 1

	for i := entryIdx; i < n; i++ {
		// Protective stop is checked first (pessimistic).
		if candles[i].Low <= sl {
			exitRaw, status, exitIdx = sl, StatusStop, i
			break
		}
		// EMA-recross exit (only after the entry candle).
		if i > entryIdx && fast[i] > 0 && slow[i] > 0 && fast[i] < slow[i] {
			exitRaw, status, exitIdx = closes[i], StatusRecross, i
			break
		}
		// Time cap.
		if o.MaxHoldDays > 0 && i-entryIdx >= o.MaxHoldDays {
			exitRaw, status, exitIdx = closes[i], StatusTimeout, i
			break
		}
	}

	t.Exit = exitRaw
	t.Status = status
	t.Open = status == StatusOpen
	t.ExitIdx = exitIdx
	t.ExitDate = candles[exitIdx].Timestamp.Format("2006-01-02")
	t.HoldDays = exitIdx - entryIdx

	buy := entryRaw * (1 + o.slip()) * (1 + o.legCost())
	sell := exitRaw * (1 - o.slip()) * (1 - o.legCost())
	if buy > 0 {
		t.NetPct = (sell/buy - 1) * 100
	}
	return t
}

// Summary aggregates a set of evaluated trades.
type Summary struct {
	Trades       int
	Wins         int // net > 0
	Open         int
	Stopped      int
	Recross      int
	Timeout      int
	AvgNetPct    float64
	MedianNetPct float64
	Best         float64
	Worst        float64
	// ClosedTrades / ClosedAvgNetPct cover only trades that have actually
	// exited (Status != OPEN) — the honest, mature subset.
	ClosedTrades    int
	ClosedAvgNetPct float64
}

// Summarize computes aggregate statistics over the trades.
func Summarize(trades []Trade) Summary {
	var s Summary
	if len(trades) == 0 {
		return s
	}
	s.Trades = len(trades)
	nets := make([]float64, 0, len(trades))
	s.Best, s.Worst = trades[0].NetPct, trades[0].NetPct
	var sum, closedSum float64
	for _, t := range trades {
		sum += t.NetPct
		nets = append(nets, t.NetPct)
		if t.NetPct > 0 {
			s.Wins++
		}
		if t.NetPct > s.Best {
			s.Best = t.NetPct
		}
		if t.NetPct < s.Worst {
			s.Worst = t.NetPct
		}
		switch t.Status {
		case StatusOpen:
			s.Open++
		case StatusStop:
			s.Stopped++
		case StatusRecross:
			s.Recross++
		case StatusTimeout:
			s.Timeout++
		}
		if t.Status != StatusOpen {
			s.ClosedTrades++
			closedSum += t.NetPct
		}
	}
	s.AvgNetPct = sum / float64(len(trades))
	if s.ClosedTrades > 0 {
		s.ClosedAvgNetPct = closedSum / float64(s.ClosedTrades)
	}
	sort.Float64s(nets)
	s.MedianNetPct = nets[len(nets)/2]
	return s
}
