// Command live-scan subscribes to all watchlist symbols via the Kite WebSocket
// feed (full mode) and runs the scanner engine every 2 minutes during NSE
// market hours (09:15–15:30 IST, Mon–Fri), automatically skipping public
// holidays.
//
// Each scan run merges 2 years of historical candles from PostgreSQL with the
// current live tick as today's candle, then ranks signals exactly as the
// offline scanner does — EMA trend filter, zone detection, R/R grading, score.
//
// Relative strength vs NIFTY 50 is computed for every signal and shown in the
// output. Signals new to this scan are marked [NEW]; persistent signals show a
// streak counter (×2, ×3, …). Every scan run is written to the scan_results
// table so it can be reviewed and back-tested later.
//
// Usage:
//
//	go run ./cmd/live-scan
//	go run ./cmd/live-scan --top 10 --interval 2m --min-rr 2.0
//
// Required environment variables:
//
//	KITE_API_KEY
//	KITE_ACCESS_TOKEN   (refresh daily via cmd/kite-token)
//
// Optional flags:
//
//	--symbols    path to watchlist file     (default: config/symbols.txt)
//	--top        signals to print per run   (default: 10)
//	--mode       swing, breakout, or all    (default: swing)
//	--min-rr     minimum risk/reward ratio  (default: 2.0)
//	--ema-margin minimum %% gap above EMA200 (default: 1.0, 0 = disabled)
//	--min-volume minimum avg daily volume   (default: 0, disabled)
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
//	--interval   scan interval              (default: 2m)
//	--period     historical candle window   (default: 2y)
//	--exchange   Kite exchange              (default: NSE)
//	--dev        disable market hours check (default: false)
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
	"github.com/sahiltyagi27/stock-market-analysis/internal/display"
	"github.com/sahiltyagi27/stock-market-analysis/internal/kite"
	"github.com/sahiltyagi27/stock-market-analysis/internal/scanner"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

var ist *time.Location

func init() {
	var err error
	ist, err = time.LoadLocation("Asia/Kolkata")
	if err != nil {
		log.Fatalf("load IST timezone: %v", err)
	}
}

// nseHolidays lists NSE equity-segment trading holidays keyed by IST date
// (format "2006-01-02").
//
// Fixed-date holidays are confirmed. Moveable feasts (marked with *) are
// best-effort estimates — verify against the official NSE holiday circular
// before each trading year:
//
//	https://www.nseindia.com/resources/exchange-communication-holidays
//
// Saturday/Sunday closures are handled separately by isMarketOpen and do
// not need to be listed here.
var nseHolidays = map[string]string{
	// 2026 — fixed
	"2026-01-26": "Republic Day",
	"2026-04-03": "Good Friday", // Easter Apr 5 → GF Apr 3
	"2026-04-14": "Dr. Ambedkar Jayanti",
	"2026-05-01": "Maharashtra Day",
	"2026-10-02": "Gandhi Jayanti",
	"2026-12-25": "Christmas",
	// 2026 — moveable (*)
	"2026-02-26": "Mahashivratri",
	"2026-03-04": "Holi",
	"2026-10-20": "Dussehra",
	"2026-11-09": "Diwali (Laxmi Puja)",
	"2026-11-10": "Diwali (Balipratipada)",
	"2026-11-25": "Guru Nanak Jayanti",
}

// niftyToken is the Kite instrument token for the NIFTY 50 index (NSE).
// We subscribe to it alongside the equity watchlist so we can compute each
// signal's relative strength vs the broad market.
const (
	niftyToken  = uint32(256265)
	niftySymbol = "NIFTY 50"
)

func main() {
	symbolsFile := flag.String("symbols", "config/symbols.txt", "path to watchlist file")
	topN := flag.Int("top", 10, "signals to print per scan run")
	mode := flag.String("mode", "swing", "scanner mode: swing, breakout, or all")
	minRR := flag.Float64("min-rr", 2.0, "minimum risk/reward ratio")
	emaMargin := flag.Float64("ema-margin", 1.0, "minimum %% gap required between price and EMA200 (0 = disabled)")
	minVolume := flag.Int64("min-volume", 0, "minimum 20-day avg daily volume to qualify (0 = disabled)")
	minResistanceTouches := flag.Int("min-resistance-touches", 2, "minimum touches required for a resistance zone to qualify (1 = allow all)")
	alertScore           := flag.Float64("alert-score", 85, "highlight signals at or above this score with ⚡ (0 = disabled)")
	retentionDays        := flag.Int("retention-days", 30, "delete scan_results older than this many days on startup (0 = keep forever)")
	minCandles           := flag.Int("min-candles", 200, "minimum candles required per symbol before analysis (0 = use default 200)")
	atrPeriod            := flag.Int("atr-period", 14, "ATR period for volatility-based SL sizing (negative = use fixed SL buffer)")
	atrMultiplier        := flag.Float64("atr-multiplier", 1.5, "ATR multiplier for SL distance: SL = support.Low − multiplier × ATR")
	maxEMA10Extension := flag.Float64("max-ema10-extension", 8.0, "maximum %% above EMA10 before filtering as extended (<0 disables)")
	maxEMA50Extension := flag.Float64("max-ema50-extension", 15.0, "maximum %% above EMA50 before filtering as extended (<0 disables)")
	maxSupportExtension := flag.Float64("max-support-extension", 5.0, "maximum %% above support high before filtering as extended (<0 disables)")
	maxMove10D := flag.Float64("max-10d-move", 12.0, "maximum 10-candle %% move before filtering as extended (<0 disables)")
	maxBreakoutDistance  := flag.Float64("max-breakout-distance", 3.0, "maximum %% below resistance for breakout watch candidates (<0 disables)")
	maxRiskPct           := flag.Float64("max-risk-pct", 8.0, "maximum SL distance as %% of entry price (<0 disables)")
	minRiskPct           := flag.Float64("min-risk-pct", 1.5, "minimum SL distance as %% of entry price (<0 disables)")
	allowBearishCandle   := flag.Bool("allow-bearish-candle", false, "allow bearish signal candles (soft −5 penalty only)")
	ema200SlopePeriod    := flag.Int("ema200-slope-period", 20, "candles to look back for EMA200 slope filter (≤0 disables)")
	interval := flag.Duration("interval", 2*time.Minute, "scan interval (e.g. 2m, 30s)")
	period := flag.String("period", "2y", "historical candle window for EMA/zone computation")
	exchange := flag.String("exchange", "NSE", "Kite exchange")
	dev := flag.Bool("dev", false, "disable market hours check (useful for testing)")
	flag.Parse()
	if *mode != "swing" && *mode != "breakout" && *mode != "all" {
		log.Fatalf("invalid --mode %q: use swing, breakout, or all", *mode)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.KiteAPIKey == "" || cfg.KiteAccessToken == "" {
		log.Fatal("KITE_API_KEY and KITE_ACCESS_TOKEN are required (run cmd/kite-token to refresh)")
	}

	// ── Symbols ───────────────────────────────────────────────────────────────
	symbols, err := config.LoadSymbols(*symbolsFile)
	if err != nil {
		log.Fatalf("symbols: %v", err)
	}
	log.Printf("loaded %d symbols from %s", len(symbols), *symbolsFile)

	// ── DB: historical candle cache + scan result store ───────────────────────
	db, err := sql.Open("postgres", cfg.DSN())
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}
	candleStore := store.NewCandleStore(db)

	resultStore, err := store.NewScanResultStore(db)
	if err != nil {
		// Non-fatal: live scan still works, we just won't persist results.
		log.Printf("warn: scan result store unavailable: %v — scan history will not be recorded", err)
		resultStore = nil
	}

	// Purge old scan results on startup so the table doesn't grow forever.
	// Candles are kept — they're the core dataset. Only scan_results are pruned.
	if resultStore != nil && *retentionDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -*retentionDays)
		if n, err := resultStore.PurgeOlderThan(ctx, cutoff); err != nil {
			log.Printf("warn: scan_results purge failed: %v", err)
		} else if n > 0 {
			log.Printf("purged %d scan_results older than %d days", n, *retentionDays)
		}
	}

	from, err := parsePeriod(*period, time.Now())
	if err != nil {
		log.Fatalf("period: %v", err)
	}

	log.Printf("loading historical candles from DB (window: %s)…", *period)
	historyCache := make(map[string][]models.Candle, len(symbols))
	for _, rawSym := range symbols {
		sym := kite.NormalizeSymbol(rawSym)
		candles, err := candleStore.GetCandles(ctx, sym, store.CandleFilter{From: &from})
		if err != nil {
			log.Printf("  warn: DB read failed for %s: %v", sym, err)
			continue
		}
		if len(candles) > 0 {
			historyCache[sym] = candles
		}
	}
	log.Printf("cached history for %d/%d symbols", len(historyCache), len(symbols))

	// ── Kite: instrument token map ────────────────────────────────────────────
	kiteClient := kite.NewClient(cfg.KiteBaseURL, cfg.KiteAPIKey, cfg.KiteAccessToken)
	instruments, err := kiteClient.Instruments(ctx, *exchange)
	if err != nil {
		log.Fatalf("kite instruments: %v", err)
	}
	log.Printf("fetched %d %s instruments from Kite", len(instruments), *exchange)

	// tokenSymbol and tokens include the NIFTY 50 index so we can compute
	// relative strength for each signal (IsTradable=false ticks are now stored
	// by WSClient since ws.go no longer filters by IsTradable).
	tokenSymbol := make(map[uint32]string, len(symbols)+1)
	symbolToken := make(map[string]uint32, len(symbols)+1)
	tokens := []uint32{niftyToken}
	tokenSymbol[niftyToken] = niftySymbol

	for _, rawSym := range symbols {
		sym := kite.NormalizeSymbol(rawSym)
		inst, ok := kite.FindEquityInstrument(instruments, *exchange, sym)
		if !ok {
			continue
		}
		tok := uint32(inst.InstrumentToken)
		tokenSymbol[tok] = sym
		symbolToken[sym] = tok
		tokens = append(tokens, tok)
	}
	log.Printf("mapped %d/%d symbols to instrument tokens (+ NIFTY 50 index)", len(tokens)-1, len(symbols))
	if len(tokens) <= 1 { // only Nifty token, no equities
		log.Fatal("no equity instrument tokens resolved — check KITE_ACCESS_TOKEN and exchange")
	}

	// ── WebSocket ─────────────────────────────────────────────────────────────
	ws := kite.NewWSClient(cfg.KiteAPIKey, cfg.KiteAccessToken, tokenSymbol)
	go func() {
		if err := ws.Run(ctx, tokens); err != nil {
			log.Printf("ws error: %v", err)
		}
	}()

	// Allow time for the initial batch of ticks to arrive before first scan.
	log.Printf("connecting to Kite WebSocket — waiting 5s for initial ticks…")
	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		return
	}

	// ── Scan loop ─────────────────────────────────────────────────────────────
	log.Printf("live scan ready — interval %s | top %d | min R/R %.1f", *interval, *topN, *minRR)
	if *dev {
		log.Printf("⚠  --dev mode: market hours check disabled")
	}

	scanOpts := scanner.Options{
		MinRR:                  *minRR,
		EMAMarginPct:           *emaMargin,
		MinAvgVolume:           *minVolume,
		MinCandles:             *minCandles,
		MaxEMA10ExtensionPct:   *maxEMA10Extension,
		MaxEMA50ExtensionPct:   *maxEMA50Extension,
		MaxSupportExtensionPct: *maxSupportExtension,
		MaxMove10DPct:          *maxMove10D,
		MaxBreakoutDistancePct: *maxBreakoutDistance,
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

	state := newScanState()

	// Restore today's scan state from DB so streak counts and [NEW] detection
	// survive a process restart. State from a previous day is intentionally
	// ignored — streaks reset cleanly at the start of each session.
	if resultStore != nil {
		if snap, err := resultStore.LatestTodayScanState(ctx, ist); err != nil {
			log.Printf("warn: could not restore scan state from DB: %v", err)
		} else if snap != nil {
			state.prevSymbols = snap.PrevSymbols
			state.streaks     = snap.Streaks
			state.initialized = true
			maxStreak := 0
			for _, n := range snap.Streaks {
				if n > maxStreak {
					maxStreak = n
				}
			}
			log.Printf("restored scan state from DB — %d symbols, max streak ×%d",
				len(snap.PrevSymbols), maxStreak)
		} else {
			log.Printf("no scan history for today — starting fresh")
		}
	}

	// Run immediately so the first results appear right after connect,
	// not after waiting a full interval.
	now := time.Now()
	if *dev || isMarketOpen(now) {
		runScan(ctx, now, ws, historyCache, symbolToken, scanOpts, *topN, *mode, *alertScore, state, resultStore)
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down")
			return
		case t := <-ticker.C:
			if !*dev && !isMarketOpen(t) {
				local := t.In(ist)
				if name, ok := nseHolidays[local.Format("2006-01-02")]; ok {
					log.Printf("[%s IST] NSE holiday: %s — skipping",
						local.Format("15:04:05"), name)
				} else {
					log.Printf("[%s IST] outside market hours (09:15–15:30 Mon–Fri) — skipping",
						local.Format("15:04:05"))
				}
				continue
			}
			runScan(ctx, t, ws, historyCache, symbolToken, scanOpts, *topN, *mode, *alertScore, state, resultStore)
		}
	}
}

// ── Scan state ────────────────────────────────────────────────────────────────

// scanState tracks which symbols appeared in the previous scan so new
// arrivals can be flagged [NEW] and persistent signals show a streak count.
type scanState struct {
	initialized bool
	prevSymbols map[string]bool
	streaks     map[string]int
}

func newScanState() *scanState {
	return &scanState{
		prevSymbols: make(map[string]bool),
		streaks:     make(map[string]int),
	}
}

// advance updates state with the current signal set and returns the symbols
// that are new (present now but absent last scan).
//
// On the very first call (before any scan has completed) no symbols are
// marked as new — prevSymbols is empty and marking everything [NEW] would
// be noisy without providing signal.
func (s *scanState) advance(signals []scanner.StockSignal) map[string]bool {
	current := make(map[string]bool, len(signals))
	newSymbols := make(map[string]bool)

	for _, sig := range signals {
		sym := sig.Symbol
		current[sym] = true
		if s.initialized && !s.prevSymbols[sym] {
			// Appeared this scan but not last — genuinely new signal.
			newSymbols[sym] = true
			s.streaks[sym] = 1
		} else {
			// Present on first scan (streak starts at 1) or consecutive scan
			// (streak increments). The zero-value for a missing map key means
			// 0++ = 1 on first appearance.
			s.streaks[sym]++
		}
	}

	// Remove streak entries for symbols that dropped out.
	for sym := range s.streaks {
		if !current[sym] {
			delete(s.streaks, sym)
		}
	}

	s.prevSymbols = current
	s.initialized = true
	return newSymbols
}

// ── Scan helpers ──────────────────────────────────────────────────────────────

// runScan builds scanner inputs from live ticks + historical candles, runs the
// full scanner pipeline, updates scan state, prints results, and persists
// every signal to the scan_results table.
func runScan(
	ctx context.Context,
	at time.Time,
	ws *kite.WSClient,
	history map[string][]models.Candle,
	symbolToken map[string]uint32,
	opts scanner.Options,
	topN int,
	mode string,
	alertScore float64,
	state *scanState,
	resultStore *store.ScanResultStore,
) {
	volFrac := sessionElapsedFraction(at)
	var inputs []scanner.Input
	noTick := 0

	for sym, candles := range history {
		tok, ok := symbolToken[sym]
		if !ok {
			continue
		}
		tick, ok := ws.LatestTick(tok)
		if !ok || tick.LastPrice <= 0 {
			noTick++
			continue
		}
		inputs = append(inputs, buildInput(sym, candles, tick, volFrac))
	}

	var signals []scanner.StockSignal
	var breakouts []scanner.BreakoutSignal
	if mode == "swing" || mode == "all" {
		signals, _ = scanner.ScanWithErrors(inputs, opts)
	}
	if mode == "breakout" || mode == "all" {
		breakouts, _ = scanner.ScanBreakouts(inputs, opts)
	}

	// Compute relative strength vs NIFTY 50 for every signal.
	rsMap, niftyPct := computeRS(ws, symbolToken, signals)

	newSymbols := state.advance(signals)
	if mode == "swing" || mode == "all" {
		printScan(at, signals, topN, len(history), noTick, volFrac, newSymbols, state.streaks, rsMap, niftyPct, alertScore)
	}
	if mode == "breakout" || mode == "all" {
		breakoutRSMap, breakoutNiftyPct := computeBreakoutRS(ws, symbolToken, breakouts)
		printBreakoutScan(at, breakouts, topN, len(history), noTick, volFrac, breakoutRSMap, breakoutNiftyPct)
	}

	// Persist scan results asynchronously so a slow DB write can't delay the
	// next scan tick.
	if resultStore != nil && len(signals) > 0 {
		rows := buildScanResultRows(at, signals, newSymbols, state.streaks, rsMap)
		go func() {
			if err := resultStore.Save(ctx, rows); err != nil {
				log.Printf("warn: save scan results: %v", err)
			}
		}()
	}
}

// computeRS returns a map of symbol → relative-strength-vs-NIFTY (percentage
// points) and the NIFTY's own % change from open.
//
// Returns (nil, 0) when the NIFTY 50 tick is not yet available or its open
// price is zero (e.g. before market open or during the first seconds of
// trading).
func computeRS(
	ws *kite.WSClient,
	symbolToken map[string]uint32,
	signals []scanner.StockSignal,
) (rsMap map[string]float64, niftyPct float64) {
	niftyTick, ok := ws.LatestTick(niftyToken)
	if !ok || niftyTick.Open <= 0 || niftyTick.LastPrice <= 0 {
		return nil, 0
	}
	niftyPct = (niftyTick.LastPrice - niftyTick.Open) / niftyTick.Open * 100

	rsMap = make(map[string]float64, len(signals))
	for _, sig := range signals {
		tok, ok := symbolToken[sig.Symbol]
		if !ok {
			continue
		}
		tick, ok := ws.LatestTick(tok)
		if !ok || tick.Open <= 0 {
			continue
		}
		stockPct := (tick.LastPrice - tick.Open) / tick.Open * 100
		rsMap[sig.Symbol] = stockPct - niftyPct
	}
	return rsMap, niftyPct
}

func computeBreakoutRS(
	ws *kite.WSClient,
	symbolToken map[string]uint32,
	signals []scanner.BreakoutSignal,
) (rsMap map[string]float64, niftyPct float64) {
	niftyTick, ok := ws.LatestTick(niftyToken)
	if !ok || niftyTick.Open <= 0 || niftyTick.LastPrice <= 0 {
		return nil, 0
	}
	niftyPct = (niftyTick.LastPrice - niftyTick.Open) / niftyTick.Open * 100

	rsMap = make(map[string]float64, len(signals))
	for _, sig := range signals {
		tok, ok := symbolToken[sig.Symbol]
		if !ok {
			continue
		}
		tick, ok := ws.LatestTick(tok)
		if !ok || tick.Open <= 0 {
			continue
		}
		stockPct := (tick.LastPrice - tick.Open) / tick.Open * 100
		rsMap[sig.Symbol] = stockPct - niftyPct
	}
	return rsMap, niftyPct
}

// buildScanResultRows converts the scanner output into rows ready for DB insert.
func buildScanResultRows(
	at time.Time,
	signals []scanner.StockSignal,
	newSymbols map[string]bool,
	streaks map[string]int,
	rsMap map[string]float64,
) []store.ScanResultRow {
	rows := make([]store.ScanResultRow, 0, len(signals))
	for _, sig := range signals {
		var rs *float64
		if rsMap != nil {
			if v, ok := rsMap[sig.Symbol]; ok {
				v := v
				rs = &v
			}
		}
		rows = append(rows, store.ScanResultRow{
			ScannedAt:   at,
			Symbol:      sig.Symbol,
			Price:       sig.Price,
			Score:       sig.Score,
			Trend:       string(sig.Trend),
			RR:          sig.Trade.RiskReward,
			EMA50:       sig.EMA.EMA50,
			EMA200:      sig.EMA.EMA200,
			RelStrength: rs,
			IsNew:       newSymbols[sig.Symbol],
			Streak:      streaks[sig.Symbol],
		})
	}
	return rows
}

// buildInput creates a scanner.Input by appending (or replacing) a synthetic
// "live candle" built from the current tick onto the historical candle slice.
//
// If the latest historical candle is from today (same IST calendar day as the
// tick), it is replaced so the scanner sees a continuously updated intraday
// candle. Otherwise the live candle is appended as a new day.
//
// volFrac is the fraction of the NSE session elapsed (from sessionElapsedFraction).
// The intraday volume is divided by volFrac to project it to a full-day equivalent,
// allowing fair comparison against the historical full-day rolling average.
// e.g. at 12:15 (48% elapsed): 328k intraday → 683k projected full-day.
func buildInput(sym string, historical []models.Candle, tick kite.Tick, volFrac float64) scanner.Input {
	now := time.Now().UTC()
	open := tick.Open
	if open <= 0 {
		open = tick.LastPrice // fallback if intraday open not yet available
	}

	// Project intraday volume to a full-day equivalent for fair scoring.
	projVol := int64(float64(tick.Volume) / volFrac)

	live := models.Candle{
		Symbol:    sym,
		Timestamp: now,
		Open:      open,
		High:      tick.High,
		Low:       tick.Low,
		Close:     tick.LastPrice, // LTP as the "current price"
		Volume:    projVol,
	}

	merged := make([]models.Candle, len(historical))
	copy(merged, historical)

	if len(merged) > 0 && sameDay(merged[len(merged)-1].Timestamp, now) {
		merged[len(merged)-1] = live // replace today's candle with live data
	} else {
		merged = append(merged, live)
	}
	return scanner.Input{Symbol: sym, Candles: merged}
}

// printScan prints ranked signals to stdout in a compact terminal-friendly format.
//
//   - volFrac:    fraction of NSE session elapsed — labels volume as "est."
//   - newSymbols: symbols appearing for the first time since the last scan → [NEW]
//   - streaks:    consecutive-scan count per symbol → ×N shown when N > 1
//   - rsMap:      symbol → relative strength % vs NIFTY (nil if unavailable)
//   - niftyPct:   NIFTY 50's own % change from open (0 if rsMap is nil)
func printScan(
	at time.Time,
	signals []scanner.StockSignal,
	topN, total, noTick int,
	volFrac float64,
	newSymbols map[string]bool,
	streaks map[string]int,
	rsMap map[string]float64,
	niftyPct float64,
	alertScore float64,
) {
	stamp := at.In(ist).Format("02-Jan-2006  15:04:05")
	bannerText := fmt.Sprintf("━━━  Live Scan  %s IST  ━━━", stamp)
	fmt.Printf("\n%s\n", display.BoldCyan.Sprint(bannerText))

	top := topN
	if top > len(signals) {
		top = len(signals)
	}
	if top == 0 {
		fmt.Println("  No bullish setups found.")
	}

	pipe := display.Dim.Sprint("├")
	last := display.Dim.Sprint("└")

	for i, sig := range signals[:top] {
		// ⚡ alert when score crosses threshold, then [NEW] / ×N streak.
		tag := ""
		if alertScore > 0 && sig.Score >= alertScore {
			tag += "  " + display.BoldYellow.Sprint("⚡")
		}
		switch {
		case newSymbols[sig.Symbol]:
			tag += "  " + display.BoldGreen.Sprint("[NEW]")
		case streaks[sig.Symbol] > 1:
			tag += "  " + display.Cyan.Sprintf("×%d", streaks[sig.Symbol])
		}

		// Pad symbol and price before applying color so alignment is preserved.
		sym := display.BoldWhite.Sprint(fmt.Sprintf("%-14s", sig.Symbol))
		price := fmt.Sprintf("₹%-9.2f", sig.Price)
		score := display.TotalScore(sig.Score)

		fmt.Printf("\n  %s %s  %s  %s %s/100%s\n",
			display.Dim.Sprintf("%d.", i+1),
			sym, price,
			display.Dim.Sprint("Score:"),
			score, tag)

		// Score breakdown — each component colored by how full it is.
		volLabel := "actual"
		if volFrac < 1.0 {
			volLabel = "est."
		}
		fmt.Printf("     %s %s %s  %s %s  %s %s",
			pipe,
			display.Dim.Sprint("Trend:"), display.Component(sig.Breakdown.Trend, 40),
			display.Dim.Sprint("R/R:"), display.Component(sig.Breakdown.RR, 30),
			display.Dim.Sprint("Support:"), display.Component(sig.Breakdown.Support, 20))
		if sig.Breakdown.AvgVolume > 0 {
			fmt.Printf("  %s %s (%s %.0f vs avg %.0f = %.2fx)\n",
				display.Dim.Sprint("Volume:"),
				display.Component(sig.Breakdown.Volume, 10),
				display.Dim.Sprint(volLabel),
				sig.Breakdown.LastVolume, sig.Breakdown.AvgVolume, sig.Breakdown.VolumeRatio)
		} else {
			fmt.Printf("  %s %s\n",
				display.Dim.Sprint("Volume:"),
				display.Component(sig.Breakdown.Volume, 10))
		}
		// Bearish candle penalty.
		if sig.Breakdown.CandleDir < 0 {
			fmt.Printf("     %s %s %s\n",
				pipe,
				display.Dim.Sprint("Candle:"),
				display.Red.Sprintf("%.0f (bearish — close < open)", sig.Breakdown.CandleDir))
		}

		// Relative strength vs NIFTY 50.
		if rsMap != nil {
			if rs, ok := rsMap[sig.Symbol]; ok {
				fmt.Printf("     %s %s %s  %s %s\n",
					pipe,
					display.Dim.Sprint("RS vs NIFTY:"),
					display.Sign(rs, "%+.2f%%"),
					display.Dim.Sprint("(NIFTY:"),
					display.Sign(niftyPct, "%+.2f%%")+display.Dim.Sprint(")"))
			}
		}

		// Extension diagnostics — useful for spotting late entries after a rally.
		fmt.Printf("     %s %s EMA10 %s  EMA50 %s  Support %s  10D %s\n",
			pipe,
			display.Dim.Sprint("Extension:"),
			display.Sign(sig.Extension.FromEMA10Pct, "%+.1f%%"),
			display.Sign(sig.Extension.FromEMA50Pct, "%+.1f%%"),
			display.Sign(sig.Extension.FromSupportHighPct, "%+.1f%%"),
			formatMove10D(sig.Extension))

		// Trade direction + R/R quality.
		trend := fmt.Sprintf("%-8s", string(sig.Trend))
		fmt.Printf("     %s %s %s  %s %s %s\n",
			pipe,
			display.Dim.Sprint("Trend:"), display.Trend(trend),
			display.Dim.Sprint("R/R:"),
			display.RR(sig.Trade.RiskReward),
			display.Dim.Sprint("(")+display.Quality(string(sig.Trade.Quality))+display.Dim.Sprint(")"))

		// Entry / SL / Target — SL red, target green; show ATR when used.
		atrLabel := ""
		if sig.Trade.ATR > 0 {
			atrLabel = display.Dim.Sprintf("  (ATR14: %.2f)", sig.Trade.ATR)
		}
		fmt.Printf("     %s %s %-9.2f  %s %s  %s %s%s\n",
			pipe,
			display.Dim.Sprint("Entry:"), sig.Trade.Entry,
			display.Dim.Sprint("SL:"), display.Red.Sprintf("%-9.2f", sig.Trade.StopLoss),
			display.Dim.Sprint("Target:"), display.Green.Sprintf("%.2f", sig.Trade.Target),
			atrLabel)

		// Support / Resistance zones.
		fmt.Printf("     %s %s %.2f–%.2f (%s)\n",
			pipe,
			display.Dim.Sprint("Support:   "),
			sig.Support.Low, sig.Support.High,
			display.Dim.Sprintf("%d touches", sig.Support.Touches))
		fmt.Printf("     %s %s %.2f–%.2f (%s)\n",
			pipe,
			display.Dim.Sprint("Resistance:"),
			sig.Resistance.Low, sig.Resistance.High,
			display.Dim.Sprintf("%d touches", sig.Resistance.Touches))

		// Reasons.
		fmt.Printf("     %s %s\n", last, display.Dim.Sprint("Reasons:"))
		for _, r := range sig.Reasons {
			fmt.Printf("         %s %s\n", display.Cyan.Sprint("•"), display.Dim.Sprint(r))
		}
	}

	sep := display.Dim.Sprint(strings.Repeat("─", 54))
	fmt.Printf("\n  %s\n", sep)
	fmt.Printf("  %s\n",
		display.Dim.Sprintf("Scanned: %-4d  Signals: %-4d  No tick yet: %d",
			total, len(signals), noTick))
	if volFrac < 1.0 {
		fmt.Printf("  %s\n",
			display.Dim.Sprintf("* volume projected to full-day (%d%% of session elapsed)",
				int(volFrac*100)))
	}
	if rsMap != nil {
		fmt.Printf("  %s %s\n",
			display.Dim.Sprint("NIFTY 50:"),
			display.Sign(niftyPct, "%+.2f%%")+display.Dim.Sprint(" from open"))
	}
	fmt.Printf("%s\n", display.BoldCyan.Sprint(strings.Repeat("━", len(bannerText))))
}

func printBreakoutScan(
	at time.Time,
	signals []scanner.BreakoutSignal,
	topN, total, noTick int,
	volFrac float64,
	rsMap map[string]float64,
	niftyPct float64,
) {
	stamp := at.In(ist).Format("02-Jan-2006  15:04:05")
	bannerText := fmt.Sprintf("━━━  Breakout Watch  %s IST  ━━━", stamp)
	fmt.Printf("\n%s\n", display.BoldCyan.Sprint(bannerText))

	top := topN
	if top > len(signals) {
		top = len(signals)
	}
	if top == 0 {
		fmt.Println("  No breakout watch candidates found.")
	}

	pipe := display.Dim.Sprint("├")
	last := display.Dim.Sprint("└")
	volLabel := "actual"
	if volFrac < 1.0 {
		volLabel = "est."
	}

	for i, sig := range signals[:top] {
		sym := display.BoldWhite.Sprint(fmt.Sprintf("%-14s", sig.Symbol))
		price := fmt.Sprintf("₹%-9.2f", sig.Price)
		fmt.Printf("\n  %s %s %s  %s %s/100\n",
			display.Dim.Sprintf("%d.", i+1),
			sym, price,
			display.Dim.Sprint("Score:"),
			display.TotalScore(sig.Score))
		fmt.Printf("     %s %s %.2f–%.2f (%s)  %s %s below\n",
			pipe,
			display.Dim.Sprint("Resistance:"),
			sig.Resistance.Low, sig.Resistance.High,
			display.Dim.Sprintf("%d touches", sig.Resistance.Touches),
			display.Dim.Sprint("Distance:"),
			display.Cyan.Sprintf("%.2f%%", sig.DistanceToResistancePct))
		fmt.Printf("     %s %s %.2f  %s %.2f–%.2f\n",
			pipe,
			display.Dim.Sprint("Confirm above:"),
			sig.BreakoutPrice,
			display.Dim.Sprint("Support:"),
			sig.Support.Low, sig.Support.High)
		if rsMap != nil {
			if rs, ok := rsMap[sig.Symbol]; ok {
				fmt.Printf("     %s %s %s  %s %s\n",
					pipe,
					display.Dim.Sprint("RS vs NIFTY:"),
					display.Sign(rs, "%+.2f%%"),
					display.Dim.Sprint("(NIFTY:"),
					display.Sign(niftyPct, "%+.2f%%")+display.Dim.Sprint(")"))
			}
		}
		fmt.Printf("     %s %s EMA10 %s  EMA50 %s  Support %s  10D %s\n",
			pipe,
			display.Dim.Sprint("Extension:"),
			display.Sign(sig.Extension.FromEMA10Pct, "%+.1f%%"),
			display.Sign(sig.Extension.FromEMA50Pct, "%+.1f%%"),
			display.Sign(sig.Extension.FromSupportHighPct, "%+.1f%%"),
			formatMove10D(sig.Extension))
		if sig.Volume.AvgVolume > 0 {
			fmt.Printf("     %s %s %.2fx (%s %.0f vs avg %.0f)\n",
				pipe,
				display.Dim.Sprint("Volume:"),
				sig.Volume.VolumeRatio,
				display.Dim.Sprint(volLabel),
				sig.Volume.LastVolume, sig.Volume.AvgVolume)
		}
		fmt.Printf("     %s %s\n", last, display.Dim.Sprint("Watch for close above resistance with volume confirmation"))
	}

	sep := display.Dim.Sprint(strings.Repeat("─", 54))
	fmt.Printf("\n  %s\n", sep)
	fmt.Printf("  %s\n",
		display.Dim.Sprintf("Scanned: %-4d  Breakouts: %-4d  No tick yet: %d",
			total, len(signals), noTick))
	if volFrac < 1.0 {
		fmt.Printf("  %s\n",
			display.Dim.Sprintf("* volume projected to full-day (%d%% of session elapsed)",
				int(volFrac*100)))
	}
	if rsMap != nil {
		fmt.Printf("  %s %s\n",
			display.Dim.Sprint("NIFTY 50:"),
			display.Sign(niftyPct, "%+.2f%%")+display.Dim.Sprint(" from open"))
	}
	fmt.Printf("%s\n", display.BoldCyan.Sprint(strings.Repeat("━", len(bannerText))))
}

func formatMove10D(ext scanner.Extension) string {
	if !ext.HasMove10D {
		return display.Dim.Sprint("n/a")
	}
	return display.Sign(ext.Move10DPct, "%+.1f%%")
}

// ── Market hours helpers ───────────────────────────────────────────────────────

// sessionElapsedFraction returns the fraction of the NSE trading session
// (09:15–15:30 IST = 375 minutes) that has elapsed at time t.
//
// Clamped to a minimum of 30/375 ≈ 0.08 to avoid extreme scale factors in
// the first 30 minutes of trading when very few trades have occurred.
// Returns 1.0 outside session hours so volume is never inflated.
func sessionElapsedFraction(t time.Time) float64 {
	local := t.In(ist)
	y, m, d := local.Date()
	open := time.Date(y, m, d, 9, 15, 0, 0, ist)
	close_ := time.Date(y, m, d, 15, 30, 0, 0, ist)

	if local.Before(open) || local.After(close_) {
		return 1.0 // outside session: use volume as-is
	}

	const totalMins = 375.0
	const minMins = 30.0 // cap: don't project before 30 min of trading

	elapsed := local.Sub(open).Minutes()
	if elapsed < minMins {
		elapsed = minMins
	}
	return elapsed / totalMins
}

// isMarketOpen returns true if t falls within NSE trading hours:
// Monday–Friday, 09:15–15:30 IST, excluding public holidays.
func isMarketOpen(t time.Time) bool {
	local := t.In(ist)
	switch local.Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	if _, holiday := nseHolidays[local.Format("2006-01-02")]; holiday {
		return false
	}
	y, m, d := local.Date()
	open := time.Date(y, m, d, 9, 15, 0, 0, ist)
	close_ := time.Date(y, m, d, 15, 30, 0, 0, ist)
	return !local.Before(open) && !local.After(close_)
}

// sameDay reports whether two timestamps fall on the same IST calendar day.
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.In(ist).Date()
	by, bm, bd := b.In(ist).Date()
	return ay == by && am == bm && ad == bd
}

func parsePeriod(period string, from time.Time) (time.Time, error) {
	if len(period) < 2 {
		return time.Time{}, fmt.Errorf("invalid period %q: must be like 2y, 6m, 90d", period)
	}
	unit := period[len(period)-1]
	var n int
	if _, err := fmt.Sscanf(period[:len(period)-1], "%d", &n); err != nil || n <= 0 {
		return time.Time{}, fmt.Errorf("invalid period %q: number must be positive", period)
	}
	switch unit {
	case 'y', 'Y':
		return from.AddDate(-n, 0, 0), nil
	case 'm', 'M':
		return from.AddDate(0, -n, 0), nil
	case 'd', 'D':
		return from.AddDate(0, 0, -n), nil
	default:
		return time.Time{}, fmt.Errorf("invalid period unit %q: use y, m, or d", string(unit))
	}
}
