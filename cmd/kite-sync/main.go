// Command kite-sync downloads daily candles from Kite Connect and stores them
// in PostgreSQL for scanner use.
//
// Usage:
//
//	go run ./cmd/kite-sync --symbols config/symbols.txt --period 2y
//
// Required environment variables:
//
//	KITE_API_KEY
//	KITE_ACCESS_TOKEN
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
	"github.com/sahiltyagi27/stock-market-analysis/internal/kite"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
)

func main() {
	symbolsFile := flag.String("symbols", "config/symbols.txt", "path to watchlist file")
	exchange := flag.String("exchange", "NSE", "Kite exchange")
	period := flag.String("period", "2y", "history window (e.g. 2y, 6m, 90d)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.KiteAPIKey == "" || cfg.KiteAccessToken == "" {
		log.Fatal("KITE_API_KEY and KITE_ACCESS_TOKEN are required")
	}

	symbols, err := config.LoadSymbols(*symbolsFile)
	if err != nil {
		log.Fatalf("symbols: %v", err)
	}
	log.Printf("loaded %d symbols from %s", len(symbols), *symbolsFile)

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

	candleStore := store.NewCandleStore(db)
	if err := candleStore.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	to := time.Now()
	from, err := parsePeriod(*period, to)
	if err != nil {
		log.Fatalf("period: %v", err)
	}

	client := kite.NewClient(cfg.KiteBaseURL, cfg.KiteAPIKey, cfg.KiteAccessToken)
	instruments, err := client.Instruments(ctx, *exchange)
	if err != nil {
		log.Fatalf("kite instruments: %v", err)
	}
	log.Printf("loaded %d %s instruments from Kite", len(instruments), *exchange)

	var synced, skipped int
	for _, rawSymbol := range symbols {
		symbol := kite.NormalizeSymbol(rawSymbol)
		inst, ok := kite.FindEquityInstrument(instruments, *exchange, symbol)
		if !ok {
			log.Printf("skip %s: no %s equity instrument found", symbol, *exchange)
			skipped++
			continue
		}

		candles, err := client.HistoricalDaily(ctx, inst.InstrumentToken, symbol, from, to)
		if err != nil {
			log.Printf("skip %s: historical fetch failed: %v", symbol, err)
			skipped++
			continue
		}
		if len(candles) == 0 {
			log.Printf("skip %s: Kite returned no candles", symbol)
			skipped++
			continue
		}
		if err := candleStore.UpsertCandles(ctx, candles); err != nil {
			log.Printf("skip %s: DB upsert failed: %v", symbol, err)
			skipped++
			continue
		}
		log.Printf("synced %d daily candles for %s", len(candles), symbol)
		synced++
	}

	fmt.Println()
	fmt.Printf("Symbols: %d\n", len(symbols))
	fmt.Printf("Synced:  %d\n", synced)
	fmt.Printf("Skipped: %d\n", skipped)
}

func parsePeriod(period string, from time.Time) (time.Time, error) {
	if len(period) < 2 {
		return time.Time{}, fmt.Errorf("invalid period %q: must be like 2y, 6m, 90d", period)
	}
	unit := period[len(period)-1]
	var n int
	if _, err := fmt.Sscanf(period[:len(period)-1], "%d", &n); err != nil || n <= 0 {
		return time.Time{}, fmt.Errorf("invalid period %q: number must be a positive integer", period)
	}
	switch unit {
	case 'y', 'Y':
		return from.AddDate(-n, 0, 0), nil
	case 'm', 'M':
		return from.AddDate(0, -n, 0), nil
	case 'd', 'D':
		return from.AddDate(0, 0, -n), nil
	default:
		return time.Time{}, fmt.Errorf("invalid period unit %q in %q: use y, m, or d", string(unit), period)
	}
}
