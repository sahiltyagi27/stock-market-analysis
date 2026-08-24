// Command earnings-reaction studies how NSE stock prices react to quarterly
// results declarations: for each stored EarningsEvent it computes the price
// reaction over the calendar week before and the calendar week after the
// report date (report date − 7 calendar days through report date + 7
// calendar days, whatever trading days fall in that span) from the existing
// candles table.
//
// Seed the reference data (15 stocks' Q1 FY27 events, sourced manually from
// company press releases / financial news since NSE/BSE's own filing APIs
// are not reachable from this environment):
//
//	go run ./cmd/earnings-reaction --seed
//
// Print the summary table for every stored event:
//
//	go run ./cmd/earnings-reaction
//
// Print the full day-by-day price table for one symbol:
//
//	go run ./cmd/earnings-reaction --detail KEI
//
// Register the next quarter to watch for (so "has it reported yet" is a
// query against a known list, not a re-derivation from scratch):
//
//	go run ./cmd/earnings-reaction --watch
//
// List symbols whose watched quarter hasn't been declared/seeded yet:
//
//	go run ./cmd/earnings-reaction --pending
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"time"

	_ "github.com/lib/pq"
	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// watchedSymbols is every symbol currently under study — the original 5
// (longhold's biggest winners, §16) plus 10 large, liquid, well-known names
// spanning different sectors added as a non-pre-selected control group (the
// §17 Verdict's next-step #2).
var watchedSymbols = []string{
	"KEI", "NEULANDLAB", "JBMA", "JSWSTEEL", "LAURUSLABS",
	"RELIANCE", "TCS", "HDFCBANK", "INFY", "ICICIBANK",
	"SBIN", "BHARTIARTL", "MARUTI", "SUNPHARMA", "TITAN",
}

// seedEvents is the manually-researched Q1 FY27 (quarter ended 30 June 2026)
// reference data for the 15 stocks under study. Figures and dates are as
// reported by the sources in each SourceURL/Notes field. For the two banks
// (HDFCBANK, ICICIBANK), RevenueYoYPct holds Net Interest Income (NII)
// growth instead of a topline revenue figure — NII is the standard
// "revenue" proxy for a lender, not a real revenue line.
func seedEvents() []store.EarningsEvent {
	return []store.EarningsEvent{
		{
			Symbol: "KEI", ReportDate: date(2026, 8, 3), Quarter: "Q1 FY27",
			RevenueCr: 3185, RevenueYoYPct: 22.97, PATCr: 274, PATYoYPct: 40.05,
			EBITDAMarginPct: 12.43,
			SourceURL:       "https://www.business-standard.com (KEI Q1 FY27), upstox.com",
			Notes:           "Domestic Wires & Cables segment +29% YoY; shares closed ~+0.5% on the day.",
		},
		{
			Symbol: "NEULANDLAB", ReportDate: date(2026, 8, 5), Quarter: "Q1 FY27",
			RevenueCr: 650.1, RevenueYoYPct: 116.3, PATCr: 148, PATYoYPct: 962,
			EBITDAMarginPct: 35.5,
			SourceURL:       "https://www.business-standard.com (Neuland Lab Q1 PAT leaps), businessupturn.com",
			Notes:           "Commercial CMS project revenue drove the jump; board meeting ran 10:00am-3:42pm IST.",
		},
		{
			Symbol: "JBMA", ReportDate: date(2026, 7, 30), Quarter: "Q1 FY27",
			RevenueCr: 1442.45, RevenueYoYPct: 15.04, PATCr: 44.26, PATYoYPct: 13.37,
			SourceURL: "https://www.freepressjournal.in (JBM Auto Q1 FY27), equitybulls.com",
			Notes:     "Also proposed a Rs 1,500 Cr securities issue same day — a confound for isolating pure-earnings reaction.",
		},
		{
			Symbol: "JSWSTEEL", ReportDate: date(2026, 7, 17), Quarter: "Q1 FY27",
			RevenueCr: 47364, RevenueYoYPct: 9.78, PATCr: 4696, PATYoYPct: 112.6,
			SourceURL: "https://www.freepressjournal.in (JSW Steel Q1 FY27 profit), equitybulls.com",
			Notes:     "PAT attributable to owners 4651 Cr, +113% YoY; profit was down sequentially vs Q4 FY26 per one source.",
		},
		{
			Symbol: "LAURUSLABS", ReportDate: date(2026, 7, 24), Quarter: "Q1 FY27",
			RevenueCr: 2026.31, RevenueYoYPct: 29.10, PATCr: 367.60, PATYoYPct: 125.5,
			EBITDAMarginPct: 31.8,
			SourceURL:       "https://businessupturn.com (Laurus Labs Q1 FY27), equitybulls.com",
			Notes:           "Board approved results same day (24 Jul), conference call same evening; highest-ever quarterly revenue.",
		},
		{
			Symbol: "RELIANCE", ReportDate: date(2026, 7, 17), Quarter: "Q1 FY27",
			RevenueCr: 311850, RevenueYoYPct: 25.4, PATCr: 20946, PATYoYPct: -22.4,
			SourceURL: "https://www.business-standard.com (RIL Q1FY27 results), freepressjournal.in",
			Notes:     "PAT attributable to owners fell despite revenue +25.4% -- prior year had exceptional other-income gains; a real earnings-quality story, not a pure growth one.",
		},
		{
			Symbol: "TCS", ReportDate: date(2026, 7, 9), Quarter: "Q1 FY27",
			RevenueCr: 72275, RevenueYoYPct: 13.93, PATCr: 13349, PATYoYPct: 4.62,
			SourceURL: "https://www.businesstoday.in (TCS Q1 FY27), tcs.com newsroom",
			Notes:     "Revenue growth outpaced PAT growth (margin compression); Rs 12/share interim dividend declared same day.",
		},
		{
			Symbol: "HDFCBANK", ReportDate: date(2026, 7, 18), Quarter: "Q1 FY27",
			RevenueCr: 33534, RevenueYoYPct: 6.7, PATCr: 19060, PATYoYPct: 4.94,
			SourceURL: "https://www.business-standard.com (HDFC Bank Q1FY27 results), angelone.in",
			Notes:     "RevenueYoYPct is Net Interest Income (NII) growth, the standard revenue proxy for a bank, not topline revenue. Shares fell >4% on the day -- PAT missed NII/estimates despite YoY growth.",
		},
		{
			Symbol: "INFY", ReportDate: date(2026, 7, 23), Quarter: "Q1 FY27",
			RevenueCr: 48211, RevenueYoYPct: 14.03, PATCr: 7775, PATYoYPct: 12.29,
			SourceURL: "https://www.indiainfoline.com (Infosys Q1 FY27 results), sahi.com",
			Notes:     "PAT reported here is the YoY figure (+12.29%); one source separately noted a 9% QoQ sequential drop -- a reminder YoY and QoQ can disagree.",
		},
		{
			Symbol: "ICICIBANK", ReportDate: date(2026, 7, 17), Quarter: "Q1 FY27",
			RevenueCr: 0, RevenueYoYPct: 6.3, PATCr: 14804.50, PATYoYPct: 15.9,
			SourceURL: "https://www.business-standard.com (ICICI Bank Q1FY27 standalone profit), kotakneo.com",
			Notes:     "RevenueYoYPct is NII growth (bank revenue proxy). Standalone PAT figure used; consolidated PAT was Rs 15,440 Cr (+4.6% QoQ, a different comparison base).",
		},
		{
			Symbol: "SBIN", ReportDate: date(2026, 8, 7), Quarter: "Q1 FY27",
			RevenueCr: 0, RevenueYoYPct: 0, PATCr: 21121, PATYoYPct: 10.23,
			SourceURL: "https://www.business-standard.com (SBI Q1FY27 net profit), freepressjournal.in",
			Notes:     "Standalone PAT used (Rs 21,121 Cr, +10.23%); consolidated PAT was Rs 24,113 Cr (+12.08%). No clean NII/revenue YoY figure found in this search pass -- left at 0, not a real 0%.",
		},
		{
			Symbol: "BHARTIARTL", ReportDate: date(2026, 8, 4), Quarter: "Q1 FY27",
			RevenueCr: 58539.1, RevenueYoYPct: 18.35, PATCr: 8167.4, PATYoYPct: 37.32,
			SourceURL: "https://www.business-standard.com (Bharti Airtel Q1 results), upstox.com",
			Notes:     "ARPU improved ~6% to Rs 264; strong subscriber-upgrade-driven quarter.",
		},
		{
			Symbol: "MARUTI", ReportDate: date(2026, 7, 31), Quarter: "Q1 FY27",
			RevenueCr: 52456, RevenueYoYPct: 36.0, PATCr: 3352.1, PATYoYPct: -9.65,
			SourceURL: "https://upstox.com (Maruti Suzuki Q1 FY27 results), psuconnect.in",
			Notes:     "Revenue +36% but PAT fell -- material-cost pressure compressed EBITDA margin to 8.22% from 10.4%. A volume-growth-without-margin story, useful contrast to the pure-growth names in this set.",
		},
		{
			Symbol: "SUNPHARMA", ReportDate: date(2026, 7, 31), Quarter: "Q1 FY27",
			RevenueCr: 15300, RevenueYoYPct: 10.5, PATCr: 2895, PATYoYPct: 27.0,
			SourceURL: "https://www.business-standard.com (Sun Pharma Q1 profit), upstox.com",
			Notes:     "US formulation sales declined YoY even as overall PAT grew 27% -- a segment-level divergence worth remembering when reading the headline number.",
		},
		{
			Symbol: "TITAN", ReportDate: date(2026, 8, 7), Quarter: "Q1 FY27",
			RevenueCr: 20753, RevenueYoYPct: 40.31, PATCr: 1777, PATYoYPct: 62.87,
			SourceURL: "https://www.business-standard.com (Titan Q1FY27 results), freepressjournal.in",
			Notes:     "Jewellery division (ex-bullion/Digi-gold) income +43%; broad-based across jewellery/watches/eyewear segments.",
		},
	}
}

// nextWatchQuarter is the quarter to register on --watch: the one after
// every seeded event above. Q2 FY27 = the quarter ending 30 Sep 2026.
// Indian companies typically only announce their board-meeting date 1-2
// weeks ahead, so no attempt is made to guess an exact report date here --
// only the fiscal quarter end, which is a fixed calendar fact.
var nextWatchQuarter = struct {
	Label string
	End   time.Time
}{Label: "Q2 FY27", End: date(2026, 9, 30)}

// istOffset converts a UTC daily-candle timestamp back to its true IST
// trading date. Kite's daily candles are midnight IST (e.g.
// "2026-07-17T00:00:00+0530"); parseKiteTime converts that to UTC, which
// lands on 18:30 the *previous* calendar day. Without this correction, every
// date comparison against an externally-sourced (true IST) date is off by
// one trading day.
const istOffset = 5*time.Hour + 30*time.Minute

// istDate returns the candle's true IST trading date, truncated to midnight UTC
// so it can be compared directly against dates built with the date() helper.
func istDate(c models.Candle) time.Time {
	ist := c.Timestamp.Add(istOffset)
	return time.Date(ist.Year(), ist.Month(), ist.Day(), 0, 0, 0, 0, time.UTC)
}

func main() {
	seed := flag.Bool("seed", false, "insert/update the reference earnings events and exit")
	watch := flag.Bool("watch", false, "register the next quarter to watch for every tracked symbol and exit")
	pending := flag.Bool("pending", false, "list watched symbol/quarter pairs not yet declared and exit")
	detail := flag.String("detail", "", "print the full day-by-day price table for one symbol (e.g. --detail KEI)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	db, err := sql.Open("postgres", cfg.DSN())
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	es, err := store.NewEarningsStore(db)
	if err != nil {
		log.Fatal(err)
	}
	ws, err := store.NewWatchlistStore(db)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	switch {
	case *seed:
		for _, e := range seedEvents() {
			if err := es.Upsert(ctx, e); err != nil {
				log.Fatal(err)
			}
			// A seeded event means that quarter is no longer "upcoming" for
			// this symbol -- close out any matching watchlist entry too.
			if err := ws.MarkDeclared(ctx, e.Symbol, e.Quarter); err != nil {
				log.Fatal(err)
			}
			fmt.Printf("seeded %s %s (%s)\n", e.Symbol, e.Quarter, e.ReportDate.Format("2006-01-02"))
		}
		return

	case *watch:
		for _, sym := range watchedSymbols {
			if err := ws.Add(ctx, sym, nextWatchQuarter.Label, nextWatchQuarter.End); err != nil {
				log.Fatal(err)
			}
		}
		fmt.Printf("watching %d symbols for %s (quarter end %s)\n",
			len(watchedSymbols), nextWatchQuarter.Label, nextWatchQuarter.End.Format("2006-01-02"))
		return

	case *pending:
		rows, err := ws.Pending(ctx)
		if err != nil {
			log.Fatal(err)
		}
		if len(rows) == 0 {
			fmt.Println("nothing pending -- run --watch to register the next quarter")
			return
		}
		fmt.Printf("%-12s %-10s %-12s\n", "Symbol", "Quarter", "Quarter end")
		for _, w := range rows {
			fmt.Printf("%-12s %-10s %-12s\n", w.Symbol, w.Quarter, w.QuarterEnd.Format("2006-01-02"))
		}
		fmt.Printf("\n%d pending -- once a quarter-end has passed by ~4-6 weeks, search for that\n", len(rows))
		fmt.Println("symbol's results and add it via seedEvents() + --seed.")
		return
	}

	events, err := es.Query(ctx, store.EarningsFilter{})
	if err != nil {
		log.Fatal(err)
	}
	if len(events) == 0 {
		log.Fatal("no earnings events stored — run with --seed first")
	}

	cs := store.NewCandleStore(db)

	if *detail != "" {
		for _, e := range events {
			if e.Symbol == *detail {
				printDetail(ctx, cs, e)
				return
			}
		}
		log.Fatalf("no stored earnings event for symbol %q", *detail)
	}

	fmt.Printf("%-12s %-10s %-8s %8s %8s %9s %9s %9s\n",
		"Symbol", "Report", "Quarter", "PAT YoY", "Rev YoY", "Wk-before", "Post-wk", "Total")
	fmt.Println("--------------------------------------------------------------------------------")
	for _, e := range events {
		printSummaryRow(ctx, cs, e)
	}
}

// reaction holds the computed price-reaction figures for one earnings event.
type reaction struct {
	day0Date            time.Time
	preWeekPct          float64
	postWeekPct         float64
	totalPct            float64
	firstClose, day0Close, lastClose float64
	ok                  bool
}

// computeReaction pulls candles in the calendar window (report date ± 7
// calendar days) and computes the reaction. ok=false means there wasn't
// enough candle data to compute it (e.g. a future/undeclared event).
func computeReaction(ctx context.Context, cs *store.CandleStore, e store.EarningsEvent) (reaction, []models.Candle, int) {
	windowFrom := e.ReportDate.AddDate(0, 0, -7)
	windowTo := e.ReportDate.AddDate(0, 0, 7)
	queryFrom := windowFrom.AddDate(0, 0, -5)
	queryTo := windowTo.AddDate(0, 0, 5)

	candles, err := cs.GetCandles(ctx, e.Symbol, store.CandleFilter{From: &queryFrom, To: &queryTo})
	if err != nil || len(candles) == 0 {
		return reaction{}, nil, -1
	}

	day0 := -1
	for i, c := range candles {
		if !istDate(c).Before(e.ReportDate) {
			day0 = i
			break
		}
	}
	if day0 < 0 {
		return reaction{}, candles, -1
	}
	day0Close := candles[day0].Close

	var firstInWindow, lastInWindow float64
	haveFirst := false
	for _, c := range candles {
		d := istDate(c)
		if d.Before(windowFrom) || d.After(windowTo) {
			continue
		}
		if !haveFirst {
			firstInWindow = c.Close
			haveFirst = true
		}
		lastInWindow = c.Close
	}
	if !haveFirst {
		return reaction{}, candles, day0
	}

	return reaction{
		day0Date:    istDate(candles[day0]),
		preWeekPct:  (day0Close/firstInWindow - 1) * 100,
		postWeekPct: (lastInWindow/day0Close - 1) * 100,
		totalPct:    (lastInWindow/firstInWindow - 1) * 100,
		firstClose:  firstInWindow, day0Close: day0Close, lastClose: lastInWindow,
		ok: true,
	}, candles, day0
}

func printSummaryRow(ctx context.Context, cs *store.CandleStore, e store.EarningsEvent) {
	r, _, _ := computeReaction(ctx, cs, e)
	if !r.ok {
		fmt.Printf("%-12s %-10s %-8s %7.1f%% %7.1f%%   insufficient candle data\n",
			e.Symbol, e.ReportDate.Format("2006-01-02"), e.Quarter, e.PATYoYPct, e.RevenueYoYPct)
		return
	}
	fmt.Printf("%-12s %-10s %-8s %7.1f%% %7.1f%% %8.1f%% %8.1f%% %8.1f%%\n",
		e.Symbol, r.day0Date.Format("2006-01-02"), e.Quarter,
		e.PATYoYPct, e.RevenueYoYPct, r.preWeekPct, r.postWeekPct, r.totalPct)
}

// printDetail prints the full day-by-day close table for one symbol's
// earnings event, plus the summary line.
func printDetail(ctx context.Context, cs *store.CandleStore, e store.EarningsEvent) {
	windowFrom := e.ReportDate.AddDate(0, 0, -7)
	windowTo := e.ReportDate.AddDate(0, 0, 7)

	r, candles, day0 := computeReaction(ctx, cs, e)
	if day0 < 0 {
		fmt.Printf("=== %s: no trading day found on/after report date %s ===\n",
			e.Symbol, e.ReportDate.Format("2006-01-02"))
		return
	}

	fmt.Printf("=== %s — %s reported %s (PAT YoY %+.1f%%, Revenue YoY %+.1f%%) ===\n",
		e.Symbol, e.Quarter, e.ReportDate.Format("2006-01-02"), e.PATYoYPct, e.RevenueYoYPct)
	if e.Notes != "" {
		fmt.Printf("    note: %s\n", e.Notes)
	}
	fmt.Printf("%-12s %10s %12s %12s\n", "Date", "Close", "vs. prior", "vs. day 0")

	var prevClose float64
	havePrev := false
	day0Close := candles[day0].Close
	for i, c := range candles {
		d := istDate(c)
		if d.Before(windowFrom) || d.After(windowTo) {
			continue
		}
		marker := ""
		if i == day0 {
			marker = "  <- RESULT DAY"
		}
		priorPct := "   --"
		if havePrev {
			priorPct = fmt.Sprintf("%+.1f%%", (c.Close/prevClose-1)*100)
		}
		vsDay0Pct := fmt.Sprintf("%+.1f%%", (c.Close/day0Close-1)*100)
		fmt.Printf("%-12s %10.2f %12s %12s%s\n", d.Format("2006-01-02 Mon"), c.Close, priorPct, vsDay0Pct, marker)
		prevClose = c.Close
		havePrev = true
	}

	if !r.ok {
		fmt.Println("    (no candles found in the requested window)")
		return
	}
	fmt.Printf("Summary: week-before %+.1f%%  |  post-week %+.1f%%  |  total (window) %+.1f%%\n",
		r.preWeekPct, r.postWeekPct, r.totalPct)
}
