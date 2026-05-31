// Command scan fetches daily OHLCV data for a watchlist, upserts it into
// PostgreSQL, runs the scanner engine, and prints ranked signal candidates.
//
// Usage:
//
//	go run ./cmd/scan [flags]
//
// Flags:
//
//	--symbols   path to watchlist file   (default: config/symbols.txt)
//	--period    history window           (default: 2y)
//	--top       max signals to print     (default: 5)
//	--min-rr    minimum risk/reward      (default: 2.0)
//	--dry-run   skip DB writes           (default: false)
//
// Note: output is for watchlist research purposes only, not buy recommendations.
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
	"github.com/sahiltyagi27/stock-market-analysis/internal/fetcher"
	"github.com/sahiltyagi27/stock-market-analysis/internal/scanner"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

func main() {
	symbolsFile := flag.String("symbols", "config/symbols.txt", "path to watchlist file")
	period := flag.String("period", "2y", "history window (e.g. 2y, 6m, 90d)")
	topN := flag.Int("top", 5, "number of top signals to print")
	minRR := flag.Float64("min-rr", 2.0, "minimum risk/reward ratio")
	dryRun := flag.Bool("dry-run", false, "fetch and scan without writing to DB")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --- Load symbol watchlist ---
	symbols, err := config.LoadSymbols(*symbolsFile)
	if err != nil {
		log.Fatalf("symbols: %v", err)
	}
	log.Printf("loaded %d symbols from %s", len(symbols), *symbolsFile)

	// --- DB setup (skipped on dry-run) ---
	var candleStore *store.CandleStore
	if !*dryRun {
		cfg, err := config.Load()
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		db, err := sql.Open("postgres", cfg.DSN())
		if err != nil {
			log.Fatalf("db open: %v", err)
		}
		defer db.Close()
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(5 * time.Minute)

		if err := db.PingContext(ctx); err != nil {
			log.Fatalf("db ping: %v", err)
		}
		candleStore = store.NewCandleStore(db)
		if err := candleStore.Migrate(ctx); err != nil {
			log.Fatalf("migrate: %v", err)
		}
	} else {
		log.Println("dry-run: DB writes disabled")
	}

	// --- Fetch + load + build scanner inputs ---
	yf := fetcher.NewYahooFetcher()
	var inputs []scanner.Input
	fetchFailed := 0

	for _, sym := range symbols {
		normalized := fetcher.NormalizeSymbol(sym)

		log.Printf("fetching %s (%s)…", sym, *period)
		candles, err := yf.FetchDaily(sym, *period)
		if err != nil {
			log.Printf("  skip %s: fetch failed: %v", normalized, err)
			fetchFailed++
			continue
		}
		log.Printf("  fetched %d candles for %s", len(candles), normalized)

		var dbCandles []models.Candle
		if !*dryRun {
			// Persist fresh data.
			if err := candleStore.UpsertCandles(ctx, candles); err != nil {
				log.Printf("  skip %s: upsert failed: %v", normalized, err)
				fetchFailed++
				continue
			}
			// Read the full window back from DB (combines new + existing history).
			from := time.Now().AddDate(-2, 0, 0)
			dbCandles, err = candleStore.GetCandles(ctx, normalized, store.CandleFilter{From: &from})
			if err != nil {
				log.Printf("  skip %s: db read failed: %v", normalized, err)
				fetchFailed++
				continue
			}
			log.Printf("  %d candles available in DB for %s", len(dbCandles), normalized)
		} else {
			dbCandles = candles
		}

		inputs = append(inputs, scanner.Input{Symbol: normalized, Candles: dbCandles})
	}

	// --- Run scanner ---
	opts := scanner.Options{MinRR: *minRR}
	signals, scanErrs := scanner.ScanWithErrors(inputs, opts)
	scanSkipped := len(scanErrs)

	for sym, err := range scanErrs {
		log.Printf("  filter %s: %v", sym, err)
	}

	// --- Print results ---
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║      Top Watchlist Candidates        ║")
	fmt.Println("║  (research only — not buy signals)   ║")
	fmt.Println("╚══════════════════════════════════════╝")

	top := *topN
	if top > len(signals) {
		top = len(signals)
	}

	if top == 0 {
		fmt.Println("\nNo bullish setups found matching the criteria.")
	}

	for i, sig := range signals[:top] {
		fmt.Printf("\n%d. %s\n", i+1, sig.Symbol)
		fmt.Printf("   Score:      %.1f / 100\n", sig.Score)
		fmt.Printf("   Price:      %.2f\n", sig.Price)
		fmt.Printf("   Trend:      %s\n", sig.Trend)
		fmt.Printf("   R/R:        %.2f  (%s)\n", sig.Trade.RiskReward, sig.Trade.Quality)
		fmt.Printf("   Entry:      %.2f   SL: %.2f   Target: %.2f\n",
			sig.Trade.Entry, sig.Trade.StopLoss, sig.Trade.Target)
		fmt.Printf("   Support:    %.2f – %.2f  (%d touches)\n",
			sig.Support.Low, sig.Support.High, sig.Support.Touches)
		fmt.Printf("   Resistance: %.2f – %.2f  (%d touches)\n",
			sig.Resistance.Low, sig.Resistance.High, sig.Resistance.Touches)
		fmt.Println("   Reasons:")
		for _, r := range sig.Reasons {
			fmt.Printf("     • %s\n", r)
		}
	}

	// --- Summary ---
	fmt.Println()
	fmt.Println(strings.Repeat("─", 42))
	fmt.Printf("Scanned:  %d symbols\n", len(symbols))
	fmt.Printf("Skipped:  %d (fetch/db errors: %d, no setup: %d)\n",
		fetchFailed+scanSkipped, fetchFailed, scanSkipped)
	fmt.Printf("Signals:  %d\n", len(signals))
	if *dryRun {
		fmt.Println("Mode:     dry-run (no DB writes)")
	}
}
