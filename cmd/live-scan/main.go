// Command live-scan subscribes to all watchlist symbols via the Kite WebSocket
// feed (full mode) and runs the scanner engine every 2 minutes during NSE
// market hours (09:15–15:30 IST, Mon–Fri).
//
// Each scan run merges 2 years of historical candles from PostgreSQL with the
// current live tick as today's candle, then ranks signals exactly as the
// offline scanner does — EMA trend filter, zone detection, R/R grading, score.
//
// Usage:
//
//	go run ./cmd/live-scan
//	go run ./cmd/live-scan --top 10 --interval 2m --min-rr 2.0
//
// Required environment variables:
//
//	KITE_API_KEY
//	KITE_ACCESS_TOKEN   (refresh daily via cmd/kite-token)
//
// Optional flags:
//
//	--symbols   path to watchlist file     (default: config/symbols.txt)
//	--top       signals to print per run   (default: 10)
//	--min-rr    minimum risk/reward ratio  (default: 2.0)
//	--interval  scan interval              (default: 2m)
//	--period    historical candle window   (default: 2y)
//	--exchange  Kite exchange              (default: NSE)
//	--dev       disable market hours check (default: false)
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/kite"
	"github.com/sahiltyagi27/stock-market-analysis/internal/scanner"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

var ist *time.Location

func init() {
	var err error
	ist, err = time.LoadLocation("Asia/Kolkata")
	if err != nil {
		log.Fatalf("load IST timezone: %v", err)
	}
}

func main() {
	symbolsFile := flag.String("symbols", "config/symbols.txt", "path to watchlist file")
	topN := flag.Int("top", 10, "signals to print per scan run")
	minRR := flag.Float64("min-rr", 2.0, "minimum risk/reward ratio")
	interval := flag.Duration("interval", 2*time.Minute, "scan interval (e.g. 2m, 30s)")
	period := flag.String("period", "2y", "historical candle window for EMA/zone computation")
	exchange := flag.String("exchange", "NSE", "Kite exchange")
	dev := flag.Bool("dev", false, "disable market hours check (useful for testing)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.KiteAPIKey == "" || cfg.KiteAccessToken == "" {
		log.Fatal("KITE_API_KEY and KITE_ACCESS_TOKEN are required (run cmd/kite-token to refresh)")
	}

	// ── Symbols ───────────────────────────────────────────────────────────────
	symbols, err := config.LoadSymbols(*symbolsFile)
	if err != nil {
		log.Fatalf("symbols: %v", err)
	}
	log.Printf("loaded %d symbols from %s", len(symbols), *symbolsFile)

	// ── DB: historical candle cache ───────────────────────────────────────────
	db, err := sql.Open("postgres", cfg.DSN())
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}
	candleStore := store.NewCandleStore(db)

	from, err := parsePeriod(*period, time.Now())
	if err != nil {
		log.Fatalf("period: %v", err)
	}

	log.Printf("loading historical candles from DB (window: %s)…", *period)
	historyCache := make(map[string][]models.Candle, len(symbols))
	for _, rawSym := range symbols {
		sym := kite.NormalizeSymbol(rawSym)
		candles, err := candleStore.GetCandles(ctx, sym, store.CandleFilter{From: &from})
		if err != nil {
			log.Printf("  warn: DB read failed for %s: %v", sym, err)
			continue
		}
		if len(candles) > 0 {
			historyCache[sym] = candles
		}
	}
	log.Printf("cached history for %d/%d symbols", len(historyCache), len(symbols))

	// ── Kite: instrument token map ────────────────────────────────────────────
	kiteClient := kite.NewClient(cfg.KiteBaseURL, cfg.KiteAPIKey, cfg.KiteAccessToken)
	instruments, err := kiteClient.Instruments(ctx, *exchange)
	if err != nil {
		log.Fatalf("kite instruments: %v", err)
	}
	log.Printf("fetched %d %s instruments from Kite", len(instruments), *exchange)

	tokenSymbol := make(map[uint32]string, len(symbols))
	symbolToken := make(map[string]uint32, len(symbols))
	var tokens []uint32

	for _, rawSym := range symbols {
		sym := kite.NormalizeSymbol(rawSym)
		inst, ok := kite.FindEquityInstrument(instruments, *exchange, sym)
		if !ok {
			continue
		}
		tok := uint32(inst.InstrumentToken)
		tokenSymbol[tok] = sym
		symbolToken[sym] = tok
		tokens = append(tokens, tok)
	}
	log.Printf("mapped %d/%d symbols to instrument tokens", len(tokens), len(symbols))
	if len(tokens) == 0 {
		log.Fatal("no instrument tokens resolved — check KITE_ACCESS_TOKEN and exchange")
	}

	// ── WebSocket ─────────────────────────────────────────────────────────────
	ws := kite.NewWSClient(cfg.KiteAPIKey, cfg.KiteAccessToken, tokenSymbol)
	go func() {
		if err := ws.Run(ctx, tokens); err != nil {
			log.Printf("ws error: %v", err)
		}
	}()

	// Allow time for the initial batch of ticks to arrive before first scan.
	log.Printf("connecting to Kite WebSocket — waiting 5s for initial ticks…")
	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		return
	}

	// ── Scan loop ─────────────────────────────────────────────────────────────
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	log.Printf("live scan ready — interval %s | top %d | min R/R %.1f", *interval, *topN, *minRR)
	if *dev {
		log.Printf("⚠  --dev mode: market hours check disabled")
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down")
			return
		case t := <-ticker.C:
			if !*dev && !isMarketOpen(t) {
				log.Printf("[%s IST] outside market hours (09:15–15:30 Mon–Fri) — skipping",
					t.In(ist).Format("15:04:05"))
				continue
			}
			runScan(t, ws, historyCache, symbolToken,
				scanner.Options{MinRR: *minRR}, *topN)
		}
	}
}

// runScan builds scanner inputs by merging historical candles with the latest
// live tick for each symbol, runs the scanner, and prints results.
func runScan(
	at time.Time,
	ws *kite.WSClient,
	history map[string][]models.Candle,
	symbolToken map[string]uint32,
	opts scanner.Options,
	topN int,
) {
	var inputs []scanner.Input
	noTick := 0

	for sym, candles := range history {
		tok, ok := symbolToken[sym]
		if !ok {
			continue
		}
		tick, ok := ws.LatestTick(tok)
		if !ok || tick.LastPrice <= 0 {
			noTick++
			continue
		}
		inputs = append(inputs, buildInput(sym, candles, tick))
	}

	signals, _ := scanner.ScanWithErrors(inputs, opts)
	printScan(at, signals, topN, len(history), noTick)
}

// buildInput creates a scanner.Input by appending (or replacing) a synthetic
// "live candle" built from the current tick onto the historical candle slice.
//
// If the latest historical candle is from today (same IST calendar day as the
// tick), it is replaced so the scanner sees a continuously updated intraday
// candle. Otherwise the live candle is appended as a new day.
func buildInput(sym string, historical []models.Candle, tick kite.Tick) scanner.Input {
	now := time.Now().UTC()
	open := tick.Open
	if open <= 0 {
		open = tick.LastPrice // fallback if intraday open not yet available
	}
	live := models.Candle{
		Symbol:    sym,
		Timestamp: now,
		Open:      open,
		High:      tick.High,
		Low:       tick.Low,
		Close:     tick.LastPrice, // LTP as the "current price"
		Volume:    int64(tick.Volume),
	}

	merged := make([]models.Candle, len(historical))
	copy(merged, historical)

	if len(merged) > 0 && sameDay(merged[len(merged)-1].Timestamp, now) {
		merged[len(merged)-1] = live // replace today's candle with live data
	} else {
		merged = append(merged, live)
	}
	return scanner.Input{Symbol: sym, Candles: merged}
}

// printScan prints ranked signals to stdout in a compact terminal-friendly format.
func printScan(at time.Time, signals []scanner.StockSignal, topN, total, noTick int) {
	stamp := at.In(ist).Format("02-Jan-2006  15:04:05")
	banner := fmt.Sprintf("━━━  Live Scan  %s IST  ━━━", stamp)
	fmt.Printf("\n%s\n", banner)

	top := topN
	if top > len(signals) {
		top = len(signals)
	}
	if top == 0 {
		fmt.Println("  No bullish setups found.")
	}

	for i, sig := range signals[:top] {
		fmt.Printf("\n  %d. %-14s  ₹%-9.2f  Score: %.0f/100\n",
			i+1, sig.Symbol, sig.Price, sig.Score)
		fmt.Printf("     Trend: %-8s  R/R: %.2f (%s)\n",
			sig.Trend, sig.Trade.RiskReward, sig.Trade.Quality)
		fmt.Printf("     Entry: %-9.2f  SL: %-9.2f  Target: %.2f\n",
			sig.Trade.Entry, sig.Trade.StopLoss, sig.Trade.Target)
		fmt.Printf("     Support:    %.2f–%.2f (%d touches)\n",
			sig.Support.Low, sig.Support.High, sig.Support.Touches)
		fmt.Printf("     Resistance: %.2f–%.2f (%d touches)\n",
			sig.Resistance.Low, sig.Resistance.High, sig.Resistance.Touches)
		fmt.Println("     Reasons:")
		for _, r := range sig.Reasons {
			fmt.Printf("       • %s\n", r)
		}
	}

	fmt.Printf("\n  %s\n", strings.Repeat("─", 54))
	fmt.Printf("  Scanned: %-4d  Signals: %-4d  No tick yet: %d\n",
		total, len(signals), noTick)
	fmt.Printf("%s\n", strings.Repeat("━", len(banner)))
}

// isMarketOpen returns true if t is within NSE trading hours:
// Monday–Friday, 09:15–15:30 IST.
func isMarketOpen(t time.Time) bool {
	local := t.In(ist)
	switch local.Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	y, m, d := local.Date()
	open := time.Date(y, m, d, 9, 15, 0, 0, ist)
	close_ := time.Date(y, m, d, 15, 30, 0, 0, ist)
	return !local.Before(open) && !local.After(close_)
}

// sameDay reports whether two timestamps fall on the same IST calendar day.
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.In(ist).Date()
	by, bm, bd := b.In(ist).Date()
	return ay == by && am == bm && ad == bd
}

func parsePeriod(period string, from time.Time) (time.Time, error) {
	if len(period) < 2 {
		return time.Time{}, fmt.Errorf("invalid period %q: must be like 2y, 6m, 90d", period)
	}
	unit := period[len(period)-1]
	var n int
	if _, err := fmt.Sscanf(period[:len(period)-1], "%d", &n); err != nil || n <= 0 {
		return time.Time{}, fmt.Errorf("invalid period %q: number must be positive", period)
	}
	switch unit {
	case 'y', 'Y':
		return from.AddDate(-n, 0, 0), nil
	case 'm', 'M':
		return from.AddDate(0, -n, 0), nil
	case 'd', 'D':
		return from.AddDate(0, 0, -n), nil
	default:
		return time.Time{}, fmt.Errorf("invalid period unit %q: use y, m, or d", string(unit))
	}
}
