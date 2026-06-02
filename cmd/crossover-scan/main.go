// Command crossover-scan finds stocks where the 7-period EMA has recently
// crossed above the 21-period EMA — a pure momentum signal.
//
// Unlike the swing scanner, no EMA50/EMA200 long-term trend check is applied.
// The entry is at the current close, the stop-loss is the Low of the candle
// immediately before the crossover, and the target is the nearest resistance
// zone above price.
//
// Usage:
//
//	go run ./cmd/crossover-scan
//	go run ./cmd/crossover-scan --max-age 2 --min-rr 1.5 --top 10
//
// Required environment (loaded from .env):
//
//	DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME
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
	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/internal/crossover"
	"github.com/sahiltyagi27/stock-market-analysis/internal/display"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
)

func main() {
	symbolsFile := flag.String("symbols", "config/symbols.txt", "path to watchlist file")
	period      := flag.String("period", "1y", "historical candle window (e.g. 1y, 6m, 90d)")
	topN        := flag.Int("top", 10, "signals to print")
	maxAge      := flag.Int("max-age", 2, "maximum candles since crossover (0=today only, 2=last 3 candles)")
	minRR       := flag.Float64("min-rr", 1.5, "minimum risk/reward ratio")
	minCandles  := flag.Int("min-candles", 50, "minimum candles required before analysis")
	volWindow   := flag.Int("vol-window", 20, "candles used for volume rolling average")
	minResTouches := flag.Int("min-resistance-touches", 1, "minimum touches for a resistance zone to qualify as target")
	minVolMult    := flag.Float64("min-vol-mult", 0, "require today's volume ≥ this × the rolling avg of the previous --vol-mult-window candles (0 = disabled)")
	volMultWindow := flag.Int("vol-mult-window", 10, "candles used for the today's-volume average check")
	minTargetPct  := flag.Float64("min-target-pct", 4.0, "minimum %% distance the resistance target must sit above entry (<0 disables)")
	minRiskPct    := flag.Float64("min-risk-pct", 3.0, "minimum SL distance as %% of price; widens a too-tight previous-candle-low stop (<0 disables)")
	showFiltered  := flag.Bool("show-filtered", false, "print rejection reasons for every filtered symbol")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── Config + DB ───────────────────────────────────────────────────────────
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

	// ── Symbols ───────────────────────────────────────────────────────────────
	symbols, err := config.LoadSymbols(*symbolsFile)
	if err != nil {
		log.Fatalf("symbols: %v", err)
	}
	log.Printf("loaded %d symbols from %s", len(symbols), *symbolsFile)

	// ── Candles ───────────────────────────────────────────────────────────────
	from, err := parsePeriod(*period, time.Now())
	if err != nil {
		log.Fatalf("period: %v", err)
	}
	candleStore := store.NewCandleStore(db)
	log.Printf("loading candles from DB (window: %s)…", *period)

	var inputs []crossover.Input
	for _, sym := range symbols {
		cc, err := candleStore.GetCandles(ctx, sym, store.CandleFilter{From: &from})
		if err != nil {
			log.Printf("  warn: %s: %v", sym, err)
			continue
		}
		if len(cc) > 0 {
			inputs = append(inputs, crossover.Input{Symbol: sym, Candles: cc})
		}
	}
	log.Printf("loaded candles for %d/%d symbols", len(inputs), len(symbols))

	// ── Scan ──────────────────────────────────────────────────────────────────
	opts := crossover.Options{
		MaxCrossoverAge:       *maxAge,
		MinRR:                 *minRR,
		VolumeWindow:          *volWindow,
		MinCandles:            *minCandles,
		MinCurrentVolMultiple: *minVolMult,
		CurrentVolWindow:      *volMultWindow,
		MinTargetPct:          *minTargetPct,
		MinRiskPct:            *minRiskPct,
		ZoneOpts:              analysis.ZoneOptions{MinResistanceTouches: *minResTouches},
	}

	signals, errs := crossover.Scan(inputs, opts)
	log.Printf("scan complete — %d signals from %d symbols", len(signals), len(inputs))

	// ── Print ──────────────────────────────────────────────────────────────────
	printSignals(signals, *topN)

	if *showFiltered {
		printFiltered(errs)
	}
}

// ── Terminal display ──────────────────────────────────────────────────────────

func printSignals(signals []crossover.Signal, topN int) {
	stamp := time.Now().Format("02-Jan-2006  15:04:05")
	banner := fmt.Sprintf("━━━  EMA 7×21 Crossover Scan  %s  ━━━", stamp)
	fmt.Printf("\n%s\n", display.BoldCyan.Sprint(banner))

	top := topN
	if top > len(signals) {
		top = len(signals)
	}
	if top == 0 {
		fmt.Println("  No crossover signals found.")
	}

	pipe := display.Dim.Sprint("├")
	last := display.Dim.Sprint("└")

	for i, sig := range signals[:top] {
		ageLabel := ageStr(sig.CrossoverAge)
		sym := display.BoldWhite.Sprint(fmt.Sprintf("%-14s", sig.Symbol))
		price := fmt.Sprintf("₹%-10.2f", sig.Price)
		score := display.TotalScore(sig.Score)

		fmt.Printf("\n  %s %s %s %s%s/100\n",
			display.Dim.Sprintf("%d.", i+1),
			sym, price,
			display.Dim.Sprint("Score: "),
			score)

		// EMA line.
		ema7Str := display.Green.Sprintf("%.2f", sig.EMA7)
		ema21Str := display.Yellow.Sprintf("%.2f", sig.EMA21)
		gapPct := 0.0
		if sig.Price > 0 {
			gapPct = (sig.EMA7 - sig.EMA21) / sig.Price * 100
		}
		fmt.Printf("     %s %s %s  %s %s  %s  %s\n",
			pipe,
			display.Dim.Sprint("EMA7:"), ema7Str,
			display.Dim.Sprint("EMA21:"), ema21Str,
			display.Green.Sprintf("(+%.2f%%)", gapPct),
			display.Cyan.Sprintf("crossed %s", ageLabel))

		// Entry / SL / Target.
		atrLabel := ""
		fmt.Printf("     %s %s %-10.2f  %s %s  %s %s%s\n",
			pipe,
			display.Dim.Sprint("Entry:"), sig.Entry,
			display.Dim.Sprint("SL:"), display.Red.Sprintf("%-10.2f", sig.SL),
			display.Dim.Sprint("Target:"), targetStr(sig),
			atrLabel)

		// R/R and volume.
		rrStr := rrLabel(sig.RiskReward)
		if sig.Volume.AvgVolume > 0 {
			fmt.Printf("     %s %s %s  %s %.2fx avg (%.0f vs %.0f)\n",
				pipe,
				display.Dim.Sprint("R/R:"), rrStr,
				display.Dim.Sprint("Volume:"),
				sig.Volume.Ratio,
				sig.Volume.CrossVolume, sig.Volume.AvgVolume)
		} else {
			fmt.Printf("     %s %s %s\n",
				pipe,
				display.Dim.Sprint("R/R:"), rrStr)
		}

		// Reasons.
		fmt.Printf("     %s %s\n", last, display.Dim.Sprint("Reasons:"))
		for _, r := range sig.Reasons {
			fmt.Printf("         %s %s\n",
				display.Cyan.Sprint("•"), display.Dim.Sprint(r))
		}
	}

	sep := display.Dim.Sprint(strings.Repeat("─", 54))
	fmt.Printf("\n  %s\n  %s\n%s\n",
		sep,
		display.Dim.Sprintf("Scanned: %d  Signals: %d  (max-age: %d candles)",
			-1, len(signals), -1), // placeholders; caller has the totals
		display.BoldCyan.Sprint(strings.Repeat("━", len(banner))))
}

func printFiltered(errs map[string]error) {
	if len(errs) == 0 {
		return
	}
	fmt.Printf("\n%s\n", display.Dim.Sprint("── Filtered ─────────────────────────────────"))
	for sym, err := range errs {
		fmt.Printf("  %s  %s\n",
			display.Dim.Sprint(fmt.Sprintf("%-16s", sym)),
			display.Dim.Sprint(err.Error()))
	}
}

func ageStr(age int) string {
	switch age {
	case 0:
		return display.BoldGreen.Sprint("today")
	case 1:
		return display.Green.Sprint("1 candle ago")
	default:
		return display.Yellow.Sprintf("%d candles ago", age)
	}
}

func targetStr(sig crossover.Signal) string {
	if sig.Target == 0 {
		return display.Dim.Sprint("—")
	}
	return display.Green.Sprintf("%.2f", sig.Target)
}

func rrLabel(rr float64) string {
	if rr <= 0 {
		return display.Dim.Sprint("—")
	}
	return display.RR(rr)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func parsePeriod(period string, from time.Time) (time.Time, error) {
	if len(period) < 2 {
		return time.Time{}, fmt.Errorf("invalid period %q: use 2y, 6m, 90d", period)
	}
	unit := period[len(period)-1]
	var n int
	if _, err := fmt.Sscanf(period[:len(period)-1], "%d", &n); err != nil || n <= 0 {
		return time.Time{}, fmt.Errorf("invalid period %q", period)
	}
	switch unit {
	case 'y', 'Y':
		return from.AddDate(-n, 0, 0), nil
	case 'm', 'M':
		return from.AddDate(0, -n, 0), nil
	case 'd', 'D':
		return from.AddDate(0, 0, -n), nil
	default:
		return time.Time{}, fmt.Errorf("unknown period unit %q", string(unit))
	}
}

