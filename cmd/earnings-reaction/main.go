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
	"github.com/sahiltyagi27/stock-market-analysis/internal/earnings"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// watchedSymbols is every symbol currently under study — the original 5
// (longhold's biggest winners, §16), 10 large-cap names added as a
// non-pre-selected control group (§17 Verdict's next-step #2), and the
// remaining 39 current Nifty 50 constituents (as of the Mar 2026 index
// review -- confirmed unchanged from the Dec 2025 constituent list, and
// the Sep 2026 rejig hadn't happened yet as of this writing) added to
// cover the full index rather than a hand-picked subset of it. 11 of the
// original 15 (all but KEI/NEULANDLAB/JBMA/LAURUSLABS) are themselves
// Nifty 50 members, so the union below is 50 + 4 = 54 symbols, not 65.
var watchedSymbols = []string{
	"KEI", "NEULANDLAB", "JBMA", "JSWSTEEL", "LAURUSLABS",
	"RELIANCE", "TCS", "HDFCBANK", "INFY", "ICICIBANK",
	"SBIN", "BHARTIARTL", "MARUTI", "SUNPHARMA", "TITAN",
	"ADANIENT", "ADANIPORTS", "APOLLOHOSP", "ASIANPAINT", "AXISBANK",
	"BAJAJ-AUTO", "BAJFINANCE", "BAJAJFINSV", "BEL", "CIPLA",
	"COALINDIA", "DRREDDY", "EICHERMOT", "ETERNAL", "GRASIM",
	"HCLTECH", "HDFCLIFE", "HINDALCO", "HINDUNILVR", "INDIGO",
	"ITC", "JIOFIN", "KOTAKBANK", "LT", "M&M",
	"MAXHEALTH", "NESTLEIND", "NTPC", "ONGC", "POWERGRID",
	"SBILIFE", "SHRIRAMFIN", "TATACONSUM", "TMPV", "TATASTEEL",
	"TECHM", "TRENT", "ULTRACEMCO", "WIPRO",
}

// seedEvents is the manually-researched Q1 FY27 (quarter ended 30 June 2026)
// reference data for the 54 stocks under study. Figures and dates are as
// reported by the sources in each SourceURL/Notes field. For banks and
// insurers, RevenueYoYPct holds Net Interest Income (NII) or Net Premium
// Income growth instead of a topline revenue figure — the standard
// "revenue" proxy for a lender/insurer, not a real revenue line.
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
		{
			Symbol: "ADANIENT", ReportDate: date(2026, 7, 29), Quarter: "Q1 FY27",
			RevenueCr: 33546, RevenueYoYPct: 50, PATCr: -1461.54, PATYoYPct: -299.1,
			SourceURL: "https://www.indiainfoline.com (Adani Enterprises Q1 FY27), business-standard.com",
			Notes:     "Swung to a net loss on a Rs 2,644 Cr exceptional OFAC-settlement item; EBITDA still grew +49% YoY to a record Rs 5,642 Cr, so the PAT collapse is a one-off, not the operating trend. PATYoYPct computed against last year's Rs 734.41 Cr PAT.",
		},
		{
			Symbol: "ADANIPORTS", ReportDate: date(2026, 7, 29), Quarter: "Q1 FY27",
			RevenueCr: 10821, RevenueYoYPct: 19, PATCr: 3620, PATYoYPct: 9,
			SourceURL: "https://www.business-standard.com (Adani Ports Q1 results), upstox.com",
			Notes:     "Sources gave PAT figures ranging Rs 3,315-3,650 Cr across consolidated/other cuts; used the Business Standard consolidated PAT (+9% YoY) figure.",
		},
		{
			Symbol: "APOLLOHOSP", ReportDate: date(2026, 8, 12), Quarter: "Q1 FY27",
			RevenueCr: 7044, RevenueYoYPct: 20.6, PATCr: 610, PATYoYPct: 38.4,
			SourceURL: "https://tradebrains.in (Apollo Hospitals Q1 FY27), inkl.com",
			Notes:     "PAT and revenue both beat Street estimates per contemporaneous coverage; bed occupancy rose to 70% from 65% YoY.",
		},
		{
			Symbol: "ASIANPAINT", ReportDate: date(2026, 7, 29), Quarter: "Q1 FY27",
			RevenueCr: 10541.94, RevenueYoYPct: 17.94, PATCr: 1559.45, PATYoYPct: 39.60,
			SourceURL: "https://upstox.com (Asian Paints Q1 FY27 results), sahi.com",
			Notes:     "Decorative business volume growth 9%, value growth 16.6% on calibrated pricing.",
		},
		{
			Symbol: "AXISBANK", ReportDate: date(2026, 7, 18), Quarter: "Q1 FY27",
			RevenueCr: 14646, RevenueYoYPct: 8, PATCr: 7114, PATYoYPct: 22.5,
			SourceURL: "https://www.republicworld.com (Axis Bank Q1 FY27), business-standard.com",
			Notes:     "RevenueYoYPct is NII growth. Standalone PAT used; consolidated PAT was Rs 7,632 Cr (+22%). Reported to have beaten brokerage expectations.",
		},
		{
			Symbol: "BAJAJ-AUTO", ReportDate: date(2026, 7, 21), Quarter: "Q1 FY27",
			RevenueCr: 17244, RevenueYoYPct: 37, PATCr: 2983, PATYoYPct: 42,
			SourceURL: "https://www.zeebiz.com (Bajaj Auto Q1 FY27), freepressjournal.in",
			Notes:     "Standalone figures used (consolidated PAT +45.9% to Rs 3,225.63 Cr on Rs 21,688.83 Cr revenue, +65.15%). Record exports, 700K+ units/quarter for the first time.",
		},
		{
			Symbol: "BAJFINANCE", ReportDate: date(2026, 7, 30), Quarter: "Q1 FY27",
			RevenueCr: 23165.45, RevenueYoYPct: 19.58, PATCr: 5985.75, PATYoYPct: 27.4,
			SourceURL: "https://www.businesstoday.in (Bajaj Finance Q1 FY27), business-standard.com",
			Notes:     "AUM +23.9% YoY to Rs 5.47 lakh Cr; NII +23% YoY.",
		},
		{
			Symbol: "BAJAJFINSV", ReportDate: date(2026, 7, 31), Quarter: "Q1 FY27",
			RevenueCr: 42036.90, RevenueYoYPct: 19, PATCr: 3132.35, PATYoYPct: 12.3,
			SourceURL: "https://www.freepressjournal.in (Bajaj Finserv Q1 FY27), business-standard.com",
			Notes:     "PAT figure used is attributable-to-owners (+12.3%); the +18% headline elsewhere is consolidated PAT before minority interest (Rs 6,296.67 Cr).",
		},
		{
			Symbol: "BEL", ReportDate: date(2026, 7, 27), Quarter: "Q1 FY27",
			RevenueCr: 5533.06, RevenueYoYPct: 25.27, PATCr: 1054.34, PATYoYPct: 9,
			SourceURL: "https://upstox.com (Bharat Electronics Q1 results), business-standard.com",
			Notes:     "Order book Rs 72,258 Cr as of 1 Jul 2026; management reaffirmed full-year >15% revenue growth guidance.",
		},
		{
			Symbol: "CIPLA", ReportDate: date(2026, 7, 23), Quarter: "Q1 FY27",
			RevenueCr: 0, RevenueYoYPct: 0, PATCr: 785.55, PATYoYPct: -39.2,
			SourceURL: "https://www.freepressjournal.in (Cipla Q1 FY27), business-standard.com",
			Notes:     "No clean revenue YoY figure found in this search pass (results emphasized QoQ, not YoY, revenue comparisons) -- left at 0, not a real 0%. PAT decline driven by US business headwinds.",
		},
		{
			Symbol: "COALINDIA", ReportDate: date(2026, 7, 27), Quarter: "Q1 FY27",
			RevenueCr: 46255, RevenueYoYPct: 8, PATCr: 8852, PATYoYPct: 0.63,
			SourceURL: "https://upstox.com (Coal India Q1 FY27), indianmasterminds.com",
			Notes:     "PAT essentially flat YoY despite +8% revenue -- EBITDA actually fell 4% YoY, a real margin story.",
		},
		{
			Symbol: "DRREDDY", ReportDate: date(2026, 7, 22), Quarter: "Q1 FY27",
			RevenueCr: 8070.5, RevenueYoYPct: -6, PATCr: 443.5, PATYoYPct: -69,
			SourceURL: "https://www.republicworld.com (Dr Reddy's Q1 FY27), angelone.in",
			Notes:     "Hit by a Rs 240 Cr semaglutide API quality provision and a 35% North America revenue decline (lenalidomide sales fell sharply). One of two clear revenue-and-PAT-both-down cases in this set.",
		},
		{
			Symbol: "EICHERMOT", ReportDate: date(2026, 7, 29), Quarter: "Q1 FY27",
			RevenueCr: 6632, RevenueYoYPct: 31.5, PATCr: 1463, PATYoYPct: 21,
			SourceURL: "https://upstox.com (Eicher Motors Q1 FY27), dailyhunt.in",
			Notes:     "Royal Enfield's highest-ever quarterly sales (332,940 units, +27% YoY).",
		},
		{
			Symbol: "ETERNAL", ReportDate: date(2026, 7, 22), Quarter: "Q1 FY27",
			RevenueCr: 20211, RevenueYoYPct: 182, PATCr: 92, PATYoYPct: 268,
			SourceURL: "https://www.republicworld.com (Eternal/Zomato Q1 FY27), freepressjournal.in",
			Notes:     "Formerly Zomato; Blinkit (quick-commerce) is the growth driver behind the outsized revenue jump. Both PAT and revenue are small-base, high-growth-rate figures -- treat the percentages with more caution than the large-caps in this set.",
		},
		{
			Symbol: "GRASIM", ReportDate: date(2026, 8, 12), Quarter: "Q1 FY27",
			RevenueCr: 48716.20, RevenueYoYPct: 21.43, PATCr: 3846.28, PATYoYPct: 38.8,
			SourceURL: "https://www.business-standard.com (Grasim Q1 results), apparelresources.com",
			Notes:     "Record consolidated EBITDA, +26% YoY.",
		},
		{
			Symbol: "HCLTECH", ReportDate: date(2026, 7, 13), Quarter: "Q1 FY27",
			RevenueCr: 34579, RevenueYoYPct: 13.94, PATCr: 4626, PATYoYPct: 20.35,
			SourceURL: "https://www.freepressjournal.in (HCL Technologies Q1 FY27), equitybulls.com",
			Notes:     "Rs 12/share interim dividend declared same day.",
		},
		{
			Symbol: "HDFCLIFE", ReportDate: date(2026, 7, 15), Quarter: "Q1 FY27",
			RevenueCr: 16728, RevenueYoYPct: 15.1, PATCr: 611, PATYoYPct: 11.5,
			SourceURL: "https://univest.in (HDFC Life Q1 results FY27), business-standard.com",
			Notes:     "RevenueYoYPct is net premium income growth (insurer revenue proxy). Solvency ratio 185%, down YoY but still well above the 150% IRDAI minimum.",
		},
		{
			Symbol: "HINDALCO", ReportDate: date(2026, 8, 7), Quarter: "Q1 FY27",
			RevenueCr: 84825, RevenueYoYPct: 32.06, PATCr: 7013, PATYoYPct: 75.15,
			SourceURL: "https://www.adityabirla.com (Hindalco Q1 FY27 results), sahi.com",
			Notes:     "Record quarter across Aluminium Upstream/Downstream, Copper, and Novelis segments.",
		},
		{
			Symbol: "HINDUNILVR", ReportDate: date(2026, 7, 28), Quarter: "Q1 FY27",
			RevenueCr: 17184, RevenueYoYPct: 10, PATCr: 2680, PATYoYPct: -2.2,
			SourceURL: "https://www.republicworld.com (HUL Q1 FY27 results), business-standard.com",
			Notes:     "Highest revenue growth in 13 quarters (10% USG, 5% UVG) but PAT dipped slightly on a one-off tax credit in the prior-year base -- a clean example of revenue and PAT genuinely diverging for a boring, explainable reason. Shares fell >3% on the day despite the revenue beat.",
		},
		{
			Symbol: "INDIGO", ReportDate: date(2026, 7, 23), Quarter: "Q1 FY27",
			RevenueCr: 24584, RevenueYoYPct: 20, PATCr: -238, PATYoYPct: -110.9,
			SourceURL: "https://www.goodreturns.in (IndiGo Q1 FY27), business-standard.com",
			Notes:     "Swung to a net loss from Rs 2,176 Cr profit a year earlier -- steep fuel-cost increases (+86% per one source) outweighed strong 20% revenue growth. The other clear swing-to-loss case in this set alongside ADANIENT.",
		},
		{
			Symbol: "ITC", ReportDate: date(2026, 7, 31), Quarter: "Q1 FY27",
			RevenueCr: 29523.30, RevenueYoYPct: 27.64, PATCr: 4394.13, PATYoYPct: -16.21,
			SourceURL: "https://www.freepressjournal.in (ITC Q1 FY27), businesstoday.in",
			Notes:     "PAT figure used is attributable-to-owners (-16.21%); strong revenue growth did not translate to profit growth this quarter.",
		},
		{
			Symbol: "JIOFIN", ReportDate: date(2026, 7, 16), Quarter: "Q1 FY27",
			RevenueCr: 2004.47, RevenueYoYPct: 227.45, PATCr: 830.25, PATYoYPct: 155.38,
			SourceURL: "https://www.freepressjournal.in (Jio Financial Services Q1 FY27), upstox.com",
			Notes:     "Small-base, high-growth-rate figures (interest income, fee income, and dividend income all scaling up together) -- treat the percentages with more caution than the large, mature names in this set.",
		},
		{
			Symbol: "KOTAKBANK", ReportDate: date(2026, 7, 18), Quarter: "Q1 FY27",
			RevenueCr: 30068.60, RevenueYoYPct: 12.6, PATCr: 5480.46, PATYoYPct: 22.5,
			SourceURL: "https://www.business-standard.com (Kotak Mahindra Bank Q1FY27), angelone.in",
			Notes:     "RevenueYoYPct is total income growth (bank revenue proxy). Consolidated PAT used; standalone PAT was Rs 4,122.96 Cr.",
		},
		{
			Symbol: "LT", ReportDate: date(2026, 7, 28), Quarter: "Q1 FY27",
			RevenueCr: 67942, RevenueYoYPct: 6.69, PATCr: 4122.85, PATYoYPct: 13.98,
			SourceURL: "https://www.business-standard.com (L&T Q1 FY27), dailyhunt.in",
			Notes:     "PAT figure used is attributable-to-owners; order inflow +14% YoY to Rs 1.08 lakh Cr, order book Rs 7.79 lakh Cr.",
		},
		{
			Symbol: "M&M", ReportDate: date(2026, 7, 30), Quarter: "Q1 FY27",
			RevenueCr: 57533.44, RevenueYoYPct: 26.62, PATCr: 5454.54, PATYoYPct: 33.6,
			SourceURL: "https://upstox.com (Mahindra & Mahindra Q1 FY27), equitybulls.com",
			Notes:     "Consolidated, attributable-to-owners figures used. Standalone PAT grew a much smaller +7% -- the consolidated growth is significantly boosted by subsidiaries/JVs, worth remembering.",
		},
		{
			Symbol: "MAXHEALTH", ReportDate: date(2026, 8, 13), Quarter: "Q1 FY27",
			RevenueCr: 2982, RevenueYoYPct: 16, PATCr: 357, PATYoYPct: 3,
			SourceURL: "https://www.sahi.com (Max Healthcare Q1 FY27), investywise.com",
			Notes:     "Revenue growth far outpaced PAT growth as the network absorbed new beds/acquisitions -- an expansion-phase margin story.",
		},
		{
			Symbol: "NESTLEIND", ReportDate: date(2026, 7, 22), Quarter: "Q1 FY27",
			RevenueCr: 6378.18, RevenueYoYPct: 25.16, PATCr: 975.1, PATYoYPct: 47.9,
			SourceURL: "https://www.republicworld.com (Nestle India Q1 FY27), business-standard.com",
			Notes:     "Domestic sales +25% on rural penetration and quick-commerce momentum.",
		},
		{
			Symbol: "NTPC", ReportDate: date(2026, 7, 24), Quarter: "Q1 FY27",
			RevenueCr: 43831.86, RevenueYoYPct: 3, PATCr: 5342.36, PATYoYPct: 12,
			SourceURL: "https://www.business-standard.com (NTPC Q1 FY27), psuconnect.in",
			Notes:     "Standalone figures used (consolidated PAT +13% to Rs 6,896 Cr). Coal-plant PLF of 76.71% vs an all-India average of 70.32%.",
		},
		{
			Symbol: "ONGC", ReportDate: date(2026, 8, 4), Quarter: "Q1 FY27",
			RevenueCr: 46460, RevenueYoYPct: 45.2, PATCr: 17033.81, PATYoYPct: 112,
			SourceURL: "https://www.angelone.in (ONGC Q1FY27 earnings), business-standard.com",
			Notes:     "Standalone PAT used, driven by crude realisation rising to $99.45/bbl from $66.13/bbl. Consolidated profit fell -43% on refining & marketing losses -- a sharp standalone/consolidated divergence worth remembering.",
		},
		{
			Symbol: "POWERGRID", ReportDate: date(2026, 8, 5), Quarter: "Q1 FY27",
			RevenueCr: 11697, RevenueYoYPct: 2.2, PATCr: 3598, PATYoYPct: -0.88,
			SourceURL: "https://dailypioneer.com (POWERGRID Q1 FY27), investywise.com",
			Notes:     "Consolidated figures used; PATYoYPct computed against last year's Rs 3,630 Cr PAT (from a separate Q1 FY26 article) since no single source gave the YoY% directly.",
		},
		{
			Symbol: "SBILIFE", ReportDate: date(2026, 7, 24), Quarter: "Q1 FY27",
			RevenueCr: 20078.2, RevenueYoYPct: 16.88, PATCr: 725, PATYoYPct: 22,
			SourceURL: "https://scanx.trade (SBI Life Q1 results), equitybulls.com",
			Notes:     "RevenueYoYPct is net premium income growth. APE (annualised premium equivalent) grew 36% YoY, VNB +29%.",
		},
		{
			Symbol: "SHRIRAMFIN", ReportDate: date(2026, 7, 24), Quarter: "Q1 FY27",
			RevenueCr: 13400.43, RevenueYoYPct: 16.16, PATCr: 3444.56, PATYoYPct: 59.79,
			SourceURL: "https://univest.in (Shriram Finance Q1 FY27), equitybulls.com",
			Notes:     "Standalone PAT used. AUM +16.6% YoY to Rs 2,72,000 Cr; cost-to-income ratio improved to 25.48% from 29.29%.",
		},
		{
			Symbol: "TATACONSUM", ReportDate: date(2026, 7, 24), Quarter: "Q1 FY27",
			RevenueCr: 5348.88, RevenueYoYPct: 11.93, PATCr: 426.98, PATYoYPct: 27.78,
			SourceURL: "https://upstox.com (Tata Consumer Products Q1 results), business-standard.com",
			Notes:     "EBITDA +19% YoY, margin +70bps.",
		},
		{
			Symbol: "TMPV", ReportDate: date(2026, 8, 13), Quarter: "Q1 FY27",
			RevenueCr: 95800, RevenueYoYPct: 9.3, PATCr: 775, PATYoYPct: -80.3,
			SourceURL: "https://businessupturn.com (Tata Motors Passenger Vehicles Q1 FY27), cars.tatamotors.com",
			Notes:     "Consolidated figures for the passenger-vehicle entity (post the Tata Motors CV/PV demerger). JLR supply disruptions, commodity pressure, and adverse FX outweighed domestic PV growth -- one of the largest revenue/PAT divergences in this set.",
		},
		{
			Symbol: "TATASTEEL", ReportDate: date(2026, 7, 30), Quarter: "Q1 FY27",
			RevenueCr: 60794.29, RevenueYoYPct: 14.34, PATCr: 2318.35, PATYoYPct: 11.58,
			SourceURL: "https://www.equitybulls.com (Tata Steel Q1 FY27), sahi.com",
			Notes:     "PAT figure used is attributable-to-owners; a one-time Rs 345 Cr exceptional loss dragged PAT down 20.8% QoQ despite the positive YoY figure.",
		},
		{
			Symbol: "TECHM", ReportDate: date(2026, 7, 17), Quarter: "Q1 FY27",
			RevenueCr: 15712, RevenueYoYPct: 17.7, PATCr: 1465, PATYoYPct: 28.4,
			SourceURL: "https://www.dqindia.com (Tech Mahindra Q1 FY27), indiainfoline.com",
			Notes:     "EBIT margin expanded ~330bps YoY to 14.4%; new deal-wins +33% YoY to $1.08B.",
		},
		{
			Symbol: "TRENT", ReportDate: date(2026, 8, 6), Quarter: "Q1 FY27",
			RevenueCr: 5754.71, RevenueYoYPct: 17.84, PATCr: 518.07, PATYoYPct: 21.98,
			SourceURL: "https://www.freepressjournal.in (Trent Q1 FY27), angelone.in",
			Notes:     "Reported to have hit a record high on the results.",
		},
		{
			Symbol: "ULTRACEMCO", ReportDate: date(2026, 7, 20), Quarter: "Q1 FY27",
			RevenueCr: 24465, RevenueYoYPct: 16, PATCr: 2604, PATYoYPct: 17,
			SourceURL: "https://www.adityabirla.com (UltraTech Q1 FY27), business-standard.com",
			Notes:     "Grey cement volumes +12.2% YoY to 41.31 MT.",
		},
		{
			Symbol: "WIPRO", ReportDate: date(2026, 7, 16), Quarter: "Q1 FY27",
			RevenueCr: 24478.6, RevenueYoYPct: 10.6, PATCr: 3356.3, PATYoYPct: 0.6,
			SourceURL: "https://www.republicworld.com (Wipro Q1 FY27), business-standard.com",
			Notes:     "PAT essentially flat YoY (missed Street estimates) despite +10.6% revenue -- margins at a 15-quarter low. A clean example of the revenue/PAT-growth gap the correlation analysis is trying to measure.",
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

func printSummaryRow(ctx context.Context, cs *store.CandleStore, e store.EarningsEvent) {
	r, _, _ := earnings.Compute(ctx, cs, e)
	if !r.OK {
		fmt.Printf("%-12s %-10s %-8s %7.1f%% %7.1f%%   insufficient candle data\n",
			e.Symbol, e.ReportDate.Format("2006-01-02"), e.Quarter, e.PATYoYPct, e.RevenueYoYPct)
		return
	}
	fmt.Printf("%-12s %-10s %-8s %7.1f%% %7.1f%% %8.1f%% %8.1f%% %8.1f%%\n",
		e.Symbol, r.Day0Date.Format("2006-01-02"), e.Quarter,
		e.PATYoYPct, e.RevenueYoYPct, r.PreWeekPct, r.PostWeekPct, r.TotalPct)
}

// printDetail prints the full day-by-day close table for one symbol's
// earnings event, plus the summary line.
func printDetail(ctx context.Context, cs *store.CandleStore, e store.EarningsEvent) {
	windowFrom, windowTo := earnings.Window(e)

	r, candles, day0 := earnings.Compute(ctx, cs, e)
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
		d := earnings.ISTDate(c)
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

	if !r.OK {
		fmt.Println("    (no candles found in the requested window)")
		return
	}
	fmt.Printf("Summary: week-before %+.1f%%  |  post-week %+.1f%%  |  total (window) %+.1f%%\n",
		r.PreWeekPct, r.PostWeekPct, r.TotalPct)
}
