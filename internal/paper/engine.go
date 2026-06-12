// Package paper runs a forward, day-by-day paper-trading session that mirrors
// the validated portfolio backtest (EMA-recross exit, risk-based sizing,
// strategy-health gate, transaction costs) against persisted state, so a session
// continues across trading days.
//
// The strategy operates on DAILY candles, so the authoritative cycle is run once
// per day after the close (the "eod" mode). Intraday "live" mode is a read-only
// monitor that marks open positions to live prices.
package paper

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/internal/scanner"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// ErrAlreadyProcessed is returned by RunDayEnd when the requested day has already
// been processed (guards against running the EOD cycle twice). Use --force to override.
var ErrAlreadyProcessed = errors.New("EOD cycle already processed for this day")

// Config holds the paper-trading parameters (mirrors the backtest defaults).
type Config struct {
	StartCapital float64
	MaxPositions int
	RiskPct      float64 // risk-based sizing; <=0 → equal 1/N slices
	MaxWeightPct float64
	HealthWindow int     // strategy-health gate window (0 = off)
	HealthMin    float64 // avg-R threshold over the window
	HealthShadow bool    // keep the gate measuring via shadow trades while closed
	MinScore     float64
	CostPct      float64 // round-trip transaction cost %
	SlippagePct  float64 // per-leg slippage %
	ScanOpts     scanner.Options

	// signalFunc, when non-nil, overrides entry-signal generation. Production
	// leaves it nil (the swing scanner). Tests inject deterministic signals so the
	// gate/shadow orchestration can be exercised without crafting candles that
	// satisfy every swing-scanner filter.
	signalFunc func(history map[string][]models.Candle, opts scanner.Options) []scanner.StockSignal
}

// Store is the persistence surface RunDayEnd needs. *store.PaperStore satisfies
// it; tests provide an in-memory fake so the day-by-day gate behaviour can be
// verified hermetically (no database).
type Store interface {
	Account(ctx context.Context) (*store.PaperAccount, error)
	InitAccount(ctx context.Context, startCapital float64) error
	SetCash(ctx context.Context, cash float64) error
	SetLastEOD(ctx context.Context, d time.Time) error
	Positions(ctx context.Context) ([]store.PaperPosition, error)
	InsertPosition(ctx context.Context, p store.PaperPosition) error
	DeletePosition(ctx context.Context, symbol string) error
	Pending(ctx context.Context) ([]store.PaperPending, error)
	InsertPending(ctx context.Context, p store.PaperPending) error
	ClearPending(ctx context.Context) error
	InsertTrade(ctx context.Context, t store.PaperTrade) error
	RecentTradeR(ctx context.Context, n int) ([]float64, error)
	// Shadow surface (gate-closed measurement; no capital).
	ShadowPositions(ctx context.Context) ([]store.PaperPosition, error)
	InsertShadowPosition(ctx context.Context, p store.PaperPosition) error
	DeleteShadowPosition(ctx context.Context, symbol string) error
	ShadowPending(ctx context.Context) ([]store.PaperPending, error)
	InsertShadowPending(ctx context.Context, p store.PaperPending) error
	ClearShadowPending(ctx context.Context) error
	InsertShadowTrade(ctx context.Context, t store.PaperTrade) error
}

// CandleSource supplies candle history. *store.CandleStore satisfies it.
type CandleSource interface {
	GetCandles(ctx context.Context, symbol string, f store.CandleFilter) ([]models.Candle, error)
}

func (c Config) slip() float64    { return c.SlippagePct / 100 }
func (c Config) legCost() float64 { return c.CostPct / 100 / 2 }

// — pure helpers, identical math to internal/backtest —

func buyFill(p, slip float64) float64  { return p * (1 + slip) }
func sellFill(p, slip float64) float64 { return p * (1 - slip) }

// riskNotional mirrors backtest.positionNotional.
func riskNotional(equity, cash, entry, sl float64, c Config) float64 {
	var alloc float64
	if c.RiskPct > 0 {
		riskFrac := (entry - sl) / entry
		if riskFrac <= 0 {
			return 0
		}
		alloc = equity * (c.RiskPct / 100) / riskFrac
		if cap := equity * (c.MaxWeightPct / 100); alloc > cap {
			alloc = cap
		}
	} else {
		alloc = equity / float64(c.MaxPositions)
	}
	if alloc > cash {
		alloc = cash
	}
	return alloc
}

// healthyAvgR mirrors backtest.strategyHealthy (avgr mode). Disabled/warmup → true.
func healthyAvgR(recent []float64, window int, min float64) bool {
	if window <= 0 || len(recent) < window {
		return true
	}
	w := recent[len(recent)-window:]
	var sum float64
	for _, r := range w {
		sum += r
	}
	return sum/float64(len(w)) >= min
}

// Report captures what one cycle did, for printing.
type Report struct {
	Date        time.Time
	Mode        string
	Actions     []string
	GateOpen    bool
	Cash        float64
	Equity      float64
	OpenCount   int
	PendingMade int
	Positions   []PositionView
}

// PositionView is an open position with a current mark for display.
type PositionView struct {
	Symbol     string
	Shares     int64
	Entry      float64
	Mark       float64
	SL         float64
	Target     float64
	UnrealPnL  float64
	UnrealPct  float64
	EntryDate  time.Time
}

func (r *Report) log(format string, a ...any) { r.Actions = append(r.Actions, fmt.Sprintf(format, a...)) }

// RunDayEnd executes the authoritative once-per-day cycle as of `asOf` (a date
// whose daily candle is final): fill yesterday's pending at today's open, process
// exits on today's candle, then queue tomorrow's entries (gated + sized). All
// state is persisted. dryRun computes the report without writing.
func RunDayEnd(ctx context.Context, ps Store, cs CandleSource, symbols []string, asOf time.Time, cfg Config, force, dryRun bool) (*Report, error) {
	rep := &Report{Date: asOf, Mode: "eod", GateOpen: true}

	acct, err := ps.Account(ctx)
	if err != nil {
		return nil, err
	}
	// Idempotency guard: the cycle queues entries for the NEXT open, so running
	// it twice for the same day would prematurely fill them. Refuse to re-run a
	// day that has already been processed (or run an older day out of order),
	// unless --force. Dry-run previews are never blocked (they persist nothing).
	if !dryRun && !force && acct != nil && acct.LastEOD.Valid && !istDateAfter(asOf, acct.LastEOD.Time) {
		return nil, fmt.Errorf("%w: last processed %s (use --force to re-run, or advance --as-of)",
			ErrAlreadyProcessed, acct.LastEOD.Time.In(ist).Format("2006-01-02"))
	}
	if acct == nil {
		if !dryRun {
			if err := ps.InitAccount(ctx, cfg.StartCapital); err != nil {
				return nil, err
			}
		}
		acct = &store.PaperAccount{StartCapital: cfg.StartCapital, Cash: cfg.StartCapital}
		rep.log("initialised paper account with %.0f", cfg.StartCapital)
	}
	cash := acct.Cash

	// Load candles up to asOf for every symbol once.
	history := make(map[string][]models.Candle, len(symbols))
	for _, sym := range symbols {
		cc, err := cs.GetCandles(ctx, sym, store.CandleFilter{To: &asOf})
		if err == nil && len(cc) > 0 {
			history[sym] = cc
		}
	}
	candleToday := func(sym string) (models.Candle, bool) {
		cc := history[sym]
		if len(cc) == 0 {
			return models.Candle{}, false
		}
		last := cc[len(cc)-1]
		if sameDay(last.Timestamp, asOf) {
			return last, true
		}
		return models.Candle{}, false
	}
	mark := func(sym string) float64 {
		cc := history[sym]
		if len(cc) == 0 {
			return 0
		}
		return cc[len(cc)-1].Close
	}

	openPos, err := ps.Positions(ctx)
	if err != nil {
		return nil, err
	}
	heldOrPending := map[string]bool{}
	for _, p := range openPos {
		heldOrPending[p.Symbol] = true
	}

	// Pre-cycle equity for sizing.
	equity := cash
	for _, p := range openPos {
		equity += float64(p.Shares) * mark(p.Symbol)
	}

	// ── 1. Fill pending entries at today's open ───────────────────────────────
	pending, err := ps.Pending(ctx)
	if err != nil {
		return nil, err
	}
	for _, pg := range pending {
		today, ok := candleToday(pg.Symbol)
		if !ok {
			rep.log("pending %s: no candle on %s — dropped", pg.Symbol, asOf.Format("02-Jan"))
			continue
		}
		entry := buyFill(today.Open, cfg.slip())
		notional := riskNotional(equity, cash, entry, pg.SL, cfg)
		shares := int64(math.Floor(notional / (entry * (1 + cfg.legCost()))))
		if shares <= 0 {
			rep.log("pending %s: insufficient capital — skipped", pg.Symbol)
			continue
		}
		costBasis := float64(shares) * entry * (1 + cfg.legCost())
		cash -= costBasis
		heldOrPending[pg.Symbol] = true

		if today.Low <= pg.SL { // same-day gap-down stop
			exit := sellFill(pg.SL, cfg.slip())
			proceeds := float64(shares) * exit * (1 - cfg.legCost())
			cash += proceeds
			r := (exit - entry) / (entry - pg.SL)
			if !dryRun {
				_ = ps.InsertTrade(ctx, store.PaperTrade{
					Symbol: pg.Symbol, EntryDate: asOf, ExitDate: asOf, Entry: entry, Exit: exit,
					Shares: shares, SL: pg.SL, RealizedR: r, PnL: proceeds - costBasis, Outcome: "loss",
				})
			}
			rep.log("FILL %s %d @ %.2f → gap-stopped same day @ %.2f (%.2fR)", pg.Symbol, shares, entry, exit, r)
			continue
		}
		if !dryRun {
			_ = ps.InsertPosition(ctx, store.PaperPosition{
				Symbol: pg.Symbol, Shares: shares, Entry: entry, EntryDate: asOf,
				SL: pg.SL, Target: pg.Target, ATR: pg.ATR,
			})
		}
		rep.log("FILL %s %d @ %.2f (SL %.2f, target %.2f)", pg.Symbol, shares, entry, pg.SL, pg.Target)
	}
	if !dryRun {
		_ = ps.ClearPending(ctx)
	}

	// Refresh open positions to include today's fills (for the exit pass).
	// Dry-run doesn't persist fills, but today's fills are skipped by the exit
	// pass anyway (entryDate == asOf), so the prior list is correct there.
	if !dryRun {
		if openPos, err = ps.Positions(ctx); err != nil {
			return nil, err
		}
	}

	// ── 2. Exits on today's candle (positions held from a prior day) ──────────
	var stillOpen []store.PaperPosition
	for _, pos := range openPos {
		if sameDay(pos.EntryDate, asOf) {
			stillOpen = append(stillOpen, pos) // just filled today; evaluate next day
			continue
		}
		cc := history[pos.Symbol]
		today, ok := candleToday(pos.Symbol)
		if !ok {
			stillOpen = append(stillOpen, pos) // no bar today; hold
			continue
		}
		exitTrigger, outcome, exited := exitDecision(pos, cc, today)
		if !exited {
			stillOpen = append(stillOpen, pos)
			continue
		}
		exit := sellFill(exitTrigger, cfg.slip())
		proceeds := float64(pos.Shares) * exit * (1 - cfg.legCost())
		cash += proceeds
		costBasis := float64(pos.Shares) * pos.Entry * (1 + cfg.legCost())
		r := 0.0
		if pos.Entry-pos.SL > 0 {
			r = (exit - pos.Entry) / (pos.Entry - pos.SL)
		}
		if !dryRun {
			_ = ps.InsertTrade(ctx, store.PaperTrade{
				Symbol: pos.Symbol, EntryDate: pos.EntryDate, ExitDate: asOf, Entry: pos.Entry, Exit: exit,
				Shares: pos.Shares, SL: pos.SL, RealizedR: r, PnL: proceeds - costBasis, Outcome: outcome,
			})
			_ = ps.DeletePosition(ctx, pos.Symbol)
		}
		rep.log("EXIT %s %d @ %.2f (%s, %.2fR)", pos.Symbol, pos.Shares, exit, outcome, r)
	}
	openPos = stillOpen

	// Equity after exits.
	equity = cash
	for _, p := range openPos {
		equity += float64(p.Shares) * mark(p.Symbol)
	}

	// ── 2b. Shadow trades — keep the gate measuring while it is closed ────────
	// Without this the gate is a one-way door: once closed, no real trade ever
	// closes, RecentTradeR never refreshes, and it can never reopen. Shadow
	// positions mirror real ones (next-open fill, EMA/stop exit) but use no
	// capital; their realised R is written to paper_trades with shadow=TRUE so it
	// feeds the gate (RecentTradeR) without touching account performance.
	shadowHeld := map[string]bool{}
	if cfg.HealthShadow {
		// A. Fill yesterday's shadow pending at today's open → shadow positions.
		shPending, err := ps.ShadowPending(ctx)
		if err != nil {
			return nil, err
		}
		for _, pg := range shPending {
			today, ok := candleToday(pg.Symbol)
			if !ok || heldOrPending[pg.Symbol] {
				continue
			}
			entry := buyFill(today.Open, cfg.slip())
			if today.Low <= pg.SL { // same-day gap-down stop
				exit := sellFill(pg.SL, cfg.slip())
				if !dryRun {
					_ = ps.InsertShadowTrade(ctx, shadowTrade(pg.Symbol, asOf, asOf, entry, exit, pg.SL, "loss"))
				}
				continue
			}
			if !dryRun {
				_ = ps.InsertShadowPosition(ctx, store.PaperPosition{
					Symbol: pg.Symbol, Entry: entry, EntryDate: asOf, SL: pg.SL, Target: pg.Target, ATR: pg.ATR,
				})
			}
		}
		if !dryRun {
			_ = ps.ClearShadowPending(ctx)
		}

		// B. Exit shadow positions on today's candle → shadow trades (feed gate).
		shPos, err := ps.ShadowPositions(ctx)
		if err != nil {
			return nil, err
		}
		for _, pos := range shPos {
			shadowHeld[pos.Symbol] = true
			if sameDay(pos.EntryDate, asOf) {
				continue // just filled today; evaluate next day
			}
			cc := history[pos.Symbol]
			today, ok := candleToday(pos.Symbol)
			if !ok {
				continue // no bar today; hold
			}
			trigger, outcome, exited := exitDecision(pos, cc, today)
			if !exited {
				continue
			}
			exit := sellFill(trigger, cfg.slip())
			if !dryRun {
				_ = ps.InsertShadowTrade(ctx, shadowTrade(pos.Symbol, pos.EntryDate, asOf, pos.Entry, exit, pos.SL, outcome))
				_ = ps.DeleteShadowPosition(ctx, pos.Symbol)
				delete(shadowHeld, pos.Symbol)
			}
			rep.log("SHADOW EXIT %s @ %.2f (%s) — gate measurement, no capital", pos.Symbol, exit, outcome)
		}
	}

	// ── 3. New entries → pending for tomorrow's open (gated + sized) ──────────
	recentR, err := ps.RecentTradeR(ctx, cfg.HealthWindow)
	if err != nil {
		return nil, err
	}
	rep.GateOpen = healthyAvgR(recentR, cfg.HealthWindow, cfg.HealthMin)
	freeSlots := cfg.MaxPositions - len(openPos)
	if rep.GateOpen && freeSlots > 0 {
		// Real entries resume: queue the top qualifying signals for tomorrow's open.
		for _, sig := range qualifyingSignals(history, cfg, unionSet(heldOrPending, shadowHeld), freeSlots) {
			if !dryRun {
				_ = ps.InsertPending(ctx, store.PaperPending{
					Symbol: sig.Symbol, SignalDate: asOf,
					SL: sig.Trade.StopLoss, Target: sig.Trade.Target, ATR: sig.Trade.ATR,
				})
			}
			heldOrPending[sig.Symbol] = true
			rep.PendingMade++
			rep.log("QUEUE %s for next open (entry≈%.2f, SL %.2f, target %.2f, score %.0f)",
				sig.Symbol, sig.Price, sig.Trade.StopLoss, sig.Trade.Target, sig.Score)
		}
	} else if !rep.GateOpen {
		rep.log("strategy-health gate CLOSED (last %d trades avg R < %.2f) — no new entries", cfg.HealthWindow, cfg.HealthMin)
		// Shadow queue: simulate the trades we WOULD take so the gate keeps
		// measuring and can reopen. No capital is committed.
		if cfg.HealthShadow {
			shadowSlots := cfg.MaxPositions - len(shadowHeld)
			for _, sig := range qualifyingSignals(history, cfg, unionSet(heldOrPending, shadowHeld), shadowSlots) {
				if !dryRun {
					_ = ps.InsertShadowPending(ctx, store.PaperPending{
						Symbol: sig.Symbol, SignalDate: asOf,
						SL: sig.Trade.StopLoss, Target: sig.Trade.Target, ATR: sig.Trade.ATR,
					})
				}
				shadowHeld[sig.Symbol] = true
				rep.log("SHADOW QUEUE %s (gate-closed measurement)", sig.Symbol)
			}
		}
	}

	if !dryRun {
		_ = ps.SetCash(ctx, cash)
		_ = ps.SetLastEOD(ctx, asOf.In(ist)) // mark this day processed (guard)
	}

	rep.Cash = cash
	rep.Equity = equity
	rep.OpenCount = len(openPos)
	rep.Positions = positionViews(openPos, mark)
	return rep, nil
}

// LiveSnapshot is a read-only intraday monitor: marks open positions to the
// provided live prices and flags any whose live price has breached the stop.
func LiveSnapshot(asOf time.Time, positions []store.PaperPosition, livePrice map[string]float64, pending []store.PaperPending, cash float64) *Report {
	rep := &Report{Date: asOf, Mode: "live", GateOpen: true, Cash: cash}
	equity := cash
	for _, p := range positions {
		px := livePrice[p.Symbol]
		if px <= 0 {
			px = p.Entry
		}
		equity += float64(p.Shares) * px
		if px <= p.SL {
			rep.log("⚠ %s live %.2f at/below SL %.2f — would stop out at EOD", p.Symbol, px, p.SL)
		}
	}
	rep.Equity = equity
	rep.OpenCount = len(positions)
	rep.PendingMade = len(pending)
	rep.Positions = positionViews(positions, func(s string) float64 {
		if px := livePrice[s]; px > 0 {
			return px
		}
		return 0
	})
	return rep
}

// — internals —

// shadowTrade builds a PaperTrade record for a closed shadow position: no shares
// and no P&L (capital is never committed), only the realised R that feeds the
// strategy-health gate.
func shadowTrade(symbol string, entryDate, exitDate time.Time, entry, exit, sl float64, outcome string) store.PaperTrade {
	r := 0.0
	if entry-sl > 0 {
		r = (exit - entry) / (entry - sl)
	}
	return store.PaperTrade{
		Symbol: symbol, EntryDate: entryDate, ExitDate: exitDate,
		Entry: entry, Exit: exit, Shares: 0, SL: sl, RealizedR: r, PnL: 0, Outcome: outcome,
	}
}

// qualifyingSignals scans all history and returns up to `limit` signals that
// pass the score/SL filters and are not in `skip`, ordered by score desc with a
// symbol tiebreak so slot allocation is deterministic (scanner inputs are built
// from a map, whose iteration order is randomised).
func qualifyingSignals(history map[string][]models.Candle, cfg Config, skip map[string]bool, limit int) []scanner.StockSignal {
	if limit <= 0 {
		return nil
	}
	var signals []scanner.StockSignal
	if cfg.signalFunc != nil {
		signals = cfg.signalFunc(history, cfg.ScanOpts)
	} else {
		inputs := make([]scanner.Input, 0, len(history))
		for sym, cc := range history {
			inputs = append(inputs, scanner.Input{Symbol: sym, Candles: cc})
		}
		signals = scanner.Scan(inputs, cfg.ScanOpts)
	}
	sort.SliceStable(signals, func(i, j int) bool {
		if signals[i].Score != signals[j].Score {
			return signals[i].Score > signals[j].Score
		}
		return signals[i].Symbol < signals[j].Symbol
	})
	out := make([]scanner.StockSignal, 0, limit)
	for _, sig := range signals {
		if len(out) >= limit {
			break
		}
		if cfg.MinScore > 0 && sig.Score < cfg.MinScore {
			continue
		}
		if skip[sig.Symbol] {
			continue
		}
		if sig.Trade.StopLoss <= 0 || sig.Price <= sig.Trade.StopLoss {
			continue
		}
		out = append(out, sig)
	}
	return out
}

// unionSet returns the union of two string sets (a fresh map; inputs untouched).
func unionSet(a, b map[string]bool) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// exitDecision applies SL-first then EMA7<EMA21 recross on today's candle.
func exitDecision(pos store.PaperPosition, cc []models.Candle, today models.Candle) (trigger float64, outcome string, exited bool) {
	if today.Low <= pos.SL {
		return pos.SL, "loss", true
	}
	closes := make([]float64, len(cc))
	for i, c := range cc {
		closes[i] = c.Close
	}
	ema7, _ := analysis.EMA(closes, 7)
	ema21, _ := analysis.EMA(closes, 21)
	n := len(closes)
	if n > 0 && ema7[n-1] > 0 && ema21[n-1] > 0 && ema7[n-1] < ema21[n-1] {
		return today.Close, "exit", true
	}
	return 0, "", false
}

func positionViews(positions []store.PaperPosition, mark func(string) float64) []PositionView {
	out := make([]PositionView, 0, len(positions))
	for _, p := range positions {
		m := mark(p.Symbol)
		if m <= 0 {
			m = p.Entry
		}
		pnl := float64(p.Shares) * (m - p.Entry)
		pct := 0.0
		if p.Entry > 0 {
			pct = (m - p.Entry) / p.Entry * 100
		}
		out = append(out, PositionView{
			Symbol: p.Symbol, Shares: p.Shares, Entry: p.Entry, Mark: m,
			SL: p.SL, Target: p.Target, UnrealPnL: pnl, UnrealPct: pct, EntryDate: p.EntryDate,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

// ist is the NSE session timezone. Kite daily candles are stamped at IST
// midnight, so calendar-day comparisons must be done in IST (their UTC instant
// falls on the previous day).
var ist = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return time.FixedZone("IST", 5*3600+1800)
	}
	return loc
}()

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.In(ist).Date()
	by, bm, bd := b.In(ist).Date()
	return ay == by && am == bm && ad == bd
}

// istDateAfter reports whether a's IST calendar date is strictly after b's.
func istDateAfter(a, b time.Time) bool {
	ay, am, ad := a.In(ist).Date()
	by, bm, bd := b.In(ist).Date()
	if ay != by {
		return ay > by
	}
	if am != bm {
		return am > bm
	}
	return ad > bd
}
