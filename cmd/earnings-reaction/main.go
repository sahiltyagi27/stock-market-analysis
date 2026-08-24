// Command earnings-reaction studies how NSE stock prices react to quarterly
// results declarations: for each stored EarningsEvent it pulls the 7 trading
// days before and 7 after the report date (15 days total, day 0 = report
// date) from the existing candles table and computes the pre-drift,
// announcement-day move, and post-week reaction.
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

	fmt.Printf("%-12s %-10s %-8s %8s %8s %9s %9s %9s %9s\n",
		"Symbol", "Report", "Quarter", "PAT YoY", "Rev YoY", "Pre-wk%", "Day0%", "Post-wk%", "15day%")
	fmt.Println("--------------------------------------------------------------------------------------------")

	for _, e := range events {
		from := e.ReportDate.AddDate(0, 0, -21)
		to := e.ReportDate.AddDate(0, 0, 21)
		candles, err := cs.GetCandles(ctx, e.Symbol, store.CandleFilter{From: &from, To: &to})
		if err != nil {
			fmt.Printf("%-12s error: %v\n", e.Symbol, err)
			continue
		}

		day0 := findDay0(candles, e.ReportDate)
		if day0 < 0 || day0-7 < 0 || day0+7 >= len(candles) {
			fmt.Printf("%-12s insufficient candle window around %s (day0=%d, n=%d)\n",
				e.Symbol, e.ReportDate.Format("2006-01-02"), day0, len(candles))
			continue
		}

		preClose := candles[day0-7].Close
		dayMinus1 := candles[day0-1].Close
		d0Close := candles[day0].Close
		postClose := candles[day0+7].Close

		preWeekPct := (dayMinus1/preClose - 1) * 100
		day0Pct := (d0Close/dayMinus1 - 1) * 100
		postWeekPct := (postClose/d0Close - 1) * 100
		totalPct := (postClose/preClose - 1) * 100

		day0IST := candles[day0].Timestamp.Add(istOffset)
		fmt.Printf("%-12s %-10s %-8s %7.1f%% %7.1f%% %8.1f%% %8.1f%% %8.1f%% %8.1f%%\n",
			e.Symbol, day0IST.Format("2006-01-02"), e.Quarter,
			e.PATYoYPct, e.RevenueYoYPct, preWeekPct, day0Pct, postWeekPct, totalPct)
	}
}

// istOffset converts a UTC daily-candle timestamp back to its true IST
// trading date. Kite's daily candles are midnight IST (e.g.
// "2026-07-17T00:00:00+0530"); parseKiteTime converts that to UTC, which
// lands on 18:30 the *previous* calendar day. Without this correction, every
// date comparison against an externally-sourced (true IST) date is off by
// one trading day.
const istOffset = 5*time.Hour + 30*time.Minute

// findDay0 returns the index of the first candle on or after reportDate —
// the trading day the announcement's price impact should first show up
// (results are typically declared during or after market hours on the
// board-meeting date; using the first candle >= that date captures same-day
// moves when announced during market hours and next-day moves otherwise).
func findDay0(candles []models.Candle, reportDate time.Time) int {
	for i, c := range candles {
		ist := c.Timestamp.Add(istOffset)
		d := time.Date(ist.Year(), ist.Month(), ist.Day(), 0, 0, 0, 0, time.UTC)
		if !d.Before(reportDate) {
			return i
		}
	}
	return -1
}
