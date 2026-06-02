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
	"github.com/sahiltyagi27/stock-market-analysis/internal/scanner"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

func main() {
	symbolsFile := flag.String("symbols", "config/symbols.txt", "path to watchlist file")
	fromStr := flag.String("from", "", "start of signal-date window, YYYY-MM-DD (empty = no lower bound)")
	toStr := flag.String("to", "", "end of signal-date window, YYYY-MM-DD (empty = today)")
	mode := flag.String("mode", "swing", "scanner strategy: swing or crossover")
	minScore := flag.Float64("min-score", 0, "skip signals below this score (0 = all)")
	maxHold := flag.Int("max-hold", 20, "maximum candles to hold before timing out")
	workers := flag.Int("workers", 8, "parallel goroutines for simulation")
	topN      := flag.Int("top", 30, "trades to print when --capital is disabled (sorted by score)")
	outputCSV := flag.String("output", "", "write all trades to this CSV file (empty = no file)")
	capital   := flag.Float64("capital", 100000, "starting capital in INR for the P&L journey; 0 = show top-N by score instead")

	// Scanner flags — mirror live-scan / scan for identical filter behaviour.
	minRR := flag.Float64("min-rr", 2.0, "minimum risk/reward ratio")
	emaMargin := flag.Float64("ema-margin", 1.0, "minimum %% gap required above EMA200 (0 = disabled)")
	minVolume := flag.Int64("min-volume", 0, "minimum rolling avg daily volume (0 = disabled)")
	minResistanceTouches := flag.Int("min-resistance-touches", 2, "minimum touches for a resistance zone")
	minCandles := flag.Int("min-candles", 200, "minimum historical candles required before analysis")
	atrPeriod := flag.Int("atr-period", 14, "ATR period for volatility-based SL (negative = fixed buffer)")
	atrMultiplier := flag.Float64("atr-multiplier", 1.5, "ATR multiplier: SL = support.Low − multiplier × ATR")
	maxEMA10Extension  := flag.Float64("max-ema10-extension", 8.0, "max %% above EMA10 (<0 disables)")
	maxEMA50Extension  := flag.Float64("max-ema50-extension", 15.0, "max %% above EMA50 (<0 disables)")
	maxSupportExtension := flag.Float64("max-support-extension", 5.0, "max %% above support high (<0 disables)")
	maxMove10D          := flag.Float64("max-10d-move", 12.0, "max 10-candle %% move (<0 disables)")
	maxRiskPct         := flag.Float64("max-risk-pct", 8.0, "maximum SL distance as %% of entry price (<0 disables)")
	minRiskPct         := flag.Float64("min-risk-pct", 1.5, "minimum SL distance as %% of entry price (<0 disables)")
	allowBearishCandle := flag.Bool("allow-bearish-candle", false, "allow bearish signal candles (soft −5 penalty only)")
	ema200SlopePeriod  := flag.Int("ema200-slope-period", 20, "candles to look back for EMA200 slope filter (≤0 disables)")
	trailATRMult       := flag.Float64("trail-atr-mult", 1.5, "ATR multiplier for trailing stop: trailSL = highestHigh − mult×ATR (≤0 disables)")

	// Crossover-mode flags (only used when --mode crossover).
	coMaxAge     := flag.Int("co-max-age", 2, "[crossover] max candles since EMA7×21 crossover (default 2 = last 3 candles)")
	coMinRR      := flag.Float64("co-min-rr", 1.5, "[crossover] minimum risk/reward ratio")
	coMinCandles := flag.Int("co-min-candles", 50, "[crossover] minimum candles required before analysis")
	coVolWindow  := flag.Int("co-vol-window", 20, "[crossover] volume rolling-average window")
	coMinResTouches := flag.Int("co-min-resistance-touches", 1, "[crossover] minimum touches for a resistance zone target")
	coMinVolMult    := flag.Float64("co-min-vol-mult", 0, "[crossover] require today's volume ≥ this × the prev N-day avg (0 = disabled)")
	coVolMultWindow := flag.Int("co-vol-mult-window", 10, "[crossover] candles for the today's-volume average check")

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

	// ── Run backtest ──────────────────────────────────────────────────────────
	scanOpts := scanner.Options{
		MinRR:                  *minRR,
		EMAMarginPct:           *emaMargin,
		MinAvgVolume:           *minVolume,
		MinCandles:             *minCandles,
		MaxEMA10ExtensionPct:   *maxEMA10Extension,
		MaxEMA50ExtensionPct:   *maxEMA50Extension,
		MaxSupportExtensionPct: *maxSupportExtension,
		MaxMove10DPct:          *maxMove10D,
		ATRPeriod:              *atrPeriod,
		ATRMultiplier:          *atrMultiplier,
		MaxRiskPct:             *maxRiskPct,
		MinRiskPct:             *minRiskPct,
		AllowBearishCandle:     *allowBearishCandle,
		EMA200SlopePeriod:      *ema200SlopePeriod,
		ZoneOpts: analysis.ZoneOptions{
			MinResistanceTouches: *minResistanceTouches,
		},
	}

	if *mode != "swing" && *mode != "crossover" {
		log.Fatalf("--mode must be swing or crossover, got %q", *mode)
	}

	coOpts := crossover.Options{
		MaxCrossoverAge:       *coMaxAge,
		MinRR:                 *coMinRR,
		MinCandles:            *coMinCandles,
		VolumeWindow:          *coVolWindow,
		MinCurrentVolMultiple: *coMinVolMult,
		CurrentVolWindow:      *coVolMultWindow,
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
	whole := int64(math.Round(v * 100)) / 100

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
