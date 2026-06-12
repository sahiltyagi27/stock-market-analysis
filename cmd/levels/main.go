// Command levels prints the support and resistance levels for a single stock
// from its PostgreSQL candle history.
//
// Usage:
//
//	go run ./cmd/levels --symbol RELIANCE
//	go run ./cmd/levels --symbol TATAPOWER --min-touches 3 --cluster 0.015
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"sort"

	_ "github.com/lib/pq"
	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/internal/display"
	"github.com/sahiltyagi27/stock-market-analysis/internal/kite"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
)

func main() {
	symbol := flag.String("symbol", "", "stock symbol, e.g. RELIANCE (required)")
	window := flag.Int("window", 2, "candles each side to confirm a local extreme")
	cluster := flag.Float64("cluster", 0.02, "merge extremes within this fraction of price into one zone (0.02 = 2%)")
	minTouches := flag.Int("min-touches", 1, "minimum touches for a resistance zone (1 = show all)")
	limit := flag.Int("limit", 0, "use only the most recent N candles (0 = all history)")
	flag.Parse()

	if *symbol == "" {
		log.Fatal("--symbol is required (e.g. --symbol RELIANCE)")
	}
	sym := kite.NormalizeSymbol(*symbol)

	ctx := context.Background()
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

	cc, err := store.NewCandleStore(db).GetCandles(ctx, sym, store.CandleFilter{})
	if err != nil {
		log.Fatalf("load candles for %s: %v", sym, err)
	}
	if len(cc) == 0 {
		log.Fatalf("no candles in DB for %q — check the symbol or run kite-sync", sym)
	}
	if *limit > 0 && len(cc) > *limit {
		cc = cc[len(cc)-*limit:]
	}

	highs := make([]float64, len(cc))
	lows := make([]float64, len(cc))
	for i, c := range cc {
		highs[i] = c.High
		lows[i] = c.Low
	}
	price := cc[len(cc)-1].Close
	asOf := cc[len(cc)-1].Timestamp.Format("02-Jan-2006")

	zones := analysis.FindZones(highs, lows, analysis.ZoneOptions{
		Window:               *window,
		ClusterPct:           *cluster,
		MinResistanceTouches: *minTouches,
	})

	banner := fmt.Sprintf("━━━  %s  —  Support / Resistance  ━━━", sym)
	fmt.Printf("\n%s\n", display.BoldCyan.Sprint(banner))
	fmt.Printf("  %s %s   %s %s   %s %d candles\n\n",
		display.Dim.Sprint("Price:"), display.BoldWhite.Sprintf("%.2f", price),
		display.Dim.Sprint("as of"), display.Dim.Sprint(asOf),
		display.Dim.Sprint("over"), len(cc))

	// Resistance ABOVE price (nearest first), then support BELOW price (nearest first).
	res := above(zones.Resistance, price)
	sup := below(zones.Support, price)

	fmt.Printf("  %s\n", display.Red.Sprint("Resistance (above price):"))
	if len(res) == 0 {
		fmt.Printf("     %s\n", display.Dim.Sprint("none found above current price"))
	}
	for i, z := range res {
		tag := ""
		if i == 0 {
			tag = display.Dim.Sprint("  ← nearest")
		}
		fmt.Printf("     %s  %s  %s%s\n",
			display.Red.Sprintf("%-9.2f", z.Mid),
			display.Dim.Sprintf("[%.2f–%.2f]", z.Low, z.High),
			display.Dim.Sprintf("%d touch", z.Touches), tag)
	}

	fmt.Printf("\n  %s\n", display.Green.Sprint("Support (below price):"))
	if len(sup) == 0 {
		fmt.Printf("     %s\n", display.Dim.Sprint("none found below current price"))
	}
	for i, z := range sup {
		tag := ""
		if i == 0 {
			tag = display.Dim.Sprint("  ← nearest")
		}
		fmt.Printf("     %s  %s  %s%s\n",
			display.Green.Sprintf("%-9.2f", z.Mid),
			display.Dim.Sprintf("[%.2f–%.2f]", z.Low, z.High),
			display.Dim.Sprintf("%d touch", z.Touches), tag)
	}
	fmt.Println()
}

// above returns zones whose midpoint sits above price, nearest first.
func above(zs []analysis.Zone, price float64) []analysis.Zone {
	var out []analysis.Zone
	for _, z := range zs {
		if z.Mid > price {
			out = append(out, z)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mid < out[j].Mid })
	return out
}

// below returns zones whose midpoint sits below price, nearest first.
func below(zs []analysis.Zone, price float64) []analysis.Zone {
	var out []analysis.Zone
	for _, z := range zs {
		if z.Mid < price {
			out = append(out, z)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mid > out[j].Mid })
	return out
}
