// Command earnings-reaction studies how NSE stock prices react to quarterly
// results declarations: for each stored EarningsEvent it pulls the calendar
// week before and the calendar week after the report date (report date − 7
// calendar days through report date + 7 calendar days, whatever trading days
// fall in that span) from the existing candles table and prints the
// day-by-day closes plus a summary of the pre/post/total reaction.
//
// Seed the reference data (5 known Q1 FY27 events, sourced manually from
// company press releases / financial news since NSE/BSE's own filing APIs
// are not reachable from this environment):
//
//	go run ./cmd/earnings-reaction --seed
//
// Then run the analysis:
//
//	go run ./cmd/earnings-reaction
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

// seedEvents is the manually-researched Q1 FY27 (quarter ended 30 June 2026)
// reference data for the 5 stocks under study. Figures and dates are as
// reported by the sources in each SourceURL/Notes field.
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
	}
}

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
	ctx := context.Background()

	if *seed {
		for _, e := range seedEvents() {
			if err := es.Upsert(ctx, e); err != nil {
				log.Fatal(err)
			}
			fmt.Printf("seeded %s %s (%s)\n", e.Symbol, e.Quarter, e.ReportDate.Format("2006-01-02"))
		}
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

	for i, e := range events {
		if i > 0 {
			fmt.Println()
		}
		printReaction(ctx, cs, e)
	}
}

// printReaction prints the day-by-day close for the calendar week before and
// the calendar week after e.ReportDate (report date ± 7 calendar days,
// whatever trading days fall in that span), plus a pre/day0/post/total
// summary.
func printReaction(ctx context.Context, cs *store.CandleStore, e store.EarningsEvent) {
	windowFrom := e.ReportDate.AddDate(0, 0, -7)
	windowTo := e.ReportDate.AddDate(0, 0, 7)
	// Pad the DB query so trading-day lookups near the window edges aren't
	// starved by a holiday/weekend sitting right on the boundary.
	queryFrom := windowFrom.AddDate(0, 0, -5)
	queryTo := windowTo.AddDate(0, 0, 5)

	candles, err := cs.GetCandles(ctx, e.Symbol, store.CandleFilter{From: &queryFrom, To: &queryTo})
	if err != nil {
		fmt.Printf("=== %s: error: %v ===\n", e.Symbol, err)
		return
	}

	day0 := -1
	for i, c := range candles {
		if !istDate(c).Before(e.ReportDate) {
			day0 = i
			break
		}
	}
	if day0 < 0 {
		fmt.Printf("=== %s: no trading day found on/after report date %s ===\n",
			e.Symbol, e.ReportDate.Format("2006-01-02"))
		return
	}
	day0Close := candles[day0].Close

	fmt.Printf("=== %s — %s reported %s (PAT YoY %+.1f%%, Revenue YoY %+.1f%%) ===\n",
		e.Symbol, e.Quarter, e.ReportDate.Format("2006-01-02"), e.PATYoYPct, e.RevenueYoYPct)
	if e.Notes != "" {
		fmt.Printf("    note: %s\n", e.Notes)
	}
	fmt.Printf("%-12s %10s %12s %12s\n", "Date", "Close", "vs. prior", "vs. day 0")

	var firstInWindow, lastInWindow float64
	haveFirst := false
	var prevClose float64
	havePrev := false

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

		if !haveFirst {
			firstInWindow = c.Close
			haveFirst = true
		}
		lastInWindow = c.Close
		prevClose = c.Close
		havePrev = true
	}

	if !haveFirst {
		fmt.Println("    (no candles found in the requested window)")
		return
	}

	preWeekPct := (day0Close/firstInWindow - 1) * 100
	postWeekPct := (lastInWindow/day0Close - 1) * 100
	totalPct := (lastInWindow/firstInWindow - 1) * 100
	fmt.Printf("Summary: week-before %+.1f%%  |  post-week %+.1f%%  |  total (window) %+.1f%%\n",
		preWeekPct, postWeekPct, totalPct)
}
