// Package backtest implements a walk-forward back-test engine that replays the
// scanner pipeline over historical candle data to evaluate signal quality.
//
// For every trading day D within a configured date range, the engine runs the
// full scanner pipeline on candles[0:D]. When a signal is produced it enters
// at the open of D+1 and walks forward until the stop-loss, target, or a
// maximum-hold timeout is reached — whichever comes first.
//
// Pessimistic tie-breaking: when a candle's Low ≤ SL AND High ≥ Target on
// the same bar, the stop-loss (loss) is recorded.
package backtest

import "time"

// Outcome describes how a simulated trade was closed.
type Outcome string

const (
	OutcomeWin     Outcome = "win"
	OutcomeLoss    Outcome = "loss"
	OutcomeTimeout Outcome = "timeout"
)

// TradeResult records the full lifecycle of one simulated trade.
type TradeResult struct {
	Symbol string
	// SignalDate is the timestamp of the last candle fed to the scanner.
	SignalDate time.Time
	// EntryDate is the timestamp of the candle on which we entered (D+1 open).
	EntryDate time.Time
	// ExitDate is the timestamp of the candle on which the trade closed.
	ExitDate  time.Time
	Entry     float64
	SL        float64
	Target    float64
	ExitPrice float64
	Score     float64
	// ATR is the ATR14 value used to size the stop loss (0 when ATR was disabled).
	ATR     float64
	Trend   string
	Outcome Outcome
	// ActualRR is the realised risk:reward in units of risk.
	// (ExitPrice − Entry) / (Entry − SL).
	// Positive = profitable, negative = stopped out.
	ActualRR float64
	// HoldDays is the number of candles held, 1-indexed
	// (1 = closed on the entry candle itself).
	HoldDays int
}
