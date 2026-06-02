package backtest

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/internal/crossover"
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

	// TrailATRMultiplier sets the ATR-based trailing stop distance.
	// Once the trade's highest intraday price exceeds the entry, the trailing
	// stop is placed at:
	//
	//   highestHigh − TrailATRMultiplier × ATR
	//
	// and only ever moves upward.  ATR is the value computed at signal time
	// (stored in TradeResult.ATR).  Default: 1.5.  Set to ≤ 0 to disable.
	TrailATRMultiplier float64

	// Mode selects the scanning strategy.
	// "swing" (default) uses the support-zone swing scanner.
	// "crossover" uses the EMA 7×21 crossover scanner.
	Mode string

	// ScanOpts are used when Mode == "swing" (or empty).
	ScanOpts scanner.Options

	// CrossoverOpts are used when Mode == "crossover".
	CrossoverOpts crossover.Options

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
	if opts.TrailATRMultiplier == 0 {
		opts.TrailATRMultiplier = 1.5
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

// tradeSetup holds the fields extracted from whichever scanner produced the
// signal.  It is the common interface between swing and crossover modes.
type tradeSetup struct {
	sl     float64
	target float64
	atr    float64 // used for trailing stop; may be 0 if scanner doesn't compute it
	score  float64
	trend  string
}

// getTradeSetup runs the scanner selected by opts.Mode on the given candle
// history and returns the relevant trade fields.  ok is false when no signal
// is produced (caller should advance i and continue).
func getTradeSetup(sym string, candles []models.Candle, opts Options) (ts tradeSetup, ok bool) {
	if opts.Mode == "crossover" {
		sigs, _ := crossover.Scan(
			[]crossover.Input{{Symbol: sym, Candles: candles}},
			opts.CrossoverOpts,
		)
		if len(sigs) == 0 {
			return ts, false
		}
		sig := sigs[0]
		// Compute ATR for the trailing stop even though the SL is not ATR-based.
		atr := analysis.ATR(candles, 14)
		return tradeSetup{
			sl:     sig.SL,
			target: sig.Target,
			atr:    atr,
			score:  sig.Score,
			trend:  "crossover",
		}, true
	}

	// Swing mode (default).
	sigs := scanner.Scan(
		[]scanner.Input{{Symbol: sym, Candles: candles}},
		opts.ScanOpts,
	)
	if len(sigs) == 0 {
		return ts, false
	}
	sig := sigs[0]
	return tradeSetup{
		sl:     sig.Trade.StopLoss,
		target: sig.Trade.Target,
		atr:    sig.Trade.ATR,
		score:  sig.Score,
		trend:  string(sig.Trend),
	}, true
}

// minCandles returns the minimum candle count required for the active mode.
func (o Options) minCandles() int {
	if o.Mode == "crossover" {
		if o.CrossoverOpts.MinCandles > 0 {
			return o.CrossoverOpts.MinCandles
		}
		return 50 // crossover default
	}
	if o.ScanOpts.MinCandles > 0 {
		return o.ScanOpts.MinCandles
	}
	return 200 // swing default
}

// simulateSymbol runs the walk-forward loop for a single symbol.
// It never opens a new trade while one is still active.
func simulateSymbol(sym string, candles []models.Candle, opts Options) []TradeResult {
	minC := opts.minCandles()
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

		// Run the selected scanner on history up to and including day i.
		ts, ok := getTradeSetup(sym, candles[:i+1], opts)
		if !ok {
			i++
			continue
		}

		if opts.MinScore > 0 && ts.score < opts.MinScore {
			i++
			continue
		}

		// Enter at the open of the next trading day (D+1).
		entryCandle := candles[i+1]
		entry := entryCandle.Open
		if entry <= 0 {
			entry = entryCandle.Close // gap-open fallback
		}

		// Skip the setup if a gap has already blown through the stop or there
		// is no room between entry and target.
		if ts.sl <= 0 || entry <= ts.sl || (ts.target > 0 && entry >= ts.target) {
			i++
			continue
		}

		// Walk forward starting from the entry candle.
		outcome, exitPrice, holdDays := walkForward(
			entry, ts.sl, ts.target,
			ts.atr,
			candles[i+1:],
			opts.MaxHold,
			opts.TrailATRMultiplier,
		)

		// exitIdx is the 0-based index into candles[] where the trade closed.
		exitIdx := i + holdDays

		risk := entry - ts.sl
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
			SL:         ts.sl,
			Target:     ts.target,
			ExitPrice:  exitPrice,
			Score:      ts.score,
			ATR:        ts.atr,
			Trend:      ts.trend,
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
// is resolved.  The first element of candles is the entry candle (entered at
// open).
//
// ATR trailing stop:
//
//	When trailATRMult > 0 and atr > 0, a trailing stop is computed as:
//	  trailSL = highestHigh − trailATRMult × atr
//	The trailing stop only activates once the highest intraday High seen
//	exceeds the entry price (trade is profitable).  It only moves upward —
//	never below the original sl.
//
// Pessimistic tie-breaking on the same candle:
//   - stop (trailing or fixed) checked before target
//   - if both trailing stop and target fire on the same candle → trail stop
//
// Returns (outcome, exitPrice, holdDays) where holdDays is 1-indexed
// (1 = closed on the entry candle itself).
func walkForward(entry, sl, target, atr float64, candles []models.Candle, maxHold int, trailATRMult float64) (Outcome, float64, int) {
	stopLevel := sl        // current effective stop; only ever moves up
	highestHigh := entry   // highest intraday High seen (starts at entry)
	trailBuffer := trailATRMult * atr
	trailingActive := trailATRMult > 0 && atr > 0 && trailBuffer > 0

	for i, c := range candles {
		holdDays := i + 1

		// ── Step 1: check current stop (pessimistic — before any updates) ──────
		if c.Low <= stopLevel {
			if stopLevel > sl {
				// Trailing stop moved above the original SL → trail exit.
				return OutcomeTrailStop, stopLevel, holdDays
			}
			return OutcomeLoss, sl, holdDays
		}

		// ── Step 2: target hit ───────────────────────────────────────────────
		if c.High >= target {
			return OutcomeWin, target, holdDays
		}

		// ── Step 3: max-hold timeout ─────────────────────────────────────────
		if holdDays >= maxHold {
			return OutcomeTimeout, c.Close, holdDays
		}

		// ── Step 4: update highest high AFTER this candle's checks ───────────
		// This ensures the raised trailing stop only applies from the next candle,
		// preventing a same-candle raise-and-trigger paradox.
		if c.High > highestHigh {
			highestHigh = c.High
		}

		// ── Step 5: raise trailing stop for the next candle ──────────────────
		// Trailing activates only once price has exceeded entry intraday
		// (trade is in profit), then the stop can only move upward.
		if trailingActive && highestHigh > entry {
			newTrailSL := highestHigh - trailBuffer
			if newTrailSL > stopLevel {
				stopLevel = newTrailSL
			}
		}
	}

	// Ran out of history before resolution — exit at the last close.
	if len(candles) == 0 {
		return OutcomeTimeout, entry, 1
	}
	last := candles[len(candles)-1]
	return OutcomeTimeout, last.Close, len(candles)
}
