// Command scan loads OHLCV candles from CSV files or PostgreSQL, runs the
// scanner engine, and prints ranked signal candidates.
//
// Usage:
//
//	go run ./cmd/scan --csv ~/Desktop/ITC.csv --symbol ITC
//	go run ./cmd/scan --csv-dir ~/Desktop/nifty-data --top 10
//	go run ./cmd/scan --db --symbols config/symbols.txt --top 10
//
// Flags:
//
//	--csv       path to one OHLCV CSV file
//	--csv-dir   path to a directory of OHLCV CSV files
//	--db        scan candles from PostgreSQL
//	--symbols   symbol file for --db        (default: config/symbols.txt)
//	--period    DB history window           (default: 2y)
//	--symbol    stock symbol for --csv; or filter --db to one symbol
//	--top       max signals to print        (default: 5)
//	--min-rr    minimum risk/reward         (default: 2.0)
//	--show-filtered
//	            print diagnostics for filtered symbols
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
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/internal/display"
	"github.com/sahiltyagi27/stock-market-analysis/internal/loader"
	"github.com/sahiltyagi27/stock-market-analysis/internal/scanner"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
)

func main() {
	csvPath := flag.String("csv", "", "path to one OHLCV CSV file")
	csvDir := flag.String("csv-dir", "", "path to a directory of OHLCV CSV files")
	dbMode := flag.Bool("db", false, "scan candles from PostgreSQL")
	symbolsFile := flag.String("symbols", "config/symbols.txt", "symbol file for --db")
	period := flag.String("period", "2y", "DB history window (e.g. 2y, 6m, 90d)")
	csvSymbol := flag.String("symbol", "", "stock symbol for --csv; defaults to CSV filename")
	topN := flag.Int("top", 5, "number of top signals to print")
	minRR := flag.Float64("min-rr", 2.0, "minimum risk/reward ratio")
	emaMargin := flag.Float64("ema-margin", 1.0, "minimum %% gap required between price and EMA200 (0 = disabled)")
	minVolume            := flag.Int64("min-volume", 0, "minimum 20-day avg daily volume to qualify (0 = disabled)")
	minResistanceTouches := flag.Int("min-resistance-touches", 2, "minimum touches required for a resistance zone to qualify (1 = allow all)")
	showFiltered         := flag.Bool("show-filtered", false, "print diagnostics for filtered symbols")
	flag.Parse()

	inputs, dataErrs := loadInputs(context.Background(), inputOptions{
		CSVPath:     *csvPath,
		CSVDir:      *csvDir,
		DBMode:      *dbMode,
		SymbolsFile: *symbolsFile,
		Period:      *period,
		Symbol:      *csvSymbol,
	})

	opts := scanner.Options{
		MinRR:        *minRR,
		EMAMarginPct: *emaMargin,
		MinAvgVolume: *minVolume,
		ZoneOpts:     analysis.ZoneOptions{MinResistanceTouches: *minResistanceTouches},
	}
	signals, scanErrs := scanner.ScanWithErrors(inputs, opts)

	printSignals(signals, *topN)
	if *showFiltered {
		printDataErrors(dataErrs)
		printDiagnostics(scanner.Diagnose(inputs, opts), scanErrs)
	}

	fmt.Println()
	fmt.Println(display.Dim.Sprint(strings.Repeat("─", 42)))
	fmt.Printf("%s  %d symbols\n",  display.Dim.Sprint("Scanned: "), len(inputs))
	fmt.Printf("%s  %d %s\n",       display.Dim.Sprint("Skipped: "), len(dataErrs)+len(scanErrs),
		display.Dim.Sprintf("(data errors: %d, no setup: %d)", len(dataErrs), len(scanErrs)))
	fmt.Printf("%s  %s\n", display.Dim.Sprint("Signals: "), display.TotalScore(float64(len(signals))))
}

func printDataErrors(dataErrs map[string]error) {
	if len(dataErrs) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Data Errors")
	keys := make([]string, 0, len(dataErrs))
	for key := range dataErrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("   %s: %v\n", key, dataErrs[key])
	}
}

type inputOptions struct {
	CSVPath     string
	CSVDir      string
	DBMode      bool
	SymbolsFile string
	Period      string
	// Symbol is used as the candle symbol for --csv, and as a single-symbol
	// filter when --db is set (overrides --symbols).
	Symbol string
}

func loadInputs(ctx context.Context, opts inputOptions) ([]scanner.Input, map[string]error) {
	modes := 0
	for _, enabled := range []bool{opts.CSVPath != "", opts.CSVDir != "", opts.DBMode} {
		if enabled {
			modes++
		}
	}
	if modes != 1 {
		log.Fatal("provide exactly one of --csv, --csv-dir, or --db")
	}

	switch {
	case opts.CSVPath != "":
		symbol := normalizeSymbol(opts.Symbol)
		if symbol == "" {
			symbol = symbolFromPath(opts.CSVPath)
		}
		input, err := loadOneCSV(opts.CSVPath, symbol)
		if err != nil {
			log.Fatalf("load csv: %v", err)
		}
		return []scanner.Input{input}, nil
	case opts.DBMode:
		return loadDBInputs(ctx, opts.SymbolsFile, opts.Period, normalizeSymbol(opts.Symbol))
	default:
		return loadCSVDir(opts.CSVDir)
	}
}

func loadCSVDir(csvDir string) ([]scanner.Input, map[string]error) {
	entries, err := os.ReadDir(csvDir)
	if err != nil {
		log.Fatalf("read csv dir: %v", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var inputs []scanner.Input
	dataErrs := make(map[string]error)
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".csv") {
			continue
		}
		path := filepath.Join(csvDir, entry.Name())
		input, err := loadOneCSV(path, symbolFromPath(path))
		if err != nil {
			dataErrs[path] = err
			continue
		}
		inputs = append(inputs, input)
	}
	if len(inputs) == 0 && len(dataErrs) == 0 {
		log.Fatalf("no CSV files found in %s", csvDir)
	}
	return inputs, dataErrs
}

func loadDBInputs(ctx context.Context, symbolsFile, period, singleSymbol string) ([]scanner.Input, map[string]error) {
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

	var symbols []string
	if singleSymbol != "" {
		// --symbol overrides --symbols: query only that one stock.
		symbols = []string{singleSymbol}
	} else {
		symbols, err = config.LoadSymbols(symbolsFile)
		if err != nil {
			log.Fatalf("symbols: %v", err)
		}
	}
	from, err := parsePeriod(period, time.Now())
	if err != nil {
		log.Fatalf("period: %v", err)
	}

	candleStore := store.NewCandleStore(db)
	var inputs []scanner.Input
	dataErrs := make(map[string]error)
	for _, rawSymbol := range symbols {
		symbol := normalizeSymbol(rawSymbol)
		candles, err := candleStore.GetCandles(ctx, symbol, store.CandleFilter{From: &from})
		if err != nil {
			dataErrs[symbol] = err
			continue
		}
		if len(candles) == 0 {
			dataErrs[symbol] = fmt.Errorf("no candles in DB")
			continue
		}
		inputs = append(inputs, scanner.Input{Symbol: symbol, Candles: candles})
	}
	return inputs, dataErrs
}

func loadOneCSV(path, symbol string) (scanner.Input, error) {
	candles, err := loader.LoadCSV(path, symbol)
	if err != nil {
		return scanner.Input{}, err
	}
	return scanner.Input{Symbol: symbol, Candles: candles}, nil
}

func symbolFromPath(path string) string {
	base := filepath.Base(path)
	symbol := strings.TrimSuffix(base, filepath.Ext(base))
	return normalizeSymbol(symbol)
}

func normalizeSymbol(symbol string) string {
	symbol = strings.TrimSpace(symbol)
	if _, after, ok := strings.Cut(symbol, ":"); ok {
		symbol = after
	}
	if before, _, ok := strings.Cut(symbol, "."); ok {
		symbol = before
	}
	return strings.ToUpper(strings.TrimSpace(symbol))
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

func printDiagnostics(diags []scanner.Diagnostic, scanErrs map[string]error) {
	if len(scanErrs) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Filtered Symbols")
	for _, d := range diags {
		if _, ok := scanErrs[d.Symbol]; !ok {
			continue
		}
		fmt.Printf("\n%s\n", d.Symbol)
		fmt.Printf("   Price:   %.2f\n", d.Price)
		fmt.Printf("   Trend:   %s\n", d.Trend)
		fmt.Printf("   EMA10:   %.2f\n", d.EMA.EMA10)
		fmt.Printf("   EMA50:   %.2f\n", d.EMA.EMA50)
		fmt.Printf("   EMA200:  %.2f\n", d.EMA.EMA200)
		fmt.Printf("   Reason:  %s\n", d.Error)
	}
}

func printSignals(signals []scanner.StockSignal, topN int) {
	fmt.Println()
	box := display.BoldCyan.Sprint
	fmt.Println(box("╔══════════════════════════════════════╗"))
	fmt.Println(box("║") + display.Bold.Sprint("      Top Watchlist Candidates        ") + box("║"))
	fmt.Println(box("║") + display.Dim.Sprint("  (research only — not buy signals)   ") + box("║"))
	fmt.Println(box("╚══════════════════════════════════════╝"))

	top := topN
	if top > len(signals) {
		top = len(signals)
	}

	if top == 0 {
		fmt.Println("\nNo bullish setups found matching the criteria.")
	}

	for i, sig := range signals[:top] {
		fmt.Printf("\n%s %s\n",
			display.Dim.Sprintf("%d.", i+1),
			display.BoldWhite.Sprint(sig.Symbol))

		// Total score.
		fmt.Printf("   %s  %s %s\n",
			display.Dim.Sprint("Score:     "),
			display.TotalScore(sig.Score),
			display.Dim.Sprint("/ 100"))

		// Component breakdown.
		fmt.Printf("     %s  %s\n", display.Dim.Sprint("Trend:  "), display.ComponentF(sig.Breakdown.Trend, 40))
		fmt.Printf("     %s  %s\n", display.Dim.Sprint("R/R:    "), display.ComponentF(sig.Breakdown.RR, 30))
		fmt.Printf("     %s  %s\n", display.Dim.Sprint("Support:"), display.ComponentF(sig.Breakdown.Support, 20))
		if sig.Breakdown.AvgVolume > 0 {
			fmt.Printf("     %s  %s  %s\n",
				display.Dim.Sprint("Volume: "),
				display.ComponentF(sig.Breakdown.Volume, 10),
				display.Dim.Sprintf("(latest %.0f, avg20 %.0f, %.2fx)",
					sig.Breakdown.LastVolume, sig.Breakdown.AvgVolume, sig.Breakdown.VolumeRatio))
		} else {
			fmt.Printf("     %s  %s\n",
				display.Dim.Sprint("Volume: "),
				display.ComponentF(sig.Breakdown.Volume, 10))
		}

		// Price + trend.
		fmt.Printf("   %s  %.2f\n", display.Dim.Sprint("Price:     "), sig.Price)
		fmt.Printf("   %s  %s\n",   display.Dim.Sprint("Trend:     "), display.Trend(string(sig.Trend)))

		// R/R.
		fmt.Printf("   %s  %s  %s\n",
			display.Dim.Sprint("R/R:       "),
			display.RR(sig.Trade.RiskReward),
			display.Dim.Sprint("(")+display.Quality(string(sig.Trade.Quality))+display.Dim.Sprint(")"))

		// Entry / SL / Target.
		fmt.Printf("   %s  %.2f   %s %s   %s %s\n",
			display.Dim.Sprint("Entry:     "), sig.Trade.Entry,
			display.Dim.Sprint("SL:"), display.Red.Sprintf("%.2f", sig.Trade.StopLoss),
			display.Dim.Sprint("Target:"), display.Green.Sprintf("%.2f", sig.Trade.Target))

		// Zones.
		fmt.Printf("   %s  %.2f – %.2f  %s\n",
			display.Dim.Sprint("Support:   "),
			sig.Support.Low, sig.Support.High,
			display.Dim.Sprintf("(%d touches)", sig.Support.Touches))
		fmt.Printf("   %s  %.2f – %.2f  %s\n",
			display.Dim.Sprint("Resistance:"),
			sig.Resistance.Low, sig.Resistance.High,
			display.Dim.Sprintf("(%d touches)", sig.Resistance.Touches))

		// Reasons.
		fmt.Printf("   %s\n", display.Dim.Sprint("Reasons:"))
		for _, r := range sig.Reasons {
			fmt.Printf("     %s %s\n", display.Cyan.Sprint("•"), display.Dim.Sprint(r))
		}
	}
}
