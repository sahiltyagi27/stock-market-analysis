package backtest

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/scanner"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// Options configures a back-test run.
type Options struct {
	// From and To bound the signal dates that are evaluated.
	// Full candle history before From is still loaded for reliable indicators.
	// Zero value means no bound.
	From time.Time
	To   time.Time

	// MinScore filters out scanner signals below this threshold before walking
	// forward. 0 disables the filter (all signals are evaluated).
	MinScore float64

	// MaxHold is the maximum number of candles to hold a trade before recording
	// a timeout exit at the closing price. Default: 20.
	MaxHold int

	// Workers is the size of the goroutine pool used for concurrent simulation.
	// Default: 8.
	Workers int

	// ScanOpts are passed through to scanner.Scan on every signal day.
	ScanOpts scanner.Options

	// Progress is an optional callback invoked after each symbol completes.
	// Arguments are (symbolsDone, symbolsTotal). Safe to nil; called from
	// multiple goroutines — implementations must be concurrency-safe.
	Progress func(done, total int)
}

// Run simulates every symbol in the candles map over the configured date range
// using a walk-forward methodology:
//
//  1. For each trading day D (candles[i]):
//     a. Run the full scanner pipeline on candles[0:i+1].
//     b. If a signal passes the MinScore filter, enter at candles[i+1].Open.
//     c. Walk forward until SL, Target, or MaxHold candles elapsed.
//
//  2. No overlapping trades per symbol — the next scan begins the day after
//     the previous trade closes.
//
// Results are returned sorted by SignalDate ASC, Score DESC.
// Individual symbol errors are silently skipped (a symbol with insufficient
// history simply produces no trades).
func Run(_ context.Context, candles map[string][]models.Candle, opts Options) []TradeResult {
	if opts.MaxHold <= 0 {
		opts.MaxHold = 20
	}
	if opts.Workers <= 0 {
		opts.Workers = 8
	}

	symbols := make([]string, 0, len(candles))
	for sym := range candles {
		symbols = append(symbols, sym)
	}

	sem := make(chan struct{}, opts.Workers)
	var mu sync.Mutex
	var all []TradeResult
	var wg sync.WaitGroup
	var doneCount atomic.Int32

	total := len(symbols)

	for _, sym := range symbols {
		sym := sym
		c := candles[sym]
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			res := simulateSymbol(sym, c, opts)
			n := int(doneCount.Add(1))
			if opts.Progress != nil {
				opts.Progress(n, total)
			}
			if len(res) > 0 {
				mu.Lock()
				all = append(all, res...)
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	// Stable chronological sort, highest score first within same day.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].SignalDate.Equal(all[j].SignalDate) {
			return all[i].Score > all[j].Score
		}
		return all[i].SignalDate.Before(all[j].SignalDate)
	})

	return all
}

// simulateSymbol runs the walk-forward loop for a single symbol.
// It never opens a new trade while one is still active.
func simulateSymbol(sym string, candles []models.Candle, opts Options) []TradeResult {
	minC := opts.ScanOpts.MinCandles
	if minC <= 0 {
		minC = 200 // mirrors scanner.Options.withDefaults()
	}
	// Need minC candles for the signal day plus at least one more for entry.
	if len(candles) < minC+1 {
		return nil
	}

	var results []TradeResult
	// i is the index of the last candle passed to the scanner ("signal day").
	i := minC - 1

	for i < len(candles)-1 {
		signalDate := candles[i].Timestamp

		// Date-range filter: skip days before From, stop after To.
		if !opts.From.IsZero() && signalDate.Before(opts.From) {
			i++
			continue
		}
		if !opts.To.IsZero() && signalDate.After(opts.To) {
			break
		}

		// Run the full scanner on history up to and including day i.
		sigs := scanner.Scan(
			[]scanner.Input{{Symbol: sym, Candles: candles[:i+1]}},
			opts.ScanOpts,
		)
		if len(sigs) == 0 {
			i++
			continue
		}
		sig := sigs[0]

		if opts.MinScore > 0 && sig.Score < opts.MinScore {
			i++
			continue
		}

		// Enter at the open of the next trading day (D+1).
		entryCandle := candles[i+1]
		entry := entryCandle.Open
		if entry <= 0 {
			entry = entryCandle.Close // gap-open fallback
		}

		sl := sig.Trade.StopLoss
		target := sig.Trade.Target

		// Skip the setup if a gap has already blown through the stop or there
		// is no room between entry and target.
		if sl <= 0 || entry <= sl || entry >= target {
			i++
			continue
		}

		// Walk forward starting from the entry candle.
		outcome, exitPrice, holdDays := walkForward(entry, sl, target, candles[i+1:], opts.MaxHold)

		// exitIdx is the 0-based index into candles[] where the trade closed.
		// walkForward holdDays=1 means closed on candles[i+1], so exitIdx = i+1.
		exitIdx := i + holdDays

		risk := entry - sl
		var actualRR float64
		if risk > 0 {
			actualRR = (exitPrice - entry) / risk
		}

		results = append(results, TradeResult{
			Symbol:     sym,
			SignalDate: signalDate,
			EntryDate:  entryCandle.Timestamp,
			ExitDate:   candles[exitIdx].Timestamp,
			Entry:      entry,
			SL:         sl,
			Target:     target,
			ExitPrice:  exitPrice,
			Score:      sig.Score,
			ATR:        sig.Trade.ATR,
			Trend:      string(sig.Trend),
			Outcome:    outcome,
			ActualRR:   actualRR,
			HoldDays:   holdDays,
		})

		// Jump past the closed trade — no overlapping positions.
		i = exitIdx + 1
	}

	return results
}

// walkForward advances through candles from the entry candle until the trade
// is resolved. The first element of candles is the entry candle.
//
// Pessimistic tie-breaking: when both SL and Target are hit on the same candle,
// the stop-loss is recorded (loss).
//
// Returns (outcome, exitPrice, holdDays) where holdDays is 1-indexed
// (1 = closed on the entry candle itself).
func walkForward(entry, sl, target float64, candles []models.Candle, maxHold int) (Outcome, float64, int) {
	for i, c := range candles {
		holdDays := i + 1
		// Pessimistic: check SL before Target.
		if c.Low <= sl {
			return OutcomeLoss, sl, holdDays
		}
		if c.High >= target {
			return OutcomeWin, target, holdDays
		}
		if holdDays >= maxHold {
			return OutcomeTimeout, c.Close, holdDays
		}
	}
	// Ran out of history before resolution — exit at the last close.
	if len(candles) == 0 {
		return OutcomeTimeout, entry, 1
	}
	last := candles[len(candles)-1]
	return OutcomeTimeout, last.Close, len(candles)
}
