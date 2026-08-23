// Command kite-sync downloads daily candles from Kite Connect and stores them
// in PostgreSQL for scanner use.
//
// Usage:
//
//	go run ./cmd/kite-sync --symbols config/symbols.txt --period 2y
//	go run ./cmd/kite-sync --workers 10 --rate 3   # parallel, rate-limited
//
// Symbols are synced by a pool of --workers goroutines (default 10) that overlap
// DB writes with network fetches. A shared --rate limiter (default 6 req/s) keeps
// the Kite historical-data API from throttling — unpaced concurrency just trips
// 429s and drops symbols. Throttled fetches are retried (--retries) with backoff.
// (If every symbol fails with a 403 TokenException, refresh the access token via
// cmd/kite-token — Kite tokens expire daily.)
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
	"strings"
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

func main() {
	symbolsFile := flag.String("symbols", "config/symbols.txt", "path to watchlist file")
	exchange := flag.String("exchange", "NSE", "Kite exchange")
	period := flag.String("period", "5y", "history window (e.g. 5y, 2y, 6m, 90d), relative to now. Overridden by --from. Automatically chunked into <=1900-day requests to respect Kite's ~2000-day-per-call limit.")
	fromStr := flag.String("from", "", "absolute start date YYYY-MM-DD (overrides --period). Kite's historical API generally has data for liquid NSE equities back to 2008-2010+; auto-chunked into <=1900-day requests either way.")
	includeNifty := flag.Bool("include-nifty", true, "also sync NIFTY 50 index candles as NIFTY50 for relative-strength filters")
	includeSectorIndices := flag.Bool("include-sector-indices", true, "also sync verified NSE sector index candles for sector-strength filters")
	sectorIndicesFlag := flag.String("sector-indices", "", "comma-separated sector index names to sync (empty = default verified list)")
	workers := flag.Int("workers", 10, "parallel workers for the per-symbol sync")
	rate := flag.Int("rate", 6, "max Kite historical-data requests/sec across ALL workers (the prior sequential sync sustained ~4/s; lower this if you see 'fetch failed' / 429s, raise it if your account tolerates more)")
	retries := flag.Int("retries", 3, "retry a throttled/failed fetch this many times with backoff before skipping")
	flag.Parse()
	if *workers < 1 {
		*workers = 1
	}
	if *rate < 1 {
		*rate = 1
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
	// Size the pool to the worker count so concurrent upserts don't queue on
	// connections (+2 headroom for the index sync / migrate).
	db.SetMaxOpenConns(*workers + 2)
	db.SetMaxIdleConns(*workers + 2)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}

	candleStore := store.NewCandleStore(db)
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

	// A SHARED rate limiter paces every Kite fetch — the concurrent symbol
	// workers below and the sequential NIFTY/sector-index/chunked fetches
	// above all draw from it. Kite throttles historical data (~3/s); unpaced
	// concurrency just trips 429s (slower + dropped symbols).
	limiter := time.NewTicker(time.Second / time.Duration(*rate))
	defer limiter.Stop()

	var synced, skipped int
	var indexSynced, indexSkipped int
	if *includeNifty && strings.EqualFold(*exchange, "NSE") {
		candles, err := fetchHistoricalChunked(ctx, client, kite.Nifty50InstrumentToken, kite.Nifty50Symbol, from, to, limiter, *retries)
		if err != nil {
			log.Printf("skip %s: historical fetch failed: %v", kite.Nifty50Symbol, err)
			indexSkipped++
		} else if len(candles) == 0 {
			log.Printf("skip %s: Kite returned no candles", kite.Nifty50Symbol)
			indexSkipped++
		} else if err := candleStore.UpsertCandles(ctx, candles); err != nil {
			log.Printf("skip %s: DB upsert failed: %v", kite.Nifty50Symbol, err)
			indexSkipped++
		} else {
			log.Printf("synced %d daily candles for %s", len(candles), kite.Nifty50Symbol)
			indexSynced++
		}
	}
	if *includeSectorIndices && strings.EqualFold(*exchange, "NSE") {
		s, k := syncSectorIndices(ctx, client, candleStore, instruments, *exchange, parseSectorIndices(*sectorIndicesFlag), from, to, limiter, *retries)
		indexSynced += s
		indexSkipped += k
	}

	// Sync symbols concurrently with a fixed worker pool. Each symbol is an
	// independent fetch + upsert, so workers overlap DB writes with network
	// waits, sharing the same rate limiter set up above.
	log.Printf("syncing %d symbols with %d workers, %d req/s rate cap, %d retries",
		len(symbols), *workers, *rate, *retries)

	var syncedN, skippedN atomic.Int64
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rawSymbol := range jobs {
				if ctx.Err() != nil { // interrupted — drain remaining jobs as skipped
					skippedN.Add(1)
					continue
				}
				if syncSymbol(ctx, client, candleStore, instruments, *exchange, rawSymbol, from, to, limiter, *retries) {
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
	synced += int(syncedN.Load())
	skipped += int(skippedN.Load())

	fmt.Println()
	fmt.Printf("Symbols: %d\n", len(symbols))
	fmt.Printf("Synced:  %d\n", synced)
	fmt.Printf("Skipped: %d\n", skipped)
	fmt.Printf("Indices synced:  %d\n", indexSynced)
	fmt.Printf("Indices skipped: %d\n", indexSkipped)
}

// maxChunkDays is the widest single from/to span requested per Kite call —
// safely under Kite's ~2000-day daily-candle limit. A [from, to) range wider
// than this is automatically split into sequential chunked requests.
const maxChunkDays = 1900

// chunkWindows splits [from, to) into sequential windows no wider than
// maxChunkDays, oldest first. A single window is returned unchanged when the
// whole range already fits.
func chunkWindows(from, to time.Time) [][2]time.Time {
	var windows [][2]time.Time
	cur := from
	for cur.Before(to) {
		end := cur.AddDate(0, 0, maxChunkDays)
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

// fetchHistoricalChunked fetches [from, to) for one instrument, splitting into
// <=maxChunkDays windows when the range is wider (Kite's per-call limit) and
// concatenating the results, oldest first. Each window's fetch waits on the
// shared rate limiter and is retried up to `retries` times with backoff.
func fetchHistoricalChunked(
	ctx context.Context,
	client *kite.Client,
	token int64,
	symbol string,
	from, to time.Time,
	limiter *time.Ticker,
	retries int,
) ([]models.Candle, error) {
	var all []models.Candle
	for _, w := range chunkWindows(from, to) {
		var candles []models.Candle
		var err error
		for attempt := 0; attempt <= retries; attempt++ {
			// Pace this fetch against Kite's limit (shared across all workers).
			select {
			case <-limiter.C:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			candles, err = client.HistoricalDaily(ctx, token, symbol, w[0], w[1])
			if err == nil {
				break
			}
			if attempt < retries {
				// Back off before retrying (likely a 429 throttle): 250ms, 500ms, …
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

// syncSymbol fetches one symbol's daily history (auto-chunked across
// maxChunkDays windows when the range is wide) and upserts it. Safe for
// concurrent use: the Kite client (HTTP) and candle store (DB pool) are both
// goroutine-safe. Returns true when candles were synced, false on any skip.
func syncSymbol(
	ctx context.Context,
	client *kite.Client,
	candleStore *store.CandleStore,
	instruments []kite.Instrument,
	exchange, rawSymbol string,
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

	candles, err := fetchHistoricalChunked(ctx, client, inst.InstrumentToken, symbol, from, to, limiter, retries)
	if err != nil {
		log.Printf("skip %s: historical fetch failed after %d retries: %v", symbol, retries, err)
		return false
	}
	if len(candles) == 0 {
		log.Printf("skip %s: Kite returned no candles", symbol)
		return false
	}
	if err := candleStore.UpsertCandles(ctx, candles); err != nil {
		log.Printf("skip %s: DB upsert failed: %v", symbol, err)
		return false
	}
	log.Printf("synced %d daily candles for %s", len(candles), symbol)
	return true
}

func syncSectorIndices(
	ctx context.Context,
	client *kite.Client,
	candleStore *store.CandleStore,
	instruments []kite.Instrument,
	exchange string,
	indexNames []string,
	from time.Time,
	to time.Time,
	limiter *time.Ticker,
	retries int,
) (synced, skipped int) {
	for _, indexName := range indexNames {
		inst, ok := kite.FindInstrumentByName(instruments, exchange, indexName)
		dbSymbol := kite.IndexDBSymbol(indexName)
		if !ok {
			log.Printf("skip %s: no %s index instrument found", indexName, exchange)
			skipped++
			continue
		}

		candles, err := fetchHistoricalChunked(ctx, client, inst.InstrumentToken, dbSymbol, from, to, limiter, retries)
		if err != nil {
			log.Printf("skip %s: historical fetch failed: %v", dbSymbol, err)
			skipped++
			continue
		}
		if len(candles) == 0 {
			log.Printf("skip %s: Kite returned no candles", dbSymbol)
			skipped++
			continue
		}
		if err := candleStore.UpsertCandles(ctx, candles); err != nil {
			log.Printf("skip %s: DB upsert failed: %v", dbSymbol, err)
			skipped++
			continue
		}
		log.Printf("synced %d daily candles for %s (%s)", len(candles), dbSymbol, indexName)
		synced++
	}
	return synced, skipped
}

func parseSectorIndices(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return append([]string(nil), kite.SectorIndexNames...)
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		name := strings.Join(strings.Fields(strings.ToUpper(strings.TrimSpace(part))), " ")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
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
