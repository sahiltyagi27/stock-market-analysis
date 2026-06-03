// Command backtest runs a walk-forward simulation of the scanner pipeline
// over historical PostgreSQL candle data and reports trade-level results plus
// aggregate performance statistics.
//
// For every trading day D in [--from, --to], the full scanner pipeline is run
// on all candles up to D. When a signal is produced, a trade is entered at the
// open of D+1 and held until the stop-loss, target, or --max-hold days elapsed.
// No overlapping trades are allowed per symbol.
//
// Usage:
//
//	go run ./cmd/backtest --from 2024-01-01 --to 2024-12-31
//	go run ./cmd/backtest --from 2024-01-01 --min-score 65 --output results.csv
//
// Scanner flags mirror cmd/scan and cmd/live-scan for consistency.
package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/internal/backtest"
	"github.com/sahiltyagi27/stock-market-analysis/internal/crossover"
	"github.com/sahiltyagi27/stock-market-analysis/internal/display"
	"github.com/sahiltyagi27/stock-market-analysis/internal/kite"
	"github.com/sahiltyagi27/stock-market-analysis/internal/meanrev"
	"github.com/sahiltyagi27/stock-market-analysis/internal/scanner"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

func main() {
	symbolsFile := flag.String("symbols", "config/symbols.txt", "path to watchlist file")
	fromStr := flag.String("from", "", "start of signal-date window, YYYY-MM-DD (empty = no lower bound)")
	toStr := flag.String("to", "", "end of signal-date window, YYYY-MM-DD (empty = today)")
	mode := flag.String("mode", "swing", "scanner strategy: swing, crossover, or meanrev (meanrev = REJECTED experiment, see ANALYSIS.md §10)")
	minScore := flag.Float64("min-score", 0, "skip signals below this score (0 = all)")
	maxHold := flag.Int("max-hold", 20, "maximum candles to hold before timing out")
	workers := flag.Int("workers", 8, "parallel goroutines for simulation")
	topN := flag.Int("top", 30, "trades to print when --capital is disabled (sorted by score)")
	outputCSV := flag.String("output", "", "write all trades to this CSV file (empty = no file)")
	capital := flag.Float64("capital", 100000, "starting capital in INR for the P&L journey; 0 = show top-N by score instead")
	portfolio := flag.Bool("portfolio", false, "portfolio-aware mode: shared capital pool, concurrent-position cap")
	maxPositions := flag.Int("max-positions", 5, "[portfolio] maximum simultaneous open positions")
	exitMode := flag.String("exit-mode", "ema", "[portfolio] exit rule: ema (EMA7<EMA21 recross) or target")
	costPct := flag.Float64("cost-pct", 0.25, "[portfolio] round-trip transaction cost %% of notional (brokerage+STT+fees); 0 = frictionless")
	slippagePct := flag.Float64("slippage-pct", 0.20, "[portfolio] adverse fill haircut %% per leg; 0 = perfect fills")
	allocLookback := flag.Int("alloc-lookback", 0, "[portfolio] rank same-day candidates for free slots by N-candle leadership return (0 = by scanner score)")
	riskPct := flag.Float64("risk-pct", 1.0, "[portfolio] risk-based sizing (default): size each trade so a stop-out costs this %% of equity; negative = equal 1/N slices")
	maxWeightPct := flag.Float64("max-weight-pct", 25, "[portfolio] cap any single position at this %% of equity under risk-based sizing")
	regimeMode := flag.String("regime", "off", "[portfolio] market gate on NIFTY: off | price (close>EMA200) | ema (EMA50>EMA200) — blocks new entries in unhealthy regimes")
	regimeSymbol := flag.String("regime-symbol", kite.Nifty50Symbol, "[portfolio] benchmark DB symbol for the regime gate")
	regimeFast := flag.Int("regime-fast", 50, "[portfolio] fast EMA period for --regime ema")
	regimeSlow := flag.Int("regime-slow", 200, "[portfolio] slow EMA period for the regime gate")
	healthWindow := flag.Int("health-window", 20, "[portfolio] strategy-health gate (default): only enter when the last N closed trades are healthy (0 = off)")
	healthMode := flag.String("health-mode", "avgr", "[portfolio] health metric: avgr (mean R) or pf (profit factor)")
	healthMin := flag.Float64("health-min", 0, "[portfolio] health threshold over the window (e.g. 0 for avgr, 1.2 for pf)")
	healthWarmupFrom := flag.String("health-warmup-from", "", "[portfolio] seed the health gate with trades from this date up to --from (avoids cold-start blindness), YYYY-MM-DD")

	// Scanner flags — mirror live-scan / scan for identical filter behaviour.
	minRR := flag.Float64("min-rr", 2.0, "minimum risk/reward ratio")
	emaMargin := flag.Float64("ema-margin", 1.0, "minimum %% gap required above EMA200 (0 = disabled)")
	minVolume := flag.Int64("min-volume", 0, "minimum rolling avg daily volume (0 = disabled)")
	minResistanceTouches := flag.Int("min-resistance-touches", 2, "minimum touches for a resistance zone")
	minCandles := flag.Int("min-candles", 200, "minimum historical candles required before analysis")
	atrPeriod := flag.Int("atr-period", 14, "ATR period for volatility-based SL (negative = fixed buffer)")
	atrMultiplier := flag.Float64("atr-multiplier", 1.5, "ATR multiplier: SL = support.Low − multiplier × ATR")
	maxEMA10Extension := flag.Float64("max-ema10-extension", 8.0, "max %% above EMA10 (<0 disables)")
	maxEMA50Extension := flag.Float64("max-ema50-extension", 15.0, "max %% above EMA50 (<0 disables)")
	maxSupportExtension := flag.Float64("max-support-extension", 5.0, "max %% above support high (<0 disables)")
	maxMove10D := flag.Float64("max-10d-move", 12.0, "max 10-candle %% move (<0 disables)")
	maxRiskPct := flag.Float64("max-risk-pct", 8.0, "maximum SL distance as %% of entry price (<0 disables)")
	minRiskPct := flag.Float64("min-risk-pct", 1.5, "minimum SL distance as %% of entry price (<0 disables)")
	allowBearishCandle := flag.Bool("allow-bearish-candle", false, "allow bearish signal candles (soft −5 penalty only)")
	ema200SlopePeriod := flag.Int("ema200-slope-period", 20, "candles to look back for EMA200 slope filter (≤0 disables)")
	trailATRMult := flag.Float64("trail-atr-mult", 1.5, "ATR multiplier for trailing stop: trailSL = highestHigh − mult×ATR (≤0 disables)")
	rsLookback := flag.Int("rs-lookback", 0, "[swing] relative-strength lookback vs benchmark candles (0 = disabled; backtests show it reduces returns)")
	minRSPct := flag.Float64("min-rs-pct", 0, "[swing] minimum stock outperformance vs benchmark over --rs-lookback")
	rsSymbol := flag.String("rs-symbol", kite.Nifty50Symbol, "[swing] benchmark DB symbol for relative-strength filter")
	sectorMapPath := flag.String("sector-map", "config/sector-map.csv", "[swing] CSV mapping stock symbols to sector index DB symbols")
	sectorRSLookback := flag.Int("sector-rs-lookback", 0, "[swing] sector-strength lookback vs benchmark candles (0 = disabled; backtests show it reduces returns)")
	minSectorRSPct := flag.Float64("min-sector-rs-pct", 0, "[swing] minimum sector index outperformance vs benchmark over --sector-rs-lookback")
	sectorRSStrict := flag.Bool("sector-rs-strict", false, "[swing] reject mapped stocks when sector candles are unavailable")

	// Crossover-mode flags (only used when --mode crossover).
	coMaxAge := flag.Int("co-max-age", 2, "[crossover] max candles since EMA7×21 crossover (default 2 = last 3 candles)")
	coMinRR := flag.Float64("co-min-rr", 1.5, "[crossover] minimum risk/reward ratio")
	coMinCandles := flag.Int("co-min-candles", 50, "[crossover] minimum candles required before analysis")
	coVolWindow := flag.Int("co-vol-window", 20, "[crossover] volume rolling-average window")
	coMinResTouches := flag.Int("co-min-resistance-touches", 1, "[crossover] minimum touches for a resistance zone target")
	coMinVolMult := flag.Float64("co-min-vol-mult", 0, "[crossover] require today's volume ≥ this × the prev N-day avg (0 = disabled)")
	coVolMultWindow := flag.Int("co-vol-mult-window", 10, "[crossover] candles for the today's-volume average check")
	coMinTargetPct := flag.Float64("co-min-target-pct", 4.0, "[crossover] min %% distance the resistance target must sit above entry (<0 disables)")
	coMinRiskPct := flag.Float64("co-min-risk-pct", 3.0, "[crossover] min SL distance as %% of price; widens a too-tight previous-candle-low stop (<0 disables)")
	coMinCloseStrength := flag.Float64("co-min-close-strength", 0, "[crossover] EXPERIMENTAL — require signal candle to close in top of range: (close-low)/(high-low) ≥ this (0 = off). NOT a robust edge; response is chaotic across thresholds — see ANALYSIS.md §9")

	// Mean-reversion-mode flags (only used when --mode meanrev).
	// REJECTED experiment — loses in every regime, worst in the weak years it
	// was meant to help; kept only for reproducibility. See ANALYSIS.md §10.
	mrRSIPeriod := flag.Int("mr-rsi-period", 2, "[meanrev] RSI lookback for the oversold trigger (Connors RSI-2)")
	mrMaxRSI := flag.Float64("mr-max-rsi", 10, "[meanrev] fire only when RSI(mr-rsi-period) is below this (deeply oversold)")
	mrTrendPeriod := flag.Int("mr-trend-period", 200, "[meanrev] long-term EMA the price must sit above (buy dips only in up-trends)")
	mrMeanPeriod := flag.Int("mr-mean-period", 10, "[meanrev] EMA used as the reversion target (the mean price snaps back to)")
	mrStopATRMult := flag.Float64("mr-stop-atr-mult", 2.5, "[meanrev] SL = close − this × ATR (wide; mean/time exit leads)")
	mrATRPeriod := flag.Int("mr-atr-period", 14, "[meanrev] ATR period for the stop")
	mrMinCandles := flag.Int("mr-min-candles", 0, "[meanrev] min candles before analysis (0 = trend-period + 10)")

	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── Parse date range ──────────────────────────────────────────────────────
	var from, to time.Time
	if *fromStr != "" {
		t, err := time.Parse("2006-01-02", *fromStr)
		if err != nil {
			log.Fatalf("--from: invalid date %q, expected YYYY-MM-DD", *fromStr)
		}
		from = t
	}
	if *toStr != "" {
		t, err := time.Parse("2006-01-02", *toStr)
		if err != nil {
			log.Fatalf("--to: invalid date %q, expected YYYY-MM-DD", *toStr)
		}
		to = t
	}

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
	// Load all available history — the scanner needs the full lookback even for
	// signal days at the start of the requested window.
	candleStore := store.NewCandleStore(db)
	log.Printf("loading candles from DB…")

	candlesMap := make(map[string][]models.Candle, len(symbols))
	for _, sym := range symbols {
		cc, err := candleStore.GetCandles(ctx, sym, store.CandleFilter{})
		if err != nil {
			log.Printf("  warn: %s: %v", sym, err)
			continue
		}
		if len(cc) > 0 {
			candlesMap[sym] = cc
		}
	}
	log.Printf("loaded candles for %d/%d symbols", len(candlesMap), len(symbols))
	var benchmarkCandles []models.Candle
	benchmarkSymbol := kite.NormalizeSymbol(*rsSymbol)
	if (*rsLookback > 0 || *sectorRSLookback > 0) && *mode == "swing" {
		cc, err := candleStore.GetCandles(ctx, benchmarkSymbol, store.CandleFilter{})
		if err != nil {
			log.Printf("warn: relative-strength benchmark read failed for %s: %v", benchmarkSymbol, err)
		} else if len(cc) == 0 {
			log.Printf("warn: relative-strength filter disabled — no %s candles in DB (run kite-sync)", benchmarkSymbol)
		} else {
			benchmarkCandles = cc
			log.Printf("loaded %d %s benchmark candles for relative strength", len(benchmarkCandles), benchmarkSymbol)
		}
	}
	sectorMap := loadOptionalSectorMap(*sectorMapPath, *sectorRSLookback, *mode)
	sectorCandles := map[string][]models.Candle{}
	if *sectorRSLookback > 0 && *mode == "swing" && len(sectorMap) > 0 {
		sectorCandles = loadSectorCandles(ctx, candleStore, sectorMap)
	}

	// ── Run backtest ──────────────────────────────────────────────────────────
	scanOpts := scanner.Options{
		MinRR:                    *minRR,
		EMAMarginPct:             *emaMargin,
		MinAvgVolume:             *minVolume,
		MinCandles:               *minCandles,
		MaxEMA10ExtensionPct:     *maxEMA10Extension,
		MaxEMA50ExtensionPct:     *maxEMA50Extension,
		MaxSupportExtensionPct:   *maxSupportExtension,
		MaxMove10DPct:            *maxMove10D,
		ATRPeriod:                *atrPeriod,
		ATRMultiplier:            *atrMultiplier,
		MaxRiskPct:               *maxRiskPct,
		MinRiskPct:               *minRiskPct,
		AllowBearishCandle:       *allowBearishCandle,
		EMA200SlopePeriod:        *ema200SlopePeriod,
		RelativeStrengthLookback: *rsLookback,
		MinRelativeStrengthPct:   *minRSPct,
		BenchmarkSymbol:          benchmarkSymbol,
		BenchmarkCandles:         benchmarkCandles,
		SectorStrengthLookback:   *sectorRSLookback,
		MinSectorStrengthPct:     *minSectorRSPct,
		SectorIndexBySymbol:      sectorMap,
		SectorIndexCandles:       sectorCandles,
		SectorStrengthStrict:     *sectorRSStrict,
		ZoneOpts: analysis.ZoneOptions{
			MinResistanceTouches: *minResistanceTouches,
		},
	}

	if *mode != "swing" && *mode != "crossover" && *mode != "meanrev" {
		log.Fatalf("--mode must be swing, crossover, or meanrev, got %q", *mode)
	}

	mrOpts := meanrev.Options{
		RSIPeriod:   *mrRSIPeriod,
		MaxRSI:      *mrMaxRSI,
		TrendPeriod: *mrTrendPeriod,
		MeanPeriod:  *mrMeanPeriod,
		StopATRMult: *mrStopATRMult,
		ATRPeriod:   *mrATRPeriod,
		MinCandles:  *mrMinCandles,
	}

	coOpts := crossover.Options{
		MaxCrossoverAge:       *coMaxAge,
		MinRR:                 *coMinRR,
		MinCandles:            *coMinCandles,
		VolumeWindow:          *coVolWindow,
		MinCurrentVolMultiple: *coMinVolMult,
		CurrentVolWindow:      *coVolMultWindow,
		MinTargetPct:          *coMinTargetPct,
		MinRiskPct:            *coMinRiskPct,
		MinCloseStrength:      *coMinCloseStrength,
		ZoneOpts:              analysis.ZoneOptions{MinResistanceTouches: *coMinResTouches},
	}

	opts := backtest.Options{
		From:               from,
		To:                 to,
		Mode:               *mode,
		MinScore:           *minScore,
		MaxHold:            *maxHold,
		Workers:            *workers,
		TrailATRMultiplier: *trailATRMult,
		ScanOpts:           scanOpts,
		CrossoverOpts:      coOpts,
		MeanRevOpts:        mrOpts,
		Progress: func(done, total int) {
			if done%50 == 0 || done == total {
				log.Printf("  simulating: %d/%d symbols…", done, total)
			}
		},
	}

	fromLabel := "all-time"
	if !from.IsZero() {
		fromLabel = from.Format("2006-01-02")
	}
	toLabel := "today"
	if !to.IsZero() {
		toLabel = to.Format("2006-01-02")
	}
	// ── Portfolio-aware mode ────────────────────────────────────────────────────
	if *portfolio {
		if *exitMode != "ema" && *exitMode != "target" {
			log.Fatalf("--exit-mode must be ema or target, got %q", *exitMode)
		}
		// Regime gate: load the benchmark candles when enabled.
		var regimeCandles []models.Candle
		if *regimeMode != "" && *regimeMode != "off" {
			rsym := kite.NormalizeSymbol(*regimeSymbol)
			cc, err := candleStore.GetCandles(ctx, rsym, store.CandleFilter{})
			if err != nil || len(cc) == 0 {
				log.Fatalf("--regime %s needs %s candles in DB (run kite-sync): %v", *regimeMode, rsym, err)
			}
			regimeCandles = cc
			log.Printf("regime gate: %s on %s (EMA%d/%d), %d candles", *regimeMode, rsym, *regimeFast, *regimeSlow, len(cc))
		}

		pf := backtest.PortfolioOptions{
			From:         from,
			To:           to,
			MinScore:     *minScore,
			MaxPositions: *maxPositions,
			StartCapital: *capital,
			ExitMode:     *exitMode,
			MaxHoldDays:  *maxHold,
			CostPct:         *costPct,
			SlippagePct:     *slippagePct,
			AllocLookback:   *allocLookback,
			RiskPct:         *riskPct,
			MaxWeightPct:    *maxWeightPct,
			RegimeMode:      *regimeMode,
			RegimeBenchmark: regimeCandles,
			RegimeFast:      *regimeFast,
			RegimeSlow:      *regimeSlow,
			StrategyHealthWindow: *healthWindow,
			StrategyHealthMode:   *healthMode,
			StrategyHealthMin:    *healthMin,
			EngineOpts:      opts,
		}

		// Cold-start fix: seed the health gate with a warmup pass so it is warm
		// at --from (mirrors loading the last N completed trades from the DB live).
		if *healthWindow > 0 && *healthWarmupFrom != "" {
			wfrom, err := time.Parse("2006-01-02", *healthWarmupFrom)
			if err != nil {
				log.Fatalf("--health-warmup-from: invalid date %q", *healthWarmupFrom)
			}
			warm := pf
			warm.From = wfrom
			warm.To = from.AddDate(0, 0, -1)
			warm.HealthSeed = nil // warmup itself starts cold (far enough back not to matter)
			wtrades, _ := backtest.RunPortfolio(ctx, candlesMap, warm)
			pf.HealthSeed = lastNTradeR(wtrades, *healthWindow)
			log.Printf("health gate warmup: %s → %s produced %d trades; seeded gate with last %d",
				wfrom.Format("2006-01-02"), warm.To.Format("2006-01-02"), len(wtrades), len(pf.HealthSeed))
		}

		log.Printf("running PORTFOLIO backtest: %s → %s | mode: %s | exit: %s | max-pos %d | capital %.0f | cost %.2f%% | slip %.2f%%",
			fromLabel, toLabel, *mode, *exitMode, *maxPositions, *capital, *costPct, *slippagePct)
		trades, stats := backtest.RunPortfolio(ctx, candlesMap, pf)
		printPortfolio(trades, stats, fromLabel, toLabel, *mode, *exitMode, *maxPositions, *costPct, *slippagePct)
		if *outputCSV != "" {
			if err := writeCSV(*outputCSV, trades); err != nil {
				log.Printf("warn: CSV write failed: %v", err)
			} else {
				log.Printf("CSV written to %s (%d rows)", *outputCSV, len(trades))
			}
		}
		return
	}

	log.Printf("running backtest: %s → %s | mode: %s | max-hold %dd | workers %d",
		fromLabel, toLabel, *mode, *maxHold, *workers)

	results := backtest.Run(ctx, candlesMap, opts)
	log.Printf("simulation complete — %d trades generated", len(results))

	if len(results) == 0 {
		fmt.Println("\n  No trades generated. Try widening the date range or lowering --min-score.")
		return
	}

	// ── Terminal output ───────────────────────────────────────────────────────
	if *capital > 0 {
		printCapitalJourney(results, *capital, fromLabel, toLabel)
	} else {
		printResults(results, *topN, fromLabel, toLabel)
	}

	// ── CSV export ────────────────────────────────────────────────────────────
	if *outputCSV != "" {
		if err := writeCSV(*outputCSV, results); err != nil {
			log.Printf("warn: CSV write failed: %v", err)
		} else {
			log.Printf("CSV written to %s (%d rows)", *outputCSV, len(results))
		}
	}
}

// ── Terminal display ──────────────────────────────────────────────────────────

func printResults(results []backtest.TradeResult, topN int, fromLabel, toLabel string) {
	// Terminal table: top N by score DESC.
	byScore := make([]backtest.TradeResult, len(results))
	copy(byScore, results)
	sort.SliceStable(byScore, func(i, j int) bool {
		return byScore[i].Score > byScore[j].Score
	})

	bannerText := fmt.Sprintf("━━━  Backtest  %s → %s  (%d trades)  ━━━",
		fromLabel, toLabel, len(results))
	fmt.Printf("\n%s\n", display.BoldCyan.Sprint(bannerText))

	top := topN
	if top > len(byScore) {
		top = len(byScore)
	}

	for i, r := range byScore[:top] {
		outcomeStr := outcomeLabel(r.Outcome)
		rrStr := display.RR(r.ActualRR)
		score := display.TotalScore(r.Score)

		fmt.Printf("\n  %s %s  %s  %s%s/100\n",
			display.Dim.Sprintf("%3d.", i+1),
			display.BoldWhite.Sprint(fmt.Sprintf("%-14s", r.Symbol)),
			display.Dim.Sprint(r.SignalDate.Format("02-Jan-2006")),
			display.Dim.Sprint("Score: "),
			score)

		fmt.Printf("       %s %s %s  %s %s  %s %s\n",
			display.Dim.Sprint("├ Entry:"),
			fmt.Sprintf("%-9.2f", r.Entry),
			display.Dim.Sprint("→"),
			display.Dim.Sprint("SL:"),
			display.Red.Sprintf("%-9.2f", r.SL),
			display.Dim.Sprint("Target:"),
			display.Green.Sprintf("%.2f", r.Target))

		holdStr := fmt.Sprintf("%dd", r.HoldDays)
		fmt.Printf("       %s %s  %s %s  %s %s\n",
			display.Dim.Sprint("└ Outcome:"),
			outcomeStr,
			display.Dim.Sprint("R:R:"),
			rrStr,
			display.Dim.Sprint("Hold:"),
			display.Dim.Sprint(holdStr))
	}

	if len(byScore) > top {
		fmt.Printf("\n  %s\n",
			display.Dim.Sprintf("… %d more trades (use --top %d to see all or --output for CSV)",
				len(byScore)-top, len(byScore)))
	}

	// ── Summary ───────────────────────────────────────────────────────────────
	sum := backtest.Compute(results)
	printSummary(sum, fromLabel, toLabel)
}

func loadOptionalSectorMap(path string, lookback int, mode string) map[string]string {
	if lookback <= 0 || mode != "swing" || strings.TrimSpace(path) == "" {
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

func loadSectorCandles(ctx context.Context, candleStore *store.CandleStore, sectorMap map[string]string) map[string][]models.Candle {
	out := map[string][]models.Candle{}
	for _, sectorSymbol := range uniqueSectorSymbols(sectorMap) {
		candles, err := candleStore.GetCandles(ctx, sectorSymbol, store.CandleFilter{})
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
		sectorSymbol = kite.NormalizeSymbol(sectorSymbol)
		if sectorSymbol == "" || seen[sectorSymbol] {
			continue
		}
		seen[sectorSymbol] = true
		out = append(out, sectorSymbol)
	}
	sort.Strings(out)
	return out
}

func printSummary(s backtest.Summary, fromLabel, toLabel string) {
	sep := display.Dim.Sprint(strings.Repeat("─", 52))
	fmt.Printf("\n  %s\n", sep)

	hdr := fmt.Sprintf("Backtest Summary  %s → %s", fromLabel, toLabel)
	fmt.Printf("  %s\n\n", display.BoldWhite.Sprint(hdr))

	fmt.Printf("  %s %s\n",
		display.Dim.Sprint("Total trades  :"),
		display.Bold.Sprintf("%d", s.Total))

	winRateStr := display.TotalScore(s.WinRate) // reuse score colouring: ≥80 green, ≥60 yellow
	fmt.Printf("  %s %s  %s\n",
		display.Dim.Sprint("Win rate      :"),
		winRateStr+display.Dim.Sprint("%"),
		display.Dim.Sprintf("(%d wins / %d losses / %d trail stops / %d timeouts)",
			s.Wins, s.Losses, s.TrailStops, s.Timeouts))

	fmt.Printf("  %s %s\n",
		display.Dim.Sprint("Avg winner    :"),
		display.Green.Sprintf("+%.2fR", s.AvgWinRR))

	fmt.Printf("  %s %s\n",
		display.Dim.Sprint("Avg loser     :"),
		display.Red.Sprintf("%.2fR", s.AvgLossRR))

	pfStr := formatPF(s.ProfitFactor)
	fmt.Printf("  %s %s\n",
		display.Dim.Sprint("Profit factor :"),
		pfStr)

	expStr := display.Sign(s.Expectancy, "%+.3fR")
	fmt.Printf("  %s %s %s\n",
		display.Dim.Sprint("Expectancy    :"),
		expStr,
		display.Dim.Sprint("per trade"))

	if s.TrailStops > 0 {
		fmt.Printf("  %s %s  %s\n",
			display.Dim.Sprint("Trail stops   :"),
			display.Cyan.Sprintf("%d", s.TrailStops),
			display.Dim.Sprintf("(avg exit %+.2fR)", s.AvgTrailStopRR))
	}
	fmt.Printf("  %s %s\n",
		display.Dim.Sprint("Max consec L  :"),
		display.Red.Sprintf("%d", s.MaxConsecLoss))

	fmt.Printf("  %s %.1f %s\n",
		display.Dim.Sprint("Avg hold      :"),
		s.AvgHoldDays,
		display.Dim.Sprint("days"))

	fmt.Printf("  %s\n", sep)
	fmt.Println()
}

func printPortfolio(trades []backtest.TradeResult, s backtest.PortfolioStats, fromLabel, toLabel, mode, exitMode string, maxPos int, costPct, slippagePct float64) {
	sep := display.Dim.Sprint(strings.Repeat("─", 60))
	banner := fmt.Sprintf("━━━  Portfolio Backtest  %s → %s  ━━━", fromLabel, toLabel)
	fmt.Printf("\n%s\n", display.BoldCyan.Sprint(banner))

	fmt.Printf("  %s\n", display.Dim.Sprintf("mode: %s   exit: %s   max-positions: %d", mode, exitMode, maxPos))
	fmt.Printf("  %s\n", display.Dim.Sprintf("costs: %.2f%% round-trip   slippage: %.2f%%/leg", costPct, slippagePct))
	fmt.Printf("\n  %s\n", sep)

	fmt.Printf("  %s  %s\n", display.Dim.Sprint("Starting capital :"), display.Bold.Sprint(formatINR(s.StartCapital)))

	capColor := display.BoldGreen.Sprint
	if s.FinalCapital < s.StartCapital {
		capColor = display.Red.Sprint
	}
	fmt.Printf("  %s  %s\n", display.Dim.Sprint("Final capital    :"), capColor(formatINR(s.FinalCapital)))

	retColor := display.BoldGreen.Sprintf
	if s.ReturnPct < 0 {
		retColor = display.Red.Sprintf
	}
	years := 4.0
	cagr := 0.0
	if s.StartCapital > 0 && s.FinalCapital > 0 {
		cagr = (pow(s.FinalCapital/s.StartCapital, 1.0/years) - 1) * 100
	}
	fmt.Printf("  %s  %s   %s\n",
		display.Dim.Sprint("Total return     :"),
		retColor("%+.1f%%", s.ReturnPct),
		display.Dim.Sprintf("(~%.1f%%/yr CAGR)", cagr))

	fmt.Printf("  %s  %s\n", display.Dim.Sprint("Max drawdown     :"), display.Red.Sprintf("-%.1f%%", s.MaxDrawdownPct))

	wr := 0.0
	if s.Wins+s.Losses > 0 {
		wr = float64(s.Wins) / float64(s.Wins+s.Losses) * 100
	}
	fmt.Printf("  %s  %s  %s\n",
		display.Dim.Sprint("Win rate         :"),
		display.TotalScore(wr)+display.Dim.Sprint("%"),
		display.Dim.Sprintf("(%d W / %d L of %d trades)", s.Wins, s.Losses, s.Trades))

	fmt.Printf("  %s  %s   %s %s\n",
		display.Dim.Sprint("Profit factor    :"), formatPF(s.ProfitFactor),
		display.Dim.Sprint("Avg R:R          :"), display.Sign(s.AvgRR, "%+.2fR"))
	fmt.Printf("  %s  %.1f %s\n", display.Dim.Sprint("Avg hold         :"), s.AvgHoldDays, display.Dim.Sprint("days"))
	fmt.Printf("  %s  %d\n", display.Dim.Sprint("Peak concurrent  :"), s.MaxConcurrent)

	// Opportunity loss (M10): what the slot limit cost you.
	if s.RejectedFull > 0 {
		fmt.Printf("  %s\n", sep)
		fmt.Printf("  %s\n", display.Dim.Sprint("Opportunity loss (signals rejected because portfolio was full):"))
		fmt.Printf("  %s  %d   %s %s   %s %.0f%%\n",
			display.Dim.Sprint("  Rejected      :"), s.RejectedFull,
			display.Dim.Sprint("avg R:R"), display.Sign(s.RejectedAvgRR, "%+.2fR"),
			display.Dim.Sprint("win"), s.RejectedWinRate)
		fmt.Printf("  %s  %s %s   %s %.0f%%\n",
			display.Dim.Sprint("  vs Accepted   :"),
			display.Dim.Sprint("avg R:R"), display.Sign(s.AvgRR, "%+.2fR"),
			display.Dim.Sprint("win"),
			func() float64 {
				if s.Wins+s.Losses == 0 {
					return 0
				}
				return float64(s.Wins) / float64(s.Wins+s.Losses) * 100
			}())
	}
	fmt.Printf("  %s\n", sep)
	fmt.Printf("%s\n", display.BoldCyan.Sprint(strings.Repeat("━", len(banner))))
}

func pow(x, y float64) float64 { return math.Pow(x, y) }

// lastNTradeR returns the realised R of the most recent n trades by exit date,
// in chronological (oldest-first) order — ready to seed the health window.
func lastNTradeR(trades []backtest.TradeResult, n int) []float64 {
	sorted := make([]backtest.TradeResult, len(trades))
	copy(sorted, trades)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ExitDate.Before(sorted[j].ExitDate) })
	if n > len(sorted) {
		n = len(sorted)
	}
	out := make([]float64, 0, n)
	for _, t := range sorted[len(sorted)-n:] {
		out = append(out, t.ActualRR)
	}
	return out
}

func formatPF(pf float64) string {
	if math.IsInf(pf, 1) {
		return display.BoldGreen.Sprint("∞ (no losses)")
	}
	switch {
	case pf >= 2.0:
		return display.BoldGreen.Sprintf("%.2f", pf)
	case pf >= 1.0:
		return display.Green.Sprintf("%.2f", pf)
	default:
		return display.Red.Sprintf("%.2f", pf)
	}
}

func outcomeLabel(o backtest.Outcome) string {
	switch o {
	case backtest.OutcomeWin:
		return display.BoldGreen.Sprint("✅ WIN    ")
	case backtest.OutcomeLoss:
		return display.Red.Sprint("❌ LOSS   ")
	case backtest.OutcomeTrailStop:
		return display.Cyan.Sprint("📈 TRAIL  ")
	default:
		return display.Yellow.Sprint("⏱ TIMEOUT")
	}
}

// ── Capital journey ───────────────────────────────────────────────────────────

// printCapitalJourney shows every trade chronologically with full P&L detail
// as if all capital were deployed into each trade serially.  Shares bought =
// floor(capital / entry); any remaining fractional-share cash carries forward.
func printCapitalJourney(results []backtest.TradeResult, startCapital float64, fromLabel, toLabel string) {
	bannerText := fmt.Sprintf("━━━  Capital Journey  %s → %s  Starting: %s  ━━━",
		fromLabel, toLabel, formatINR(startCapital))
	fmt.Printf("\n%s\n", display.BoldCyan.Sprint(bannerText))

	if len(results) == 0 {
		fmt.Println("  No trades to show.")
		return
	}

	capital := startCapital
	var bestPnL, worstPnL float64
	var bestIdx, worstIdx int
	first := true

	for i, r := range results {
		shares := int(capital / r.Entry)
		invested := float64(shares) * r.Entry
		cash := capital - invested // leftover cash (< 1 share)

		var exitValue, pnl float64
		if shares == 0 {
			// Can't buy even one share — skip but still show it.
			fmt.Printf("\n  %s %s  %s → %s  (%dd)\n",
				display.Dim.Sprintf("%3d.", i+1),
				display.BoldWhite.Sprint(fmt.Sprintf("%-14s", r.Symbol)),
				r.EntryDate.Format("02-Jan-2006"),
				r.ExitDate.Format("02-Jan-2006"),
				r.HoldDays)
			fmt.Printf("       %s %s\n",
				display.Dim.Sprint("└ Skipped:"),
				display.Yellow.Sprintf("capital %s below entry ₹%.2f", formatINR(capital), r.Entry))
			continue
		}

		exitValue = float64(shares) * r.ExitPrice
		pnl = exitValue - invested
		capital = exitValue + cash
		pnlPct := pnl / invested * 100

		if first || pnl > bestPnL {
			bestPnL = pnl
			bestIdx = i
		}
		if first || pnl < worstPnL {
			worstPnL = pnl
			worstIdx = i
		}
		first = false

		// ── Trade header ─────────────────────────────────────────────────────
		sym := display.BoldWhite.Sprint(fmt.Sprintf("%-14s", r.Symbol))
		dateRange := fmt.Sprintf("%s → %s",
			r.EntryDate.Format("02-Jan-2006"),
			r.ExitDate.Format("02-Jan-2006"))
		fmt.Printf("\n  %s %s  %s  %s\n",
			display.Dim.Sprintf("%3d.", i+1),
			sym,
			display.Dim.Sprint(dateRange),
			display.Dim.Sprintf("(%dd  score %.0f)", r.HoldDays, r.Score))

		// ── Bought line ───────────────────────────────────────────────────────
		fmt.Printf("       %s %s shares @ %s  =  %s\n",
			display.Dim.Sprint("├ Bought"),
			display.Cyan.Sprintf("%d", shares),
			display.Cyan.Sprintf("₹%.2f", r.Entry),
			display.Dim.Sprint(formatINR(invested)))

		// ── Exit line ─────────────────────────────────────────────────────────
		exitVerb := journeyExitVerb(r.Outcome)
		pnlColor := display.Green.Sprintf
		if pnl < 0 {
			pnlColor = display.Red.Sprintf
		}
		fmt.Printf("       %s %s @ %s  →  %s  (%s  %s)\n",
			display.Dim.Sprint("└"),
			exitVerb,
			display.Cyan.Sprintf("₹%.2f", r.ExitPrice),
			display.Bold.Sprint(formatINR(capital)),
			pnlColor("%+.2f", pnl),
			pnlColor("%+.1f%%", pnlPct))
	}

	// ── Capital summary ───────────────────────────────────────────────────────
	sep := display.Dim.Sprint(strings.Repeat("─", 56))
	totalPnL := capital - startCapital
	totalPct := totalPnL / startCapital * 100

	fmt.Printf("\n  %s\n", sep)
	fmt.Printf("  %s  %s\n",
		display.Dim.Sprint("Starting capital :"),
		display.Bold.Sprint(formatINR(startCapital)))

	capColor := display.BoldGreen.Sprint
	if capital < startCapital {
		capColor = display.Red.Sprint
	}
	fmt.Printf("  %s  %s\n",
		display.Dim.Sprint("Final capital    :"),
		capColor(formatINR(capital)))

	pnlColor2 := display.BoldGreen.Sprintf
	if totalPnL < 0 {
		pnlColor2 = display.Red.Sprintf
	}
	fmt.Printf("  %s  %s  %s\n",
		display.Dim.Sprint("Total P&L        :"),
		pnlColor2("%+.2f", totalPnL),
		display.Dim.Sprintf("(%+.2f%%)", totalPct))

	if !first {
		b := results[bestIdx]
		w := results[worstIdx]
		fmt.Printf("  %s  %s #%d  %s\n",
			display.Dim.Sprint("Best trade       :"),
			display.Green.Sprint(b.Symbol),
			bestIdx+1,
			display.Green.Sprintf("%+.2f", bestPnL))
		fmt.Printf("  %s  %s #%d  %s\n",
			display.Dim.Sprint("Worst trade      :"),
			display.Red.Sprint(w.Symbol),
			worstIdx+1,
			display.Red.Sprintf("%+.2f", worstPnL))
	}
	fmt.Printf("  %s\n\n", sep)

	// Also print the standard strategy summary.
	sum := backtest.Compute(results)
	printSummary(sum, fromLabel, toLabel)
}

// journeyExitVerb returns a colored verb describing how the trade closed.
func journeyExitVerb(o backtest.Outcome) string {
	switch o {
	case backtest.OutcomeWin:
		return display.BoldGreen.Sprint("✅ Target hit")
	case backtest.OutcomeLoss:
		return display.Red.Sprint("❌ SL hit    ")
	case backtest.OutcomeTrailStop:
		return display.Cyan.Sprint("📈 Trail stop")
	default:
		return display.Yellow.Sprint("⏱ Timeout   ")
	}
}

// formatINR formats a float as Indian rupees with the Indian numbering system
// (e.g. 1,00,000.00 — lakh grouping, not Western thousands).
func formatINR(v float64) string {
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	paise := int64(math.Round(v*100)) % 100
	whole := int64(math.Round(v*100)) / 100

	s := fmt.Sprintf("%d", whole)
	if len(s) <= 3 {
		return fmt.Sprintf("%s₹%s.%02d", sign, s, paise)
	}

	// Indian grouping: last 3 digits, then pairs of 2 reading right-to-left.
	result := s[len(s)-3:]
	s = s[:len(s)-3]
	for len(s) > 0 {
		if len(s) >= 2 {
			result = s[len(s)-2:] + "," + result
			s = s[:len(s)-2]
		} else {
			result = s + "," + result
			s = ""
		}
	}
	return fmt.Sprintf("%s₹%s.%02d", sign, result, paise)
}

// ── CSV export ────────────────────────────────────────────────────────────────

func writeCSV(path string, results []backtest.TradeResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	header := []string{
		"signal_date", "entry_date", "exit_date",
		"symbol", "entry", "sl", "target", "exit_price",
		"score", "atr", "trend",
		"outcome", "actual_rr", "hold_days",
	}
	if err := w.Write(header); err != nil {
		return err
	}

	for _, r := range results {
		row := []string{
			r.SignalDate.Format("2006-01-02"),
			r.EntryDate.Format("2006-01-02"),
			r.ExitDate.Format("2006-01-02"),
			r.Symbol,
			fmt.Sprintf("%.4f", r.Entry),
			fmt.Sprintf("%.4f", r.SL),
			fmt.Sprintf("%.4f", r.Target),
			fmt.Sprintf("%.4f", r.ExitPrice),
			fmt.Sprintf("%.2f", r.Score),
			fmt.Sprintf("%.4f", r.ATR),
			r.Trend,
			string(r.Outcome),
			fmt.Sprintf("%.4f", r.ActualRR),
			fmt.Sprintf("%d", r.HoldDays),
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return w.Error()
}
