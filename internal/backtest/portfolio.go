package backtest

import (
	"context"
	"sort"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// EMA periods used by the EMA-recross exit (independent of the entry strategy).
const (
	exitEMAFast = 7
	exitEMASlow = 21
)

// PortfolioOptions configures a portfolio-aware backtest: a single shared
// capital pool traded across all symbols on one timeline, with a cap on
// concurrent positions.
type PortfolioOptions struct {
	From, To time.Time

	// MinScore filters signals below this score (0 = all).
	MinScore float64

	// MaxPositions is the maximum number of simultaneously open positions.
	// Default: 5.
	MaxPositions int

	// StartCapital is the starting cash. Default: 100000.
	StartCapital float64

	// ExitMode: "ema" (hold until EMA7<EMA21) or "target" (fixed resistance).
	// SL is always checked first. Default: "ema".
	ExitMode string

	// MaxHoldDays force-closes a position after this many candles (0 = no cap).
	MaxHoldDays int

	// EngineOpts carries Mode + ScanOpts + CrossoverOpts for signal generation.
	EngineOpts Options
}

// PortfolioStats summarises a portfolio run.
type PortfolioStats struct {
	StartCapital   float64
	FinalCapital   float64
	ReturnPct      float64
	MaxDrawdownPct float64
	Trades         int
	Wins           int
	Losses         int
	AvgHoldDays    float64
	MaxConcurrent  int
}

// symData holds precomputed per-symbol series for the portfolio walk.
type symData struct {
	candles  []models.Candle
	ema7     []float64
	ema21    []float64
	dateIdx  map[string]int // "2006-01-02" -> candle index
}

type pfSignal struct {
	entryDate time.Time
	entryIdx  int
	symbol    string
	entry     float64
	sl        float64
	target    float64
	atr       float64
	score     float64
}

type pfPosition struct {
	symbol    string
	shares    float64
	entry     float64
	sl        float64
	target    float64
	entryIdx  int
	entryDate time.Time
}

const dayFmt = "2006-01-02"

// RunPortfolio executes a portfolio-aware backtest and returns the closed
// trades plus summary statistics.
func RunPortfolio(_ context.Context, candles map[string][]models.Candle, opts PortfolioOptions) ([]TradeResult, PortfolioStats) {
	if opts.MaxPositions <= 0 {
		opts.MaxPositions = 5
	}
	if opts.StartCapital <= 0 {
		opts.StartCapital = 100000
	}
	if opts.ExitMode == "" {
		opts.ExitMode = "ema"
	}

	// Precompute per-symbol series.
	data := make(map[string]*symData, len(candles))
	for sym, cc := range candles {
		if len(cc) < 30 {
			continue
		}
		closes := make([]float64, len(cc))
		for i, c := range cc {
			closes[i] = c.Close
		}
		e7, _ := analysis.EMA(closes, exitEMAFast)
		e21, _ := analysis.EMA(closes, exitEMASlow)
		di := make(map[string]int, len(cc))
		for i, c := range cc {
			di[c.Timestamp.UTC().Format(dayFmt)] = i
		}
		data[sym] = &symData{candles: cc, ema7: e7, ema21: e21, dateIdx: di}
	}

	// Generate signals across all symbols.
	signalsByDate := generateSignals(data, opts)

	// Build the global trading calendar (union of all candle dates, sorted).
	dateSet := map[string]time.Time{}
	for _, sd := range data {
		for _, c := range sd.candles {
			d := c.Timestamp.UTC().Truncate(24 * time.Hour)
			dateSet[d.Format(dayFmt)] = d
		}
	}
	calendar := make([]time.Time, 0, len(dateSet))
	for _, d := range dateSet {
		calendar = append(calendar, d)
	}
	sort.Slice(calendar, func(i, j int) bool { return calendar[i].Before(calendar[j]) })

	// Walk the calendar.
	cash := opts.StartCapital
	positions := map[string]*pfPosition{}
	var trades []TradeResult
	peak := opts.StartCapital
	var maxDD float64
	var maxConcurrent int
	var totalHold int

	for _, day := range calendar {
		dk := day.Format(dayFmt)

		// 1. Exits.
		for sym, pos := range positions {
			sd := data[sym]
			idx, ok := sd.dateIdx[dk]
			if !ok {
				continue // no bar for this symbol today; hold
			}
			exitPrice, outcome, exited := checkExit(pos, sd, idx, opts)
			if !exited {
				continue
			}
			cash += pos.shares * exitPrice
			risk := pos.entry - pos.sl
			rr := 0.0
			if risk > 0 {
				rr = (exitPrice - pos.entry) / risk
			}
			hold := idx - pos.entryIdx
			totalHold += hold
			trades = append(trades, TradeResult{
				Symbol:     sym,
				SignalDate: pos.entryDate,
				EntryDate:  pos.entryDate,
				ExitDate:   day,
				Entry:      pos.entry,
				SL:         pos.sl,
				Target:     pos.target,
				ExitPrice:  exitPrice,
				Outcome:    outcome,
				ActualRR:   rr,
				HoldDays:   hold,
			})
			delete(positions, sym)
		}

		// 2. Mark-to-market equity + drawdown.
		equity := cash
		for sym, pos := range positions {
			sd := data[sym]
			if idx, ok := sd.dateIdx[dk]; ok {
				equity += pos.shares * sd.candles[idx].Close
			} else {
				equity += pos.shares * pos.entry // stale; approximate at cost
			}
		}
		if equity > peak {
			peak = equity
		}
		if peak > 0 {
			if dd := (peak - equity) / peak; dd > maxDD {
				maxDD = dd
			}
		}

		// 3. Entries (best score first).
		for _, sig := range signalsByDate[dk] {
			if len(positions) >= opts.MaxPositions {
				break
			}
			if _, held := positions[sig.symbol]; held {
				continue
			}
			if cash <= 0 {
				break
			}
			alloc := equity / float64(opts.MaxPositions)
			if alloc > cash {
				alloc = cash
			}
			shares := alloc / sig.entry
			if shares <= 0 {
				continue
			}
			// Same-day gap-down stop check.
			sd := data[sig.symbol]
			ec := sd.candles[sig.entryIdx]
			if ec.Low <= sig.sl {
				// Entered and stopped same day.
				risk := sig.entry - sig.sl
				rr := 0.0
				if risk > 0 {
					rr = (sig.sl - sig.entry) / risk
				}
				trades = append(trades, TradeResult{
					Symbol: sig.symbol, SignalDate: sig.entryDate, EntryDate: sig.entryDate,
					ExitDate: day, Entry: sig.entry, SL: sig.sl, Target: sig.target,
					ExitPrice: sig.sl, Outcome: OutcomeLoss, ActualRR: rr, HoldDays: 0,
				})
				continue
			}
			cash -= shares * sig.entry
			positions[sig.symbol] = &pfPosition{
				symbol: sig.symbol, shares: shares, entry: sig.entry, sl: sig.sl,
				target: sig.target, entryIdx: sig.entryIdx, entryDate: sig.entryDate,
			}
		}
		if len(positions) > maxConcurrent {
			maxConcurrent = len(positions)
		}
	}

	// Force-close any still-open positions at their last close.
	lastDay := calendar[len(calendar)-1]
	for sym, pos := range positions {
		sd := data[sym]
		lastIdx := len(sd.candles) - 1
		exitPrice := sd.candles[lastIdx].Close
		cash += pos.shares * exitPrice
		risk := pos.entry - pos.sl
		rr := 0.0
		if risk > 0 {
			rr = (exitPrice - pos.entry) / risk
		}
		trades = append(trades, TradeResult{
			Symbol: sym, SignalDate: pos.entryDate, EntryDate: pos.entryDate,
			ExitDate: lastDay, Entry: pos.entry, SL: pos.sl, Target: pos.target,
			ExitPrice: exitPrice, Outcome: OutcomeTimeout, ActualRR: rr,
			HoldDays: lastIdx - pos.entryIdx,
		})
		totalHold += lastIdx - pos.entryIdx
	}

	stats := PortfolioStats{
		StartCapital:   opts.StartCapital,
		FinalCapital:   cash,
		MaxDrawdownPct: maxDD * 100,
		Trades:         len(trades),
		MaxConcurrent:  maxConcurrent,
	}
	stats.ReturnPct = (cash - opts.StartCapital) / opts.StartCapital * 100
	for _, t := range trades {
		switch {
		case t.ActualRR > 0:
			stats.Wins++
		case t.ActualRR < 0:
			stats.Losses++
		}
	}
	if len(trades) > 0 {
		stats.AvgHoldDays = float64(totalHold) / float64(len(trades))
	}
	return trades, stats
}

// generateSignals scans every symbol day-by-day and records all qualifying
// entry signals (entry at next-day open), keyed by entry date.
func generateSignals(data map[string]*symData, opts PortfolioOptions) map[string][]pfSignal {
	out := map[string][]pfSignal{}
	minC := opts.EngineOpts.minCandles()

	for sym, sd := range data {
		cc := sd.candles
		if len(cc) < minC+1 {
			continue
		}
		for i := minC - 1; i < len(cc)-1; i++ {
			signalDate := cc[i].Timestamp
			if !opts.From.IsZero() && signalDate.Before(opts.From) {
				continue
			}
			if !opts.To.IsZero() && signalDate.After(opts.To) {
				break
			}
			ts, ok := getTradeSetup(sym, cc[:i+1], opts.EngineOpts)
			if !ok {
				continue
			}
			if opts.MinScore > 0 && ts.score < opts.MinScore {
				continue
			}
			ec := cc[i+1]
			entry := ec.Open
			if entry <= 0 {
				entry = ec.Close
			}
			if ts.sl <= 0 || entry <= ts.sl || (ts.target > 0 && entry >= ts.target) {
				continue
			}
			dk := ec.Timestamp.UTC().Format(dayFmt)
			out[dk] = append(out[dk], pfSignal{
				entryDate: ec.Timestamp, entryIdx: i + 1, symbol: sym,
				entry: entry, sl: ts.sl, target: ts.target, atr: ts.atr, score: ts.score,
			})
		}
	}

	// Sort each day's signals by score descending so the best fill first.
	for dk := range out {
		sigs := out[dk]
		sort.SliceStable(sigs, func(a, b int) bool { return sigs[a].score > sigs[b].score })
		out[dk] = sigs
	}
	return out
}

// checkExit evaluates a position against the candle at idx. SL is checked first
// (pessimistic). Returns (exitPrice, outcome, exited).
func checkExit(pos *pfPosition, sd *symData, idx int, opts PortfolioOptions) (float64, Outcome, bool) {
	c := sd.candles[idx]
	if c.Low <= pos.sl {
		return pos.sl, OutcomeLoss, true
	}
	switch opts.ExitMode {
	case "target":
		if pos.target > 0 && c.High >= pos.target {
			return pos.target, OutcomeWin, true
		}
	default: // "ema"
		if idx > pos.entryIdx && sd.ema7[idx] > 0 && sd.ema21[idx] > 0 && sd.ema7[idx] < sd.ema21[idx] {
			return c.Close, OutcomeWin, true
		}
	}
	if opts.MaxHoldDays > 0 && idx-pos.entryIdx >= opts.MaxHoldDays {
		return c.Close, OutcomeTimeout, true
	}
	return 0, "", false
}
