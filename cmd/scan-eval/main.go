// Command scan-eval scores the forward performance of a past day's live-scan
// signals — the honest "where are they now, net of costs" report.
//
// For a given signal date it reads every distinct symbol that appeared in the
// scan_results table that day (the live-scan log), then for each one:
//
//   - enters at the NEXT trading session's open (you can't act before then),
//   - places a protective stop from the swing scanner's setup as of the signal
//     day (falling back to an ATR stop when the EOD scanner doesn't reproduce
//     the intraday signal),
//   - manages the trade with the validated EMA-recross exit (+ optional time
//     cap), marking still-running trades to the latest close, and
//   - charges round-trip costs and per-leg slippage.
//
// It prints a per-trade table and an aggregate, compared against NIFTY over the
// same window. Because exits take time to mature, re-running the same date as
// more candles arrive sharpens the result — open trades close out over time.
//
// Usage:
//
//	go run ./cmd/scan-eval --date 2026-06-01
//	go run ./cmd/scan-eval --date 2026-06-01 --min-score 80 --max-hold 20 --csv out.csv
package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	_ "github.com/lib/pq"
	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/internal/display"
	"github.com/sahiltyagi27/stock-market-analysis/internal/kite"
	"github.com/sahiltyagi27/stock-market-analysis/internal/scanner"
	"github.com/sahiltyagi27/stock-market-analysis/internal/signaleval"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

var istLoc *time.Location

func ist() *time.Location {
	if istLoc == nil {
		l, err := time.LoadLocation("Asia/Kolkata")
		if err != nil {
			l = time.FixedZone("IST", 5*3600+1800)
		}
		istLoc = l
	}
	return istLoc
}

// istDay returns the IST calendar date of a candle/scan timestamp.
func istDay(t time.Time) string { return t.In(ist()).Format("2006-01-02") }

func main() {
	dateStr := flag.String("date", "", "signal date to evaluate, YYYY-MM-DD (IST); required")
	minScore := flag.Float64("min-score", 0, "only evaluate signals with score >= this (0 = all)")
	minStreak := flag.Int("min-streak", 0, "only evaluate signals seen at least this many consecutive scans (0 = all)")
	costPct := flag.Float64("cost-pct", 0.25, "round-trip transaction cost %% of notional")
	slippagePct := flag.Float64("slippage-pct", 0.20, "adverse fill haircut %% per leg")
	stopATRMult := flag.Float64("stop-atr-mult", 2.0, "fallback stop = signal-day close − this × ATR when the EOD scanner doesn't reproduce the signal")
	atrPeriod := flag.Int("atr-period", 14, "ATR period for the fallback stop")
	maxHold := flag.Int("max-hold", 0, "force-close after this many candles (0 = hold until stop/recross)")
	minRR := flag.Float64("min-rr", 2.0, "swing scanner min R/R when reconstructing the stop")
	benchSymbol := flag.String("benchmark", kite.Nifty50Symbol, "benchmark DB symbol for the comparison")
	csvPath := flag.String("csv", "", "write per-trade rows to this CSV file")
	flag.Parse()

	if *dateStr == "" {
		log.Fatal("--date is required (YYYY-MM-DD, IST)")
	}
	if _, err := time.Parse("2006-01-02", *dateStr); err != nil {
		log.Fatalf("--date: invalid %q, want YYYY-MM-DD", *dateStr)
	}

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

	srs, err := store.NewScanResultStore(db)
	if err != nil {
		log.Fatalf("scan_results: %v", err)
	}
	cs := store.NewCandleStore(db)

	// Bound the IST signal day in UTC for the scan_results query.
	dayStart, _ := time.ParseInLocation("2006-01-02", *dateStr, ist())
	dayEnd := dayStart.AddDate(0, 0, 1)
	rows, err := srs.Query(ctx, store.ScanResultFilter{
		From: &dayStart, To: &dayEnd, MinScore: *minScore, MinStreak: *minStreak, Limit: 100000,
	})
	if err != nil {
		log.Fatalf("query scan_results: %v", err)
	}
	if len(rows) == 0 {
		fmt.Printf("No scan_results logged for %s (IST). Was live-scan running that day?\n", *dateStr)
		return
	}

	// Earliest appearance per symbol (the moment the signal first showed).
	first := map[string]store.ScanResultRow{}
	for _, r := range rows {
		cur, ok := first[r.Symbol]
		if !ok || r.ScannedAt.Before(cur.ScannedAt) {
			first[r.Symbol] = r
		}
	}
	symbols := make([]string, 0, len(first))
	for s := range first {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)

	opts := signaleval.Options{
		CostPct: *costPct, SlippagePct: *slippagePct, MaxHoldDays: *maxHold,
	}

	var trades []signaleval.Trade
	var fired, skipped int
	for _, sym := range symbols {
		cc, err := cs.GetCandles(ctx, sym, store.CandleFilter{})
		if err != nil || len(cc) < 210 {
			skipped++
			continue
		}
		entryIdx := nextSessionIdx(cc, *dateStr)
		if entryIdx <= 0 {
			skipped++ // no session after the signal day yet
			continue
		}
		sl, didFire := reconstructStop(sym, cc, *dateStr, *minRR, *stopATRMult, *atrPeriod)
		if didFire {
			fired++
		}
		if sl <= 0 || sl >= cc[entryIdx].Open {
			skipped++
			continue
		}
		trades = append(trades, signaleval.Evaluate(cc, entryIdx, sl, opts))
	}
	if len(trades) == 0 {
		fmt.Printf("No evaluable trades for %s (need a session after the signal day).\n", *dateStr)
		return
	}

	sort.SliceStable(trades, func(i, j int) bool { return trades[i].NetPct > trades[j].NetPct })
	bench := benchmarkReturn(ctx, cs, *benchSymbol, *dateStr)
	printReport(trades, *dateStr, bench, *benchSymbol, fired, skipped, opts)

	if *csvPath != "" {
		if err := writeCSV(*csvPath, trades); err != nil {
			log.Printf("warn: CSV write failed: %v", err)
		} else {
			log.Printf("CSV written to %s (%d rows)", *csvPath, len(trades))
		}
	}
}

// nextSessionIdx returns the index of the first candle whose IST day is strictly
// after dateStr (the entry session). Returns -1 when none exists yet.
func nextSessionIdx(cc []models.Candle, dateStr string) int {
	for i, c := range cc {
		if istDay(c.Timestamp) > dateStr {
			return i
		}
	}
	return -1
}

// reconstructStop derives the protective stop. It runs the swing scanner on the
// candles up to and including the signal day: if a setup reproduces on the EOD
// candle, its support-based stop is used (didFire=true). Otherwise it falls back
// to an ATR stop below the signal-day close.
func reconstructStop(sym string, cc []models.Candle, dateStr string, minRR, atrMult float64, atrPeriod int) (sl float64, didFire bool) {
	var upto []models.Candle
	for _, c := range cc {
		if istDay(c.Timestamp) <= dateStr {
			upto = append(upto, c)
		}
	}
	if len(upto) < 210 {
		return 0, false
	}
	sigs := scanner.Scan([]scanner.Input{{Symbol: sym, Candles: upto}}, scanner.Options{MinRR: minRR})
	if len(sigs) > 0 && sigs[0].Trade.StopLoss > 0 {
		return sigs[0].Trade.StopLoss, true
	}
	atr := analysis.ATR(upto, atrPeriod)
	if atr <= 0 {
		return 0, false
	}
	return upto[len(upto)-1].Close - atrMult*atr, false
}

// benchmarkReturn computes the benchmark's % move from the entry session open
// (first session after dateStr) to its latest close.
func benchmarkReturn(ctx context.Context, cs *store.CandleStore, symbol, dateStr string) float64 {
	bc, err := cs.GetCandles(ctx, kite.NormalizeSymbol(symbol), store.CandleFilter{})
	if err != nil || len(bc) == 0 {
		return 0
	}
	ei := nextSessionIdx(bc, dateStr)
	if ei < 0 {
		return 0
	}
	entry := bc[ei].Open
	if entry <= 0 {
		entry = bc[ei].Close
	}
	last := bc[len(bc)-1].Close
	if entry <= 0 {
		return 0
	}
	return (last/entry - 1) * 100
}

func printReport(trades []signaleval.Trade, dateStr string, bench float64, benchSym string, fired, skipped int, opts signaleval.Options) {
	s := signaleval.Summarize(trades)
	banner := fmt.Sprintf("━━━  Signal Forward-Performance  %s  ━━━", dateStr)
	fmt.Printf("\n%s\n", display.BoldCyan.Sprint(banner))
	fmt.Printf("  %s\n", display.Dim.Sprintf("entry: next-session open   exit: EMA7<EMA21 recross / stop%s   costs: %.2f%% + %.2f%%/leg slip",
		holdLabel(opts.MaxHoldDays), opts.CostPct, opts.SlippagePct))
	fmt.Printf("  %s\n", display.Dim.Sprintf("stops: %d/%d reproduced on the EOD scanner (rest use ATR fallback); %d symbols skipped",
		fired, len(trades), skipped))

	fmt.Printf("\n  %-13s %9s %9s %9s  %-8s %s\n",
		"SYMBOL", "Entry", "Exit", "Net%", "Status", "Hold")
	for _, t := range trades {
		fmt.Printf("  %s %9.2f %9.2f %s  %-8s %dd\n",
			display.BoldWhite.Sprintf("%-13s", t.Symbol),
			t.Entry, t.Exit, colorPct(t.NetPct, 8), statusLabel(t.Status), t.HoldDays)
	}

	sep := display.Dim.Sprint("────────────────────────────────────────────────────────────")
	fmt.Printf("\n  %s\n", sep)
	fmt.Printf("  %s  %d   %s %d   %s %+.2f%%   %s %+.2f%%\n",
		display.Dim.Sprint("Trades:"), s.Trades,
		display.Dim.Sprint("Wins:"), s.Wins,
		display.Dim.Sprint("avg net"), s.AvgNetPct,
		display.Dim.Sprint("median"), s.MedianNetPct)
	fmt.Printf("  %s  %d stopped / %d recross / %d timeout / %d open\n",
		display.Dim.Sprint("Exits :"), s.Stopped, s.Recross, s.Timeout, s.Open)
	if s.ClosedTrades > 0 {
		fmt.Printf("  %s  %d trades, avg net %s  %s\n",
			display.Dim.Sprint("Closed:"), s.ClosedTrades, colorPct(s.ClosedAvgNetPct, 0),
			display.Dim.Sprint("(the mature subset — most trustworthy)"))
	}
	excess := s.AvgNetPct - bench
	fmt.Printf("  %s  %s %+.2f%%   %s %s  →  %s\n",
		display.Dim.Sprint("vs mkt:"),
		display.Dim.Sprintf("%s", benchSym), bench,
		display.Dim.Sprint("excess"), colorPct(excess, 0),
		excessVerdict(excess, s.Open, s.Trades))
	fmt.Printf("  %s\n", sep)
	if s.Open > 0 {
		fmt.Printf("  %s\n", display.Yellow.Sprintf("⏳ %d/%d trades still open — re-run this date as more candles arrive to mature the result.",
			s.Open, s.Trades))
	}
	fmt.Println()
}

func holdLabel(maxHold int) string {
	if maxHold > 0 {
		return fmt.Sprintf(" / %dd cap", maxHold)
	}
	return ""
}

func excessVerdict(excess float64, open, total int) string {
	tag := ""
	if open*2 >= total {
		tag = display.Dim.Sprint(" (immature — mostly open)")
	}
	if excess > 0 {
		return display.Green.Sprintf("beat market by %+.2f%%", excess) + tag
	}
	return display.Red.Sprintf("lagged market by %.2f%%", excess) + tag
}

func statusLabel(s signaleval.Status) string {
	switch s {
	case signaleval.StatusStop:
		return display.Red.Sprint("STOP")
	case signaleval.StatusRecross:
		return display.Cyan.Sprint("RECROSS")
	case signaleval.StatusTimeout:
		return display.Yellow.Sprint("TIMEOUT")
	default:
		return display.Dim.Sprint("OPEN")
	}
}

func colorPct(v float64, width int) string {
	format := "%+.2f%%"
	if width > 0 {
		format = fmt.Sprintf("%%+%d.2f%%%%", width)
	}
	if v >= 0 {
		return display.Green.Sprintf(format, v)
	}
	return display.Red.Sprintf(format, v)
}

func writeCSV(path string, trades []signaleval.Trade) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"symbol", "entry_date", "exit_date", "entry", "sl", "exit", "status", "hold_days", "net_pct"}); err != nil {
		return err
	}
	for _, t := range trades {
		if err := w.Write([]string{
			t.Symbol, t.EntryDate, t.ExitDate,
			fmt.Sprintf("%.4f", t.Entry), fmt.Sprintf("%.4f", t.SL), fmt.Sprintf("%.4f", t.Exit),
			string(t.Status), fmt.Sprintf("%d", t.HoldDays), fmt.Sprintf("%.4f", t.NetPct),
		}); err != nil {
			return err
		}
	}
	return w.Error()
}
