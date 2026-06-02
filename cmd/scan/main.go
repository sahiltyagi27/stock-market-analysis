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
//	--mode      swing, breakout, or all     (default: swing)
//	--min-rr    minimum risk/reward         (default: 2.0)
//	--max-ema10-extension
//	            max % above EMA10 before filtering as extended
//	--max-ema50-extension
//	            max % above EMA50 before filtering as extended
//	--max-support-extension
//	            max % above support before filtering as extended
//	--max-10d-move
//	            max 10-candle move before filtering as extended
//	--max-breakout-distance
//	            max % below resistance for breakout watch candidates
//	--show-filtered
//	            print diagnostics for filtered symbols
//
// Note: output is for watchlist research purposes only, not buy recommendations.
package main

import (
	"context"
	"database/sql"
	"errors"
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
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

func main() {
	csvPath := flag.String("csv", "", "path to one OHLCV CSV file")
	csvDir := flag.String("csv-dir", "", "path to a directory of OHLCV CSV files")
	dbMode := flag.Bool("db", false, "scan candles from PostgreSQL")
	symbolsFile := flag.String("symbols", "config/symbols.txt", "symbol file for --db")
	period := flag.String("period", "2y", "DB history window (e.g. 2y, 6m, 90d)")
	csvSymbol := flag.String("symbol", "", "stock symbol for --csv; defaults to CSV filename")
	topN := flag.Int("top", 5, "number of top signals to print")
	mode := flag.String("mode", "swing", "scanner mode: swing, breakout, or all")
	minRR := flag.Float64("min-rr", 2.0, "minimum risk/reward ratio")
	emaMargin := flag.Float64("ema-margin", 1.0, "minimum %% gap required between price and EMA200 (0 = disabled)")
	minVolume := flag.Int64("min-volume", 0, "minimum 20-day avg daily volume to qualify (0 = disabled)")
	minResistanceTouches := flag.Int("min-resistance-touches", 2, "minimum touches required for a resistance zone to qualify (1 = allow all)")
	minCandles := flag.Int("min-candles", 200, "minimum candles required per symbol before analysis (0 = use default 200)")
	atrPeriod := flag.Int("atr-period", 14, "ATR period for volatility-based SL sizing (negative = use fixed SL buffer)")
	atrMultiplier := flag.Float64("atr-multiplier", 1.5, "ATR multiplier for SL distance: SL = support.Low − multiplier × ATR")
	maxEMA10Extension := flag.Float64("max-ema10-extension", 8.0, "maximum %% above EMA10 before filtering as extended (<0 disables)")
	maxEMA50Extension := flag.Float64("max-ema50-extension", 15.0, "maximum %% above EMA50 before filtering as extended (<0 disables)")
	maxSupportExtension := flag.Float64("max-support-extension", 5.0, "maximum %% above support high before filtering as extended (<0 disables)")
	maxMove10D := flag.Float64("max-10d-move", 12.0, "maximum 10-candle %% move before filtering as extended (<0 disables)")
	maxBreakoutDistance := flag.Float64("max-breakout-distance", 3.0, "maximum %% below resistance for breakout watch candidates (<0 disables)")
	maxRiskPct := flag.Float64("max-risk-pct", 8.0, "maximum SL distance as %% of entry price (<0 disables)")
	minRiskPct := flag.Float64("min-risk-pct", 1.5, "minimum SL distance as %% of entry price (<0 disables)")
	allowBearishCandle := flag.Bool("allow-bearish-candle", false, "allow bearish signal candles (soft −5 penalty only)")
	ema200SlopePeriod := flag.Int("ema200-slope-period", 20, "candles to look back for EMA200 slope filter (≤0 disables)")
	rsLookback := flag.Int("rs-lookback", 20, "relative-strength lookback vs benchmark candles (0 = disabled)")
	minRSPct := flag.Float64("min-rs-pct", 0, "minimum stock outperformance vs benchmark over --rs-lookback")
	rsSymbol := flag.String("rs-symbol", "NIFTY50", "benchmark symbol for relative-strength filter")
	sectorMapPath := flag.String("sector-map", "config/sector-map.csv", "CSV mapping stock symbols to sector index DB symbols")
	sectorRSLookback := flag.Int("sector-rs-lookback", 20, "sector-strength lookback vs benchmark candles (0 = disabled)")
	minSectorRSPct := flag.Float64("min-sector-rs-pct", 0, "minimum sector index outperformance vs benchmark over --sector-rs-lookback")
	sectorRSStrict := flag.Bool("sector-rs-strict", false, "reject mapped stocks when sector candles are unavailable")
	showFiltered := flag.Bool("show-filtered", false, "print diagnostics for filtered symbols")
	flag.Parse()

	inputs, dataErrs := loadInputs(context.Background(), inputOptions{
		CSVPath:     *csvPath,
		CSVDir:      *csvDir,
		DBMode:      *dbMode,
		SymbolsFile: *symbolsFile,
		Period:      *period,
		Symbol:      *csvSymbol,
	})
	benchmarkCandles := benchmarkFromInputs(&inputs, normalizeSymbol(*rsSymbol))
	if (*rsLookback > 0 || *sectorRSLookback > 0) && *dbMode {
		benchmarkCandles = loadDBBenchmark(context.Background(), *period, normalizeSymbol(*rsSymbol))
	}
	if (*rsLookback > 0 || *sectorRSLookback > 0) && len(benchmarkCandles) == 0 {
		log.Printf("warn: relative-strength filter disabled — no %s benchmark candles found", normalizeSymbol(*rsSymbol))
	}
	sectorMap := loadOptionalSectorMap(*sectorMapPath, *sectorRSLookback)
	sectorCandles := map[string][]models.Candle{}
	if *sectorRSLookback > 0 && len(sectorMap) > 0 && *dbMode {
		sectorCandles = loadDBSectorCandles(context.Background(), *period, sectorMap)
	} else if *sectorRSLookback > 0 && len(sectorMap) > 0 {
		log.Printf("warn: sector-strength filter disabled for CSV scans — sector index candles are loaded from DB only")
		sectorMap = nil
	}

	opts := scanner.Options{
		MinRR:                    *minRR,
		EMAMarginPct:             *emaMargin,
		MinAvgVolume:             *minVolume,
		MaxEMA10ExtensionPct:     *maxEMA10Extension,
		MaxEMA50ExtensionPct:     *maxEMA50Extension,
		MaxSupportExtensionPct:   *maxSupportExtension,
		MaxMove10DPct:            *maxMove10D,
		MaxBreakoutDistancePct:   *maxBreakoutDistance,
		MinCandles:               *minCandles,
		ATRPeriod:                *atrPeriod,
		ATRMultiplier:            *atrMultiplier,
		MaxRiskPct:               *maxRiskPct,
		MinRiskPct:               *minRiskPct,
		AllowBearishCandle:       *allowBearishCandle,
		EMA200SlopePeriod:        *ema200SlopePeriod,
		RelativeStrengthLookback: *rsLookback,
		MinRelativeStrengthPct:   *minRSPct,
		BenchmarkSymbol:          normalizeSymbol(*rsSymbol),
		BenchmarkCandles:         benchmarkCandles,
		SectorStrengthLookback:   *sectorRSLookback,
		MinSectorStrengthPct:     *minSectorRSPct,
		SectorIndexBySymbol:      sectorMap,
		SectorIndexCandles:       sectorCandles,
		SectorStrengthStrict:     *sectorRSStrict,
		ZoneOpts:                 analysis.ZoneOptions{MinResistanceTouches: *minResistanceTouches},
	}
	if *mode != "swing" && *mode != "breakout" && *mode != "all" {
		log.Fatalf("invalid --mode %q: use swing, breakout, or all", *mode)
	}

	var signals []scanner.StockSignal
	var breakouts []scanner.BreakoutSignal
	scanErrs := make(map[string]error)
	breakoutErrs := make(map[string]error)

	if *mode == "swing" || *mode == "all" {
		signals, scanErrs = scanner.ScanWithErrors(inputs, opts)
		printSignals(signals, *topN)
	}
	if *mode == "breakout" || *mode == "all" {
		breakouts, breakoutErrs = scanner.ScanBreakouts(inputs, opts)
		printBreakouts(breakouts, *topN)
	}
	if *showFiltered {
		printDataErrors(dataErrs)
		if *mode == "swing" || *mode == "all" {
			printDiagnostics("Filtered Swing Symbols", scanner.Diagnose(inputs, opts), scanErrs)
		}
		if *mode == "breakout" || *mode == "all" {
			printErrors("Filtered Breakout Symbols", breakoutErrs)
		}
	}

	fmt.Println()
	fmt.Println(display.Dim.Sprint(strings.Repeat("─", 42)))
	fmt.Printf("%s  %d symbols\n", display.Dim.Sprint("Scanned: "), len(inputs))
	if *mode == "breakout" {
		fmt.Printf("%s  %d %s\n", display.Dim.Sprint("Skipped: "), len(dataErrs)+len(breakoutErrs),
			display.Dim.Sprintf("(data errors: %d, no breakout watch: %d)", len(dataErrs), len(breakoutErrs)))
	} else {
		fmt.Printf("%s  %d %s\n", display.Dim.Sprint("Skipped: "), len(dataErrs)+len(scanErrs),
			display.Dim.Sprintf("(data errors: %d, no setup: %d)", len(dataErrs), len(scanErrs)))
	}
	if *mode == "swing" || *mode == "all" {
		fmt.Printf("%s  %s\n", display.Dim.Sprint("Signals: "), display.TotalScore(float64(len(signals))))
	}
	if *mode == "breakout" || *mode == "all" {
		fmt.Printf("%s  %s\n", display.Dim.Sprint("Breakouts:"), display.TotalScore(float64(len(breakouts))))
	}
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

func loadDBBenchmark(ctx context.Context, period, symbol string) []models.Candle {
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

	from, err := parsePeriod(period, time.Now())
	if err != nil {
		log.Fatalf("period: %v", err)
	}
	candles, err := store.NewCandleStore(db).GetCandles(ctx, symbol, store.CandleFilter{From: &from})
	if err != nil {
		log.Printf("warn: load benchmark %s: %v", symbol, err)
		return nil
	}
	return candles
}

func loadOptionalSectorMap(path string, lookback int) map[string]string {
	if lookback <= 0 || strings.TrimSpace(path) == "" {
		return nil
	}
	sectorMap, err := config.LoadSectorMap(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Printf("warn: sector-strength filter disabled — sector map %s not found", path)
			return nil
		}
		log.Printf("warn: sector-strength filter disabled — %v", err)
		return nil
	}
	log.Printf("loaded %d sector mappings from %s", len(sectorMap), path)
	return sectorMap
}

func loadDBSectorCandles(ctx context.Context, period string, sectorMap map[string]string) map[string][]models.Candle {
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

	from, err := parsePeriod(period, time.Now())
	if err != nil {
		log.Fatalf("period: %v", err)
	}
	out := make(map[string][]models.Candle)
	candleStore := store.NewCandleStore(db)
	for _, sectorSymbol := range uniqueSectorSymbols(sectorMap) {
		candles, err := candleStore.GetCandles(ctx, sectorSymbol, store.CandleFilter{From: &from})
		if err != nil {
			log.Printf("warn: sector candles read failed for %s: %v", sectorSymbol, err)
			continue
		}
		if len(candles) == 0 {
			log.Printf("warn: no sector candles found for %s (run kite-sync)", sectorSymbol)
			continue
		}
		out[sectorSymbol] = candles
	}
	log.Printf("loaded candles for %d/%d mapped sector indices", len(out), len(uniqueSectorSymbols(sectorMap)))
	return out
}

func uniqueSectorSymbols(sectorMap map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, sectorSymbol := range sectorMap {
		sectorSymbol = normalizeSymbol(sectorSymbol)
		if sectorSymbol == "" || seen[sectorSymbol] {
			continue
		}
		seen[sectorSymbol] = true
		out = append(out, sectorSymbol)
	}
	sort.Strings(out)
	return out
}

func benchmarkFromInputs(inputs *[]scanner.Input, benchmarkSymbol string) []models.Candle {
	if benchmarkSymbol == "" {
		return nil
	}
	var benchmark []models.Candle
	kept := (*inputs)[:0]
	for _, in := range *inputs {
		if normalizeSymbol(in.Symbol) == benchmarkSymbol {
			benchmark = in.Candles
			continue
		}
		kept = append(kept, in)
	}
	*inputs = kept
	return benchmark
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

func printDiagnostics(title string, diags []scanner.Diagnostic, scanErrs map[string]error) {
	if len(scanErrs) == 0 {
		return
	}

	fmt.Println()
	fmt.Println(title)
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

func printErrors(title string, errs map[string]error) {
	if len(errs) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(title)
	keys := make([]string, 0, len(errs))
	for key := range errs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("   %s: %v\n", key, errs[key])
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
		if sig.Breakdown.CandleDir < 0 {
			fmt.Printf("     %s  %s\n",
				display.Dim.Sprint("Candle: "),
				display.Red.Sprintf("%.0f (bearish — close < open)", sig.Breakdown.CandleDir))
		}

		// Price + trend.
		fmt.Printf("   %s  %.2f\n", display.Dim.Sprint("Price:     "), sig.Price)
		fmt.Printf("   %s  %s\n", display.Dim.Sprint("Trend:     "), display.Trend(string(sig.Trend)))
		fmt.Printf("   %s  EMA10 %s   EMA50 %s   Support %s   10D %s\n",
			display.Dim.Sprint("Extension: "),
			display.Sign(sig.Extension.FromEMA10Pct, "%+.1f%%"),
			display.Sign(sig.Extension.FromEMA50Pct, "%+.1f%%"),
			display.Sign(sig.Extension.FromSupportHighPct, "%+.1f%%"),
			formatMove10D(sig.Extension))
		if sig.RelativeStrength.Lookback > 0 {
			rs := sig.RelativeStrength
			fmt.Printf("   %s  %s  %s\n",
				display.Dim.Sprintf("RS %dD:    ", rs.Lookback),
				display.Sign(rs.OutperformancePct, "%+.2f%%"),
				display.Dim.Sprintf("(%s %.2f%% vs %s %.2f%%)",
					sig.Symbol, rs.StockReturnPct, rs.BenchmarkSymbol, rs.BenchmarkReturnPct))
		}
		if sig.SectorStrength.Lookback > 0 {
			ss := sig.SectorStrength
			fmt.Printf("   %s  %s  %s\n",
				display.Dim.Sprintf("Sector RS %dD:", ss.Lookback),
				display.Sign(ss.OutperformancePct, "%+.2f%%"),
				display.Dim.Sprintf("(%s %.2f%% vs %s %.2f%%)",
					ss.SectorIndexSymbol, ss.SectorReturnPct, ss.BenchmarkSymbol, ss.BenchmarkReturnPct))
		}

		// R/R.
		fmt.Printf("   %s  %s  %s\n",
			display.Dim.Sprint("R/R:       "),
			display.RR(sig.Trade.RiskReward),
			display.Dim.Sprint("(")+display.Quality(string(sig.Trade.Quality))+display.Dim.Sprint(")"))

		// Entry / SL / Target — show ATR period when ATR-based SL was used.
		atrLabel := ""
		if sig.Trade.ATR > 0 {
			atrLabel = display.Dim.Sprintf("  (ATR14: %.2f)", sig.Trade.ATR)
		}
		fmt.Printf("   %s  %.2f   %s %s   %s %s%s\n",
			display.Dim.Sprint("Entry:     "), sig.Trade.Entry,
			display.Dim.Sprint("SL:"), display.Red.Sprintf("%.2f", sig.Trade.StopLoss),
			display.Dim.Sprint("Target:"), display.Green.Sprintf("%.2f", sig.Trade.Target),
			atrLabel)

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

func printBreakouts(signals []scanner.BreakoutSignal, topN int) {
	fmt.Println()
	box := display.BoldCyan.Sprint
	fmt.Println(box("╔══════════════════════════════════════╗"))
	fmt.Println(box("║") + display.Bold.Sprint("         Breakout Watchlist           ") + box("║"))
	fmt.Println(box("║") + display.Dim.Sprint("   (watch for close + volume confirm) ") + box("║"))
	fmt.Println(box("╚══════════════════════════════════════╝"))

	top := topN
	if top > len(signals) {
		top = len(signals)
	}
	if top == 0 {
		fmt.Println("\nNo breakout watch candidates found matching the criteria.")
	}

	for i, sig := range signals[:top] {
		fmt.Printf("\n%s %s\n",
			display.Dim.Sprintf("%d.", i+1),
			display.BoldWhite.Sprint(sig.Symbol))
		fmt.Printf("   %s  %s %s\n",
			display.Dim.Sprint("Score:     "),
			display.TotalScore(sig.Score),
			display.Dim.Sprint("/ 100"))
		fmt.Printf("   %s  %.2f\n", display.Dim.Sprint("Price:     "), sig.Price)
		fmt.Printf("   %s  %.2f – %.2f  %s\n",
			display.Dim.Sprint("Resistance:"),
			sig.Resistance.Low, sig.Resistance.High,
			display.Dim.Sprintf("(%d touches)", sig.Resistance.Touches))
		fmt.Printf("   %s  %s below zone, confirm above %.2f\n",
			display.Dim.Sprint("Breakout:  "),
			display.Cyan.Sprintf("%.2f%%", sig.DistanceToResistancePct),
			sig.BreakoutPrice)
		fmt.Printf("   %s  %.2f – %.2f  %s\n",
			display.Dim.Sprint("Support:   "),
			sig.Support.Low, sig.Support.High,
			display.Dim.Sprintf("(%d touches)", sig.Support.Touches))
		fmt.Printf("   %s  %s\n", display.Dim.Sprint("Trend:     "), display.Trend(string(sig.Trend)))
		fmt.Printf("   %s  EMA10 %s   EMA50 %s   Support %s   10D %s\n",
			display.Dim.Sprint("Extension: "),
			display.Sign(sig.Extension.FromEMA10Pct, "%+.1f%%"),
			display.Sign(sig.Extension.FromEMA50Pct, "%+.1f%%"),
			display.Sign(sig.Extension.FromSupportHighPct, "%+.1f%%"),
			formatMove10D(sig.Extension))
		if sig.Volume.AvgVolume > 0 {
			fmt.Printf("   %s  latest %.0f, avg20 %.0f, %.2fx\n",
				display.Dim.Sprint("Volume:    "),
				sig.Volume.LastVolume, sig.Volume.AvgVolume, sig.Volume.VolumeRatio)
		}
		fmt.Printf("   %s\n", display.Dim.Sprint("Reasons:"))
		for _, r := range sig.Reasons {
			fmt.Printf("     %s %s\n", display.Cyan.Sprint("•"), display.Dim.Sprint(r))
		}
	}
}

func formatMove10D(ext scanner.Extension) string {
	if !ext.HasMove10D {
		return display.Dim.Sprint("n/a")
	}
	return display.Sign(ext.Move10DPct, "%+.1f%%")
}
