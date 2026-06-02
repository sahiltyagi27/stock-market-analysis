package backtest

import (
	"context"
	"math"
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

	// AllocLookback controls how same-day candidates compete for free slots.
	// 0 (default): rank by scanner score (current behaviour).
	// >0: rank by the stock's leadership = its return over the last AllocLookback
	// candles ending on the signal day (descending), with score as the tiebreak.
	// This is "Variant D": buy the pullback (entries unchanged), but spend scarce
	// slots on the strongest long-term leaders among today's signals.
	AllocLookback int

	// RiskPct enables risk-based ("ATR-style") position sizing. When > 0, each
	// position is sized so that being stopped out costs RiskPct% of equity:
	//
	//   notional = equity × RiskPct% ÷ ((entry − SL) / entry)
	//
	// A tight stop → larger position; a wide (volatile) stop → smaller position.
	// Capped at MaxWeightPct of equity and at available cash.
	// 0 (default) uses equal 1/MaxPositions slices.
	RiskPct float64

	// MaxWeightPct caps any single position at this % of equity under risk-based
	// sizing (prevents a very tight stop from over-concentrating). Default: 25.
	MaxWeightPct float64

	// CostPct is the total round-trip transaction cost (brokerage + STT + fees)
	// as a percentage of notional, split evenly across the buy and sell legs.
	// e.g. 0.25 ≈ NSE delivery. 0 = frictionless.
	CostPct float64

	// SlippagePct is the adverse fill haircut applied to every leg: buys fill
	// SlippagePct% higher, sells SlippagePct% lower than the candle price.
	// e.g. 0.20. 0 = perfect fills.
	SlippagePct float64

	// EngineOpts carries Mode + ScanOpts + CrossoverOpts for signal generation.
	EngineOpts Options
}

// slip is the per-leg slippage fraction.
func (o PortfolioOptions) slip() float64 { return o.SlippagePct / 100 }

// legCost is the per-leg transaction cost fraction (half the round-trip).
func (o PortfolioOptions) legCost() float64 { return o.CostPct / 100 / 2 }

// buyFill returns the effective purchase price after slippage (worse = higher).
func buyFill(price, slip float64) float64 { return price * (1 + slip) }

// sellFill returns the effective sale price after slippage (worse = lower).
func sellFill(price, slip float64) float64 { return price * (1 - slip) }

// cashOut is the total cash spent buying `shares` at `fill` incl. leg cost.
func cashOut(shares, fill, legCost float64) float64 { return shares * fill * (1 + legCost) }

// cashIn is the net cash received selling `shares` at `fill` after leg cost.
func cashIn(shares, fill, legCost float64) float64 { return shares * fill * (1 - legCost) }

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
	ProfitFactor   float64 // sum(+R) / |sum(−R)|; +Inf when no losers
	AvgRR          float64 // mean ActualRR across all trades

	// Opportunity loss (M10): signals rejected because the portfolio was full,
	// with their hypothetical outcomes (same exit + costs). Compare RejectedAvgRR
	// vs AvgRR — if rejected ≈ accepted, the slot limit isn't costing you and
	// rotation won't help.
	RejectedFull    int
	RejectedAvgRR   float64
	RejectedWinRate float64
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
	// rankRet is the stock's return over AllocLookback candles ending on the
	// signal day — used to order same-day candidates when AllocLookback > 0.
	rankRet float64
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
	if opts.MaxWeightPct <= 0 {
		opts.MaxWeightPct = 25
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

	// Opportunity-loss tracking (M10): hypothetical outcomes of signals rejected
	// because the portfolio was full. hypoOpenUntil dedups per symbol.
	hypoOpenUntil := map[string]time.Time{}
	var rejCount, rejWins, rejLosses int
	var rejSumRR float64

	for _, day := range calendar {
		dk := day.Format(dayFmt)

		// 1. Exits.
		for sym, pos := range positions {
			sd := data[sym]
			idx, ok := sd.dateIdx[dk]
			if !ok {
				continue // no bar for this symbol today; hold
			}
			exitTrigger, outcome, exited := checkExit(pos, sd, idx, opts)
			if !exited {
				continue
			}
			exitPrice := sellFill(exitTrigger, opts.slip())
			cash += cashIn(pos.shares, exitPrice, opts.legCost())
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

		// 3. Entries (best first per allocation ranking).
		for _, sig := range signalsByDate[dk] {
			if _, held := positions[sig.symbol]; held {
				continue
			}
			// Opportunity-loss (M10): when the portfolio is full, this qualifying
			// signal is rejected. Simulate its hypothetical outcome (same exit +
			// costs) so we can compare what we skipped vs what we took. Dedup per
			// symbol until its hypothetical trade would have closed.
			if len(positions) >= opts.MaxPositions {
				sd := data[sig.symbol]
				if until, busy := hypoOpenUntil[sig.symbol]; !busy || !day.Before(until) {
					rr, exitIdx := simulateHypo(sd, sig, opts)
					rejCount++
					rejSumRR += rr
					if rr > 0 {
						rejWins++
					} else if rr < 0 {
						rejLosses++
					}
					hypoOpenUntil[sig.symbol] = sd.candles[exitIdx].Timestamp
				}
				continue
			}
			if cash <= 0 {
				continue
			}
			entryPrice := buyFill(sig.entry, opts.slip())
			alloc := positionNotional(equity, cash, entryPrice, sig.sl, opts)
			shares := alloc / (entryPrice * (1 + opts.legCost()))
			if shares <= 0 {
				continue
			}
			cash -= cashOut(shares, entryPrice, opts.legCost())
			risk := entryPrice - sig.sl

			// Same-day gap-down stop check.
			sd := data[sig.symbol]
			ec := sd.candles[sig.entryIdx]
			if ec.Low <= sig.sl {
				exitPrice := sellFill(sig.sl, opts.slip())
				cash += cashIn(shares, exitPrice, opts.legCost())
				rr := 0.0
				if risk > 0 {
					rr = (exitPrice - entryPrice) / risk
				}
				trades = append(trades, TradeResult{
					Symbol: sig.symbol, SignalDate: sig.entryDate, EntryDate: sig.entryDate,
					ExitDate: day, Entry: entryPrice, SL: sig.sl, Target: sig.target,
					ExitPrice: exitPrice, Outcome: OutcomeLoss, ActualRR: rr, HoldDays: 0,
				})
				continue
			}
			positions[sig.symbol] = &pfPosition{
				symbol: sig.symbol, shares: shares, entry: entryPrice, sl: sig.sl,
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
		exitPrice := sellFill(sd.candles[lastIdx].Close, opts.slip())
		cash += cashIn(pos.shares, exitPrice, opts.legCost())
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
	var sumPos, sumNeg, sumRR float64
	for _, t := range trades {
		sumRR += t.ActualRR
		switch {
		case t.ActualRR > 0:
			stats.Wins++
			sumPos += t.ActualRR
		case t.ActualRR < 0:
			stats.Losses++
			sumNeg += t.ActualRR
		}
	}
	if len(trades) > 0 {
		stats.AvgHoldDays = float64(totalHold) / float64(len(trades))
		stats.AvgRR = sumRR / float64(len(trades))
	}
	switch {
	case sumNeg < 0:
		stats.ProfitFactor = sumPos / (-sumNeg)
	case sumPos > 0:
		stats.ProfitFactor = math.Inf(1)
	}
	stats.RejectedFull = rejCount
	if rejCount > 0 {
		stats.RejectedAvgRR = rejSumRR / float64(rejCount)
		if rejWins+rejLosses > 0 {
			stats.RejectedWinRate = float64(rejWins) / float64(rejWins+rejLosses) * 100
		}
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
			// Leadership return over AllocLookback candles ending on the signal
			// day (index i). Used only when AllocLookback > 0.
			var rankRet float64
			if opts.AllocLookback > 0 {
				base := i - opts.AllocLookback
				if base >= 0 && cc[base].Close > 0 {
					rankRet = cc[i].Close/cc[base].Close - 1
				}
			}

			dk := ec.Timestamp.UTC().Format(dayFmt)
			out[dk] = append(out[dk], pfSignal{
				entryDate: ec.Timestamp, entryIdx: i + 1, symbol: sym,
				entry: entry, sl: ts.sl, target: ts.target, atr: ts.atr,
				score: ts.score, rankRet: rankRet,
			})
		}
	}

	// Order each day's candidates so the best fill free slots first.
	for dk := range out {
		sigs := out[dk]
		if opts.AllocLookback > 0 {
			// Variant D: leadership-ranked allocation, score as tiebreak.
			sort.SliceStable(sigs, func(a, b int) bool {
				if sigs[a].rankRet != sigs[b].rankRet {
					return sigs[a].rankRet > sigs[b].rankRet
				}
				return sigs[a].score > sigs[b].score
			})
		} else {
			sort.SliceStable(sigs, func(a, b int) bool { return sigs[a].score > sigs[b].score })
		}
		out[dk] = sigs
	}
	return out
}

// positionNotional decides how much cash to deploy into a new position.
//
//   - RiskPct == 0: equal slice = equity / MaxPositions (capped at cash).
//   - RiskPct  > 0: risk-based — size so a stop-out costs RiskPct% of equity:
//     notional = equity × RiskPct% ÷ ((entry − sl) / entry), capped at
//     MaxWeightPct of equity and at available cash. A tighter stop earns a
//     larger position; a wider (more volatile) stop a smaller one.
func positionNotional(equity, cash, entry, sl float64, opts PortfolioOptions) float64 {
	var alloc float64
	if opts.RiskPct > 0 {
		riskFrac := (entry - sl) / entry
		if riskFrac <= 0 {
			return 0
		}
		alloc = equity * (opts.RiskPct / 100) / riskFrac
		if cap := equity * (opts.MaxWeightPct / 100); alloc > cap {
			alloc = cap
		}
	} else {
		alloc = equity / float64(opts.MaxPositions)
	}
	if alloc > cash {
		alloc = cash
	}
	return alloc
}

// simulateHypo computes the hypothetical R:R of a rejected signal as if it had
// been entered (same SL/target/exit + slippage), independent of the portfolio's
// slot/cash state. Returns the realised R:R and the candle index of the exit.
func simulateHypo(sd *symData, sig pfSignal, opts PortfolioOptions) (rr float64, exitIdx int) {
	entryPrice := buyFill(sig.entry, opts.slip())
	risk := entryPrice - sig.sl
	rrFrom := func(trigger float64) float64 {
		if risk <= 0 {
			return 0
		}
		return (sellFill(trigger, opts.slip()) - entryPrice) / risk
	}

	// Same-day gap-down stop.
	if sd.candles[sig.entryIdx].Low <= sig.sl {
		return rrFrom(sig.sl), sig.entryIdx
	}
	pos := &pfPosition{
		symbol: sig.symbol, entry: entryPrice, sl: sig.sl,
		target: sig.target, entryIdx: sig.entryIdx,
	}
	for idx := sig.entryIdx + 1; idx < len(sd.candles); idx++ {
		if trigger, _, exited := checkExit(pos, sd, idx, opts); exited {
			return rrFrom(trigger), idx
		}
	}
	last := len(sd.candles) - 1
	return rrFrom(sd.candles[last].Close), last
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
