// Command paper-trade runs a persistent, forward paper-trading session of the
// validated swing strategy (EMA-recross exit, risk-based sizing, strategy-health
// gate, costs). State lives in PostgreSQL so a session continues across days.
//
// The strategy is daily, so there are two modes:
//
//	--mode eod    Authoritative once-per-day cycle (run AFTER the close, after
//	              kite-sync has the day's candle): fill yesterday's queued entries
//	              at today's open, process exits on today's candle, queue
//	              tomorrow's entries. Persists state.
//	--mode live   Read-only intraday monitor (run DURING market hours): marks open
//	              positions to live Kite prices, flags stop breaches. No state change.
//
// Daily workflow:
//
//	# during the session
//	go run ./cmd/paper-trade --mode live
//	# after the close
//	go run ./cmd/kite-sync --period 1y
//	go run ./cmd/paper-trade --mode eod
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/internal/display"
	"github.com/sahiltyagi27/stock-market-analysis/internal/kite"
	"github.com/sahiltyagi27/stock-market-analysis/internal/paper"
	"github.com/sahiltyagi27/stock-market-analysis/internal/scanner"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
)

func main() {
	mode := flag.String("mode", "eod", "eod (daily cycle, after close) or live (intraday monitor)")
	symbolsFile := flag.String("symbols", "config/symbols.txt", "watchlist file")
	capital := flag.Float64("capital", 100000, "starting paper capital (only used on first init)")
	asOfStr := flag.String("as-of", "", "[eod] cycle date YYYY-MM-DD (default: today)")
	dryRun := flag.Bool("dry-run", false, "[eod] compute and print the cycle without persisting")
	reset := flag.Bool("reset", false, "wipe all paper state (account, positions, pending, trades) and exit")
	exchange := flag.String("exchange", "NSE", "Kite exchange (for live mode)")

	// Strategy parameters — defaults match the validated portfolio config.
	maxPositions := flag.Int("max-positions", 5, "max concurrent positions")
	riskPct := flag.Float64("risk-pct", 1.0, "risk-based sizing: stop-out costs this %% of equity (≤0 = equal slices)")
	maxWeightPct := flag.Float64("max-weight-pct", 25, "cap any single position at this %% of equity")
	healthWindow := flag.Int("health-window", 20, "strategy-health gate window (0 = off)")
	healthMin := flag.Float64("health-min", 0, "min avg R over the health window")
	minScore := flag.Float64("min-score", 60, "minimum signal score to queue an entry")
	minRR := flag.Float64("min-rr", 2.0, "minimum risk/reward for the swing scanner")
	costPct := flag.Float64("cost-pct", 0.25, "round-trip transaction cost %%")
	slippagePct := flag.Float64("slippage-pct", 0.20, "per-leg slippage %%")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	db, err := sql.Open("postgres", cfg.DSN())
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}

	ps := store.NewPaperStore(db)
	if err := ps.Migrate(ctx); err != nil {
		log.Fatalf("paper migrate: %v", err)
	}
	cs := store.NewCandleStore(db)

	if *reset {
		if err := ps.Reset(ctx); err != nil {
			log.Fatalf("reset: %v", err)
		}
		fmt.Println("paper state wiped — next eod run starts a fresh account.")
		return
	}

	symbols, err := config.LoadSymbols(*symbolsFile)
	if err != nil {
		log.Fatalf("symbols: %v", err)
	}

	pcfg := paper.Config{
		StartCapital: *capital,
		MaxPositions: *maxPositions,
		RiskPct:      *riskPct,
		MaxWeightPct: *maxWeightPct,
		HealthWindow: *healthWindow,
		HealthMin:    *healthMin,
		MinScore:     *minScore,
		CostPct:      *costPct,
		SlippagePct:  *slippagePct,
		ScanOpts: scanner.Options{
			MinRR:    *minRR,
			ZoneOpts: analysis.ZoneOptions{},
		},
	}

	switch *mode {
	case "eod":
		runEOD(ctx, ps, cs, symbols, *asOfStr, pcfg, *dryRun)
	case "live":
		runLive(ctx, ps, cfg, *exchange)
	default:
		log.Fatalf("--mode must be eod or live, got %q", *mode)
	}
}

func runEOD(ctx context.Context, ps *store.PaperStore, cs *store.CandleStore, symbols []string, asOfStr string, pcfg paper.Config, dryRun bool) {
	asOf := time.Now()
	if asOfStr != "" {
		t, err := time.Parse("2006-01-02", asOfStr)
		if err != nil {
			log.Fatalf("--as-of: invalid date %q", asOfStr)
		}
		asOf = t
	}
	log.Printf("paper EOD cycle as-of %s (dry-run=%v) over %d symbols", asOf.Format("2006-01-02"), dryRun, len(symbols))
	rep, err := paper.RunDayEnd(ctx, ps, cs, symbols, asOf, pcfg, dryRun)
	if err != nil {
		log.Fatalf("eod cycle: %v", err)
	}
	printReport(rep, pcfg.StartCapital)
}

func runLive(ctx context.Context, ps *store.PaperStore, cfg *config.Config, exchange string) {
	if cfg.KiteAPIKey == "" || cfg.KiteAccessToken == "" {
		log.Fatal("live mode needs KITE_API_KEY and KITE_ACCESS_TOKEN (run cmd/kite-token)")
	}
	positions, err := ps.Positions(ctx)
	if err != nil {
		log.Fatalf("positions: %v", err)
	}
	pending, _ := ps.Pending(ctx)
	acct, _ := ps.Account(ctx)
	cash := 0.0
	if acct != nil {
		cash = acct.Cash
	}
	if len(positions) == 0 {
		fmt.Println("No open paper positions to monitor.")
		if len(pending) > 0 {
			fmt.Printf("%d entr(ies) queued to fill at the next EOD cycle.\n", len(pending))
		}
		return
	}

	// Resolve instrument tokens for the open-position symbols.
	client := kite.NewClient(cfg.KiteBaseURL, cfg.KiteAPIKey, cfg.KiteAccessToken)
	instruments, err := client.Instruments(ctx, exchange)
	if err != nil {
		log.Fatalf("kite instruments: %v", err)
	}
	tokenSymbol := map[uint32]string{}
	var tokens []uint32
	for _, p := range positions {
		inst, ok := kite.FindEquityInstrument(instruments, exchange, p.Symbol)
		if !ok {
			continue
		}
		tok := uint32(inst.InstrumentToken)
		tokenSymbol[tok] = p.Symbol
		tokens = append(tokens, tok)
	}

	ws := kite.NewWSClient(cfg.KiteAPIKey, cfg.KiteAccessToken, tokenSymbol)
	go func() {
		if err := ws.Run(ctx, tokens); err != nil {
			log.Printf("ws: %v", err)
		}
	}()
	log.Printf("connecting to Kite WebSocket — waiting 6s for ticks on %d positions…", len(tokens))
	select {
	case <-time.After(6 * time.Second):
	case <-ctx.Done():
		return
	}

	livePrice := map[string]float64{}
	for tok, sym := range tokenSymbol {
		if t, ok := ws.LatestTick(tok); ok && t.LastPrice > 0 {
			livePrice[sym] = t.LastPrice
		}
	}
	rep := paper.LiveSnapshot(time.Now(), positions, livePrice, pending, cash)
	printReport(rep, 0)
}

func printReport(rep *paper.Report, startCapital float64) {
	banner := fmt.Sprintf("━━━  Paper %s  %s  ━━━", titleMode(rep.Mode), rep.Date.Format("02-Jan-2006"))
	fmt.Printf("\n%s\n", display.BoldCyan.Sprint(banner))

	if len(rep.Actions) > 0 {
		fmt.Println()
		for _, a := range rep.Actions {
			fmt.Printf("  %s %s\n", display.Cyan.Sprint("•"), a)
		}
	}

	if len(rep.Positions) > 0 {
		fmt.Printf("\n  %s\n", display.Dim.Sprint("Open positions:"))
		for _, p := range rep.Positions {
			pnl := display.Sign(p.UnrealPnL, "%+.0f")
			pct := display.Sign(p.UnrealPct, "%+.1f%%")
			fmt.Printf("     %s %s %d @ %.2f  →  mark %.2f  (%s / %s)  SL %.2f\n",
				display.Dim.Sprint("├"), display.BoldWhite.Sprintf("%-12s", p.Symbol),
				p.Shares, p.Entry, p.Mark, pnl, pct, p.SL)
		}
	}

	sep := display.Dim.Sprint("──────────────────────────────────────────────")
	fmt.Printf("\n  %s\n", sep)
	if rep.Mode == "eod" {
		gate := display.Green.Sprint("OPEN")
		if !rep.GateOpen {
			gate = display.Red.Sprint("CLOSED")
		}
		fmt.Printf("  %s %s   %s %d   %s %d\n",
			display.Dim.Sprint("Health gate:"), gate,
			display.Dim.Sprint("Open:"), rep.OpenCount,
			display.Dim.Sprint("Queued for next open:"), rep.PendingMade)
	}
	fmt.Printf("  %s %s   %s %s\n",
		display.Dim.Sprint("Cash:"), display.Bold.Sprintf("%.0f", rep.Cash),
		display.Dim.Sprint("Equity:"), display.Bold.Sprintf("%.0f", rep.Equity))
	if startCapital > 0 {
		ret := (rep.Equity - startCapital) / startCapital * 100
		fmt.Printf("  %s %s\n", display.Dim.Sprint("Return vs start:"), display.Sign(ret, "%+.2f%%"))
	}
	fmt.Printf("  %s\n", sep)
}

func titleMode(m string) string {
	if m == "live" {
		return "Live Monitor"
	}
	return "Day-End Cycle"
}
