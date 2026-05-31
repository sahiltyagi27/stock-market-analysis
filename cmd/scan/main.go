// Command scan loads manually downloaded OHLCV CSV files, runs the scanner
// engine, and prints ranked signal candidates.
//
// Usage:
//
//	go run ./cmd/scan --csv ~/Desktop/ITC.csv --symbol ITC
//	go run ./cmd/scan --csv-dir ~/Desktop/nifty-data --top 10
//
// Flags:
//
//	--csv       path to one OHLCV CSV file
//	--csv-dir   path to a directory of OHLCV CSV files
//	--symbol    stock symbol for --csv; defaults to CSV filename
//	--top       max signals to print     (default: 5)
//	--min-rr    minimum risk/reward      (default: 2.0)
//	--show-filtered
//	            print diagnostics for filtered symbols
//
// Note: output is for watchlist research purposes only, not buy recommendations.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sahiltyagi27/stock-market-analysis/internal/loader"
	"github.com/sahiltyagi27/stock-market-analysis/internal/scanner"
)

func main() {
	csvPath := flag.String("csv", "", "path to one OHLCV CSV file")
	csvDir := flag.String("csv-dir", "", "path to a directory of OHLCV CSV files")
	csvSymbol := flag.String("symbol", "", "stock symbol for --csv; defaults to CSV filename")
	topN := flag.Int("top", 5, "number of top signals to print")
	minRR := flag.Float64("min-rr", 2.0, "minimum risk/reward ratio")
	showFiltered := flag.Bool("show-filtered", false, "print diagnostics for filtered symbols")
	flag.Parse()

	inputs, dataErrs := loadInputs(*csvPath, *csvDir, *csvSymbol)

	opts := scanner.Options{MinRR: *minRR}
	signals, scanErrs := scanner.ScanWithErrors(inputs, opts)

	for path, err := range dataErrs {
		log.Printf("  skip %s: %v", path, err)
	}
	for sym, err := range scanErrs {
		log.Printf("  filter %s: %v", sym, err)
	}

	printSignals(signals, *topN)
	if *showFiltered {
		printDiagnostics(scanner.Diagnose(inputs, opts), scanErrs)
	}

	fmt.Println()
	fmt.Println(strings.Repeat("─", 42))
	fmt.Printf("Scanned:  %d symbols\n", len(inputs))
	fmt.Printf("Skipped:  %d (data errors: %d, no setup: %d)\n",
		len(dataErrs)+len(scanErrs), len(dataErrs), len(scanErrs))
	fmt.Printf("Signals:  %d\n", len(signals))
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

func loadInputs(csvPath, csvDir, csvSymbol string) ([]scanner.Input, map[string]error) {
	if (csvPath == "") == (csvDir == "") {
		log.Fatal("provide exactly one of --csv or --csv-dir")
	}

	if csvPath != "" {
		symbol := normalizeSymbol(csvSymbol)
		if symbol == "" {
			symbol = symbolFromPath(csvPath)
		}
		input, err := loadOneCSV(csvPath, symbol)
		if err != nil {
			log.Fatalf("load csv: %v", err)
		}
		return []scanner.Input{input}, nil
	}

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

func loadOneCSV(path, symbol string) (scanner.Input, error) {
	candles, err := loader.LoadCSV(path, symbol)
	if err != nil {
		return scanner.Input{}, err
	}
	log.Printf("loaded %d candles for %s from %s", len(candles), symbol, path)
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

func printSignals(signals []scanner.StockSignal, topN int) {
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║      Top Watchlist Candidates        ║")
	fmt.Println("║  (research only — not buy signals)   ║")
	fmt.Println("╚══════════════════════════════════════╝")

	top := topN
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
}
