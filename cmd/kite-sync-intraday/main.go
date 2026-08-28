// Command kite-sync-intraday downloads sub-daily candles from Kite Connect
// (5-minute bars by default) and stores them in PostgreSQL, for day-trading
// strategy work. Mirrors cmd/kite-sync's worker-pool/rate-limit/chunking
// pattern, but against a separate intraday_candles table (see
// internal/store/intraday_candle_store.go) — the daily pipeline is untouched.
//
// Kite enforces a much shorter per-request [from, to) span for intraday
// intervals than for daily candles, so backfilling any real history means
// chunking into many small windows. The per-interval limits below were
// verified against Kite's actual API for 5minute (its documented limit,
// confirmed empirically: a 120-day request is rejected with "interval
// exceeds max limit: 100 days", a 100-day request succeeds); the other
// intervals use Kite's documented limits with a safety margin, not yet
// independently re-verified.
//
// Usage:
//
//	go run ./cmd/kite-sync-intraday --period 60d
//	go run ./cmd/kite-sync-intraday --interval 15minute --period 90d
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/kite"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// maxChunkDaysByInterval is the widest single from/to span requested per Kite
// call for each interval, kept a few days under Kite's actual limit as a
// safety margin against date-boundary rounding. Only "5minute" has been
// empirically confirmed this session (Kite's real limit is exactly 100
// days); the rest are Kite's documented limits, used conservatively.
var maxChunkDaysByInterval = map[string]int{
	"minute":   55,  // documented limit: 60 days
	"3minute":  95,  // documented limit: 100 days
	"5minute":  95,  // CONFIRMED: Kite rejects >100 days for this interval
	"10minute": 95,  // documented limit: 100 days
	"15minute": 190, // documented limit: 200 days
	"30minute": 190, // documented limit: 200 days
	"60minute": 390, // documented limit: 400 days
	"day":      1900,
}

func main() {
	symbolsFile := flag.String("symbols", "config/symbols-intraday.txt", "path to watchlist file")
	interval := flag.String("interval", "5minute", "Kite candle interval: minute, 3minute, 5minute, 10minute, 15minute, 30minute, 60minute")
	exchange := flag.String("exchange", "NSE", "Kite exchange")
	period := flag.String("period", "60d", "history window (e.g. 60d, 30d), relative to now. Overridden by --from. Auto-chunked per --interval's Kite limit.")
	fromStr := flag.String("from", "", "absolute start date YYYY-MM-DD (overrides --period)")
	workers := flag.Int("workers", 6, "parallel workers for the per-symbol sync")
	rate := flag.Int("rate", 6, "max Kite historical-data requests/sec across ALL workers")
	retries := flag.Int("retries", 3, "retry a throttled/failed fetch this many times with backoff before skipping")
	flag.Parse()
	if *workers < 1 {
		*workers = 1
	}
	if *rate < 1 {
		*rate = 1
	}
	chunkDays, ok := maxChunkDaysByInterval[*interval]
	if !ok {
		log.Fatalf("--interval %q: unrecognized; must be one of minute, 3minute, 5minute, 10minute, 15minute, 30minute, 60minute, day", *interval)
	}

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
	db.SetMaxOpenConns(*workers + 2)
	db.SetMaxIdleConns(*workers + 2)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}

	candleStore := store.NewIntradayCandleStore(db)
	if err := candleStore.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	to := time.Now()
	var from time.Time
	if *fromStr != "" {
		from, err = time.Parse("2006-01-02", *fromStr)
		if err != nil {
			log.Fatalf("--from: %v", err)
		}
	} else {
		from, err = parsePeriod(*period, to)
		if err != nil {
			log.Fatalf("period: %v", err)
		}
	}

	client := kite.NewClient(cfg.KiteBaseURL, cfg.KiteAPIKey, cfg.KiteAccessToken)
	instruments, err := client.Instruments(ctx, *exchange)
	if err != nil {
		log.Fatalf("kite instruments: %v", err)
	}
	log.Printf("loaded %d %s instruments from Kite", len(instruments), *exchange)

	limiter := time.NewTicker(time.Second / time.Duration(*rate))
	defer limiter.Stop()

	log.Printf("syncing %d symbols at %s interval, %s -> %s, %d workers, %d req/s rate cap",
		len(symbols), *interval, from.Format("2006-01-02"), to.Format("2006-01-02"), *workers, *rate)

	var syncedN, skippedN atomic.Int64
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rawSymbol := range jobs {
				if ctx.Err() != nil {
					skippedN.Add(1)
					continue
				}
				if syncSymbol(ctx, client, candleStore, instruments, *exchange, rawSymbol, *interval, chunkDays, from, to, limiter, *retries) {
					syncedN.Add(1)
				} else {
					skippedN.Add(1)
				}
			}
		}()
	}
	for _, rawSymbol := range symbols {
		jobs <- rawSymbol
	}
	close(jobs)
	wg.Wait()

	fmt.Println()
	fmt.Printf("Symbols:  %d\n", len(symbols))
	fmt.Printf("Interval: %s\n", *interval)
	fmt.Printf("Synced:   %d\n", syncedN.Load())
	fmt.Printf("Skipped:  %d\n", skippedN.Load())
}

// chunkWindows splits [from, to) into sequential windows no wider than
// chunkDays, oldest first.
func chunkWindows(from, to time.Time, chunkDays int) [][2]time.Time {
	var windows [][2]time.Time
	cur := from
	for cur.Before(to) {
		end := cur.AddDate(0, 0, chunkDays)
		if end.After(to) {
			end = to
		}
		windows = append(windows, [2]time.Time{cur, end})
		cur = end
	}
	if len(windows) == 0 {
		windows = append(windows, [2]time.Time{from, to})
	}
	return windows
}

func fetchHistoricalChunked(
	ctx context.Context,
	client *kite.Client,
	token int64,
	symbol, interval string,
	chunkDays int,
	from, to time.Time,
	limiter *time.Ticker,
	retries int,
) ([]models.Candle, error) {
	var all []models.Candle
	for _, w := range chunkWindows(from, to, chunkDays) {
		var candles []models.Candle
		var err error
		for attempt := 0; attempt <= retries; attempt++ {
			select {
			case <-limiter.C:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			candles, err = client.Historical(ctx, token, symbol, interval, w[0], w[1])
			if err == nil {
				break
			}
			if attempt < retries {
				backoff := time.Duration(250*(attempt+1)) * time.Millisecond
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
		if err != nil {
			return nil, fmt.Errorf("window %s to %s: %w", w[0].Format("2006-01-02"), w[1].Format("2006-01-02"), err)
		}
		all = append(all, candles...)
	}
	return all, nil
}

func syncSymbol(
	ctx context.Context,
	client *kite.Client,
	candleStore *store.IntradayCandleStore,
	instruments []kite.Instrument,
	exchange, rawSymbol, interval string,
	chunkDays int,
	from, to time.Time,
	limiter *time.Ticker,
	retries int,
) bool {
	symbol := kite.NormalizeSymbol(rawSymbol)
	inst, ok := kite.FindEquityInstrument(instruments, exchange, symbol)
	if !ok {
		log.Printf("skip %s: no %s equity instrument found", symbol, exchange)
		return false
	}

	candles, err := fetchHistoricalChunked(ctx, client, inst.InstrumentToken, symbol, interval, chunkDays, from, to, limiter, retries)
	if err != nil {
		log.Printf("skip %s: historical fetch failed after %d retries: %v", symbol, retries, err)
		return false
	}
	if len(candles) == 0 {
		log.Printf("skip %s: Kite returned no candles", symbol)
		return false
	}
	if err := candleStore.UpsertCandles(ctx, interval, candles); err != nil {
		log.Printf("skip %s: DB upsert failed: %v", symbol, err)
		return false
	}
	log.Printf("synced %d %s candles for %s", len(candles), interval, symbol)
	return true
}

func parsePeriod(period string, from time.Time) (time.Time, error) {
	if len(period) < 2 {
		return time.Time{}, fmt.Errorf("invalid period %q: must be like 60d, 30d", period)
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
