// Command sector-index-discovery checks whether Kite exposes NSE sector index
// instruments and daily historical candles for them.
//
// It does not write to PostgreSQL. Use it before wiring sector-strength filters
// into the scanner so we know which sector indices are actually usable.
//
// Usage:
//
//	go run ./cmd/sector-index-discovery
//	go run ./cmd/sector-index-discovery --period 30d
//	go run ./cmd/sector-index-discovery --indices "NIFTY BANK,NIFTY IT"
//
// Required environment variables:
//
//	KITE_API_KEY
//	KITE_ACCESS_TOKEN
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/kite"
)

var defaultIndices = []string{
	"NIFTY 50",
	"NIFTY BANK",
	"NIFTY AUTO",
	"NIFTY FIN SERVICE",
	"NIFTY FMCG",
	"NIFTY IT",
	"NIFTY MEDIA",
	"NIFTY METAL",
	"NIFTY PHARMA",
	"NIFTY PSU BANK",
	"NIFTY PVT BANK",
	"NIFTY REALTY",
	"NIFTY ENERGY",
	"NIFTY INFRA",
	"NIFTY OIL AND GAS",
	"NIFTY HEALTHCARE",
	"NIFTY CONSR DURBL",
}

type result struct {
	Name         string
	Found        bool
	HistoricalOK bool
	Instrument   kite.Instrument
	CandleCount  int
	FirstCandle  time.Time
	LastCandle   time.Time
	LastClose    float64
	Error        string
}

func main() {
	exchange := flag.String("exchange", "NSE", "Kite exchange")
	period := flag.String("period", "90d", "historical probe window (e.g. 90d, 6m, 1y)")
	indicesFlag := flag.String("indices", "", "comma-separated index names to probe (empty = default NSE sector list)")
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
	log.Printf("loaded %d %s instruments from Kite", len(instruments), strings.ToUpper(*exchange))

	indexNames := parseIndices(*indicesFlag)
	results := make([]result, 0, len(indexNames))
	for _, name := range indexNames {
		results = append(results, probeIndex(ctx, client, instruments, strings.ToUpper(*exchange), name, from, to))
	}

	sort.SliceStable(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	printResults(results, *period)
}

func probeIndex(
	ctx context.Context,
	client *kite.Client,
	instruments []kite.Instrument,
	exchange string,
	name string,
	from time.Time,
	to time.Time,
) result {
	r := result{Name: name}
	inst, ok := kite.FindInstrumentByName(instruments, exchange, name)
	if !ok {
		r.Error = "instrument not found"
		return r
	}
	r.Found = true
	r.Instrument = inst

	candles, err := client.HistoricalDaily(ctx, inst.InstrumentToken, inst.TradingSymbol, from, to)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if len(candles) == 0 {
		r.Error = "historical API returned no candles"
		return r
	}

	r.HistoricalOK = true
	r.CandleCount = len(candles)
	r.FirstCandle = candles[0].Timestamp
	r.LastCandle = candles[len(candles)-1].Timestamp
	r.LastClose = candles[len(candles)-1].Close
	return r
}

func parseIndices(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return append([]string(nil), defaultIndices...)
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

func printResults(results []result, period string) {
	fmt.Printf("\nSector Index Discovery (%s)\n", period)
	fmt.Println(strings.Repeat("-", 118))
	fmt.Printf("%-28s %-8s %-11s %-12s %-16s %-16s %-12s %s\n",
		"Index", "Token", "Type", "Segment", "Candles", "Last date", "Last close", "Status")
	fmt.Println(strings.Repeat("-", 118))

	var found, ok int
	for _, r := range results {
		token := "-"
		typ := "-"
		segment := "-"
		candles := "-"
		lastDate := "-"
		lastClose := "-"
		status := "missing: " + r.Error
		if r.Found {
			found++
			token = fmt.Sprintf("%d", r.Instrument.InstrumentToken)
			typ = r.Instrument.InstrumentType
			segment = r.Instrument.Segment
			status = "historical failed: " + r.Error
		}
		if r.HistoricalOK {
			ok++
			candles = fmt.Sprintf("%d", r.CandleCount)
			lastDate = r.LastCandle.Format("2006-01-02")
			lastClose = fmt.Sprintf("%.2f", r.LastClose)
			status = "OK"
		}
		fmt.Printf("%-28s %-8s %-11s %-12s %-16s %-16s %-12s %s\n",
			r.Name, token, typ, segment, candles, lastDate, lastClose, status)
	}

	fmt.Println(strings.Repeat("-", 118))
	fmt.Printf("Found instruments: %d/%d\n", found, len(results))
	fmt.Printf("Historical OK:     %d/%d\n\n", ok, len(results))
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
