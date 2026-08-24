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
	// Batch 1 of the full-501 expansion (alphabetical from config/symbols.txt,
	// no cherry-picking) -- ABDL and ABLBL were attempted but dropped: no
	// clean report date turned up in search for either, consistent with the
	// expected coverage drop-off for smaller/newer listings flagged before
	// this expansion started.
	"360ONE", "3MINDIA", "AADHARHFC", "AARTIIND", "AAVAS",
	"ABB", "ABBOTINDIA", "ABCAPITAL", "ABSLAMC", "ABFRL",
	"ABREL", "ACC", "ACE", "ACMESOLAR", "ACUTAAS",
	"ADANIENSOL", "ADANIGREEN", "ADANIPOWER",
	// Batch 2 (72 -> 90). Continuing alphabetically; ABDL/ABLBL from batch 1
	// still untracked (same reason). AEGISVOPAK, ANTHEM, and ANANDRATHI are
	// all newer/thinner-coverage listings -- included anyway since a date
	// and figures were findable, unlike ABDL/ABLBL.
	"AEGISLOG", "AEGISVOPAK", "AFCONS", "AFFLE", "AIAENG",
	"AIIL", "AJANTPHARM", "ALKEM", "AMBER", "AMBUJACEM",
	"ANANDRATHI", "ANANTRAJ", "ANGELONE", "ANTHEM", "ANURAS",
	"APARINDS", "APLAPOLLO", "APOLLOTYRE",
}

// seedEvents is the manually-researched Q1 FY27 (quarter ended 30 June 2026)
// reference data for the stocks under study. Figures and dates are as
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
		// --- Batch 1 of the full-501 expansion (18 stocks) ---
		{
			Symbol: "360ONE", ReportDate: date(2026, 7, 16), Quarter: "Q1 FY27",
			RevenueCr: 822, RevenueYoYPct: 24.2, PATCr: 330, PATYoYPct: 14.8,
			SourceURL: "https://www.freepressjournal.in (360 ONE WAM Q1 FY27), business-standard.com",
			Notes:     "RevenueCr is revenue from operations; total revenue (incl. other income) was Rs 870 Cr, +20% YoY. AUM Rs 7,76,755 Cr.",
		},
		{
			Symbol: "3MINDIA", ReportDate: date(2026, 8, 14), Quarter: "Q1 FY27",
			RevenueCr: 1423, RevenueYoYPct: 19, PATCr: 233, PATYoYPct: 31.2,
			SourceURL: "https://www.investywise.com (3M India Q1 FY27), sahi.com",
			Notes:     "PAT growth includes a Rs 73.13 Cr exceptional gain from a Pimpri, Pune land sale -- ex-exceptional growth would be meaningfully lower. Fifth consecutive quarter of double-digit sales growth.",
		},
		{
			Symbol: "AADHARHFC", ReportDate: date(2026, 7, 31), Quarter: "Q1 FY27",
			RevenueCr: 992.89, RevenueYoYPct: 17.06, PATCr: 282.36, PATYoYPct: 18.98,
			SourceURL: "https://www.equitybulls.com (Aadhar Housing Finance Q1 FY27), freepressjournal.in",
			Notes:     "AUM +18% YoY to Rs 31,364 Cr.",
		},
		{
			Symbol: "AARTIIND", ReportDate: date(2026, 7, 30), Quarter: "Q1 FY27",
			RevenueCr: 2627, RevenueYoYPct: 41, PATCr: 155, PATYoYPct: 260,
			SourceURL: "https://www.equitybulls.com (Aarti Industries Q1 FY27), indianchemicalnews.com",
			Notes:     "Large PAT growth off a small prior-year base -- driven by optimized product mix, inventory management, and forex gains, not purely operating improvement. Treat the 260% with more caution than the large-cap figures in this set.",
		},
		{
			Symbol: "AAVAS", ReportDate: date(2026, 7, 21), Quarter: "Q1 FY27",
			RevenueCr: 709.10, RevenueYoYPct: 12.9, PATCr: 171.27, PATYoYPct: 23,
			SourceURL: "https://www.equitybulls.com (Aavas Financiers Q1 FY27), scanx.trade",
			Notes:     "RevenueCr is total income (interest income dominated). Disbursements +41% YoY, AUM +15.4% YoY.",
		},
		{
			Symbol: "ABB", ReportDate: date(2026, 7, 31), Quarter: "Q1 FY27",
			RevenueCr: 3558.87, RevenueYoYPct: 21.03, PATCr: 370.07, PATYoYPct: 7.9,
			SourceURL: "https://www.freepressjournal.in (ABB India Q1 FY27), marketsmojo.com",
			Notes:     "Highest-ever quarterly revenue but operating margin contracted to 12.70% from 13.79% -- PAT growth trailing revenue growth is the margin-compression pattern, same shape as TCS/WIPRO in this set.",
		},
		{
			Symbol: "ABBOTINDIA", ReportDate: date(2026, 8, 12), Quarter: "Q1 FY27",
			RevenueCr: 1813, RevenueYoYPct: 4.33, PATCr: 428, PATYoYPct: 17.13,
			SourceURL: "https://univest.in (Abbott India Q1 FY27), indiaipo.in",
			Notes:     "PAT growth well ahead of revenue growth -- EBITDA margin expanded to 28.8%, a pure-margin story rather than a volume one.",
		},
		{
			Symbol: "ABCAPITAL", ReportDate: date(2026, 7, 31), Quarter: "Q1 FY27",
			RevenueCr: 14731, RevenueYoYPct: 29, PATCr: 1175, PATYoYPct: 40,
			SourceURL: "https://www.adityabirla.com (Aditya Birla Capital Q1 FY27), business-standard.com",
			Notes:     "Raised Rs 4,000 Cr growth capital same quarter; lending portfolio +32% YoY, housing finance AUM +50% YoY.",
		},
		{
			Symbol: "ABSLAMC", ReportDate: date(2026, 7, 21), Quarter: "Q1 FY27",
			RevenueCr: 462.96, RevenueYoYPct: 0, PATCr: 309.49, PATYoYPct: 11.69,
			SourceURL: "https://www.equitybulls.com (Aditya Birla Sun Life AMC Q1 FY27), upstox.com",
			Notes:     "RevenueYoYPct not cleanly found in this search pass -- left at 0, not a real 0%. Shares fell ~4% on muted sequential revenue growth despite the YoY PAT gain. AUM crossed Rs 6 lakh Cr.",
		},
		{
			Symbol: "ABFRL", ReportDate: date(2026, 8, 8), Quarter: "Q1 FY27",
			RevenueCr: 2025.56, RevenueYoYPct: 10.6, PATCr: -241.73, PATYoYPct: -3.4,
			SourceURL: "https://www.investywise.com (ABFRL Q1 FY27), scanx.trade",
			Notes:     "Already loss-making last year (Rs -233.73 Cr) and the loss widened further this quarter -- PATYoYPct here reflects that the loss got worse (not a swing from profit like ADANIENT/INDIGO), computed as -(change in loss magnitude)/prior loss magnitude for a more intuitive sign than the raw (current-prior)/prior formula would give on a negative base.",
		},
		{
			Symbol: "ABREL", ReportDate: date(2026, 8, 13), Quarter: "Q1 FY27",
			RevenueCr: 188.85, RevenueYoYPct: 29.74, PATCr: -38.55, PATYoYPct: -51.4,
			SourceURL: "https://www.business-standard.com (Aditya Birla Real Estate Q1 FY27), tradebrains.in",
			Notes:     "Consolidated loss widened from Rs -25.47 Cr to Rs -38.55 Cr (PATYoYPct sign convention same as ABFRL above). Standalone entity was actually profitable (+Rs 31.11 Cr) -- another case where standalone/consolidated tell different stories. Collections +31% YoY.",
		},
		{
			Symbol: "ACC", ReportDate: date(2026, 7, 24), Quarter: "Q1 FY27",
			RevenueCr: 5748, RevenueYoYPct: -8.12, PATCr: 148, PATYoYPct: -61.6,
			SourceURL: "https://www.freepressjournal.in (ACC Q1 FY27), sahi.com",
			Notes:     "Both revenue and PAT down sharply YoY -- a genuine weak quarter, not a margin or one-off story. Advancing amalgamation with Ambuja Cements.",
		},
		{
			Symbol: "ACE", ReportDate: date(2026, 7, 21), Quarter: "Q1 FY27",
			RevenueCr: 785.68, RevenueYoYPct: 20.49, PATCr: 119.49, PATYoYPct: 22.28,
			SourceURL: "https://www.equitybulls.com (Action Construction Equipment Q1 FY27), marketsmojo.com",
			Notes:     "Highest-ever quarterly PAT; EBITDA margin 20.53%, best-ever quarter per one source.",
		},
		{
			Symbol: "ACMESOLAR", ReportDate: date(2026, 7, 29), Quarter: "Q1 FY27",
			RevenueCr: 8575, RevenueYoYPct: 67.8, PATCr: 2353.3, PATYoYPct: 80,
			SourceURL: "https://solarquarter.com (ACME Solar Q1 FY27), whalesbook.com",
			Notes:     "Driven by higher generation from expanding renewable portfolio and BESS (battery storage) contribution.",
		},
		{
			Symbol: "ACUTAAS", ReportDate: date(2026, 7, 31), Quarter: "Q1 FY27",
			RevenueCr: 326.56, RevenueYoYPct: 27.25, PATCr: 62.7, PATYoYPct: 33.6,
			SourceURL: "https://upstox.com (Aether Industries Q1 FY27), business-standard.com",
			Notes:     "Ticker ACUTAAS is Aether Industries. Driven by Contract Exclusive Manufacturing (CEM) and Large Scale Manufacturing demand; shares hit a 52-week high on the print.",
		},
		{
			Symbol: "ADANIENSOL", ReportDate: date(2026, 7, 21), Quarter: "Q1 FY27",
			RevenueCr: 9711.08, RevenueYoYPct: 42.41, PATCr: 1149, PATYoYPct: 124.22,
			SourceURL: "https://www.sahi.com (Adani Energy Solutions Q1 FY27), equitybulls.com",
			Notes:     "Sources gave PAT figures from Rs 1,149-1,236 Cr across different cuts -- used the consolidated PAT paired with the +124.22% YoY figure for internal consistency. Driven by Mumbai HVDC ramp-up and smart-metering rollout.",
		},
		{
			Symbol: "ADANIGREEN", ReportDate: date(2026, 7, 22), Quarter: "Q1 FY27",
			RevenueCr: 4431, RevenueYoYPct: 16.6, PATCr: 983, PATYoYPct: 19.3,
			SourceURL: "https://upstox.com (Adani Green Energy Q1 FY27), business-standard.com",
			Notes:     "Sources were inconsistent here (some headlines said +29% revenue / +135% PAT, which don't match the underlying Rs figures cited in the same articles) -- used the self-consistent pair (Rs 4,431 Cr vs Rs 3,800 Cr = +16.6%; Rs 983 Cr vs Rs 824 Cr = +19.3%) rather than the headline percentages. Operational capacity +27% YoY to 20.1 GW. Stock fell ~3-4% on the day despite the beat.",
		},
		{
			Symbol: "ADANIPOWER", ReportDate: date(2026, 7, 22), Quarter: "Q1 FY27",
			RevenueCr: 18902, RevenueYoYPct: 34, PATCr: 4866.60, PATYoYPct: 47.2,
			SourceURL: "https://www.republicworld.com (Adani Power Q1 FY27), business-standard.com",
			Notes:     "Board approved a Rs 15,000 Cr QIP fundraise same day -- a confound worth remembering, same pattern as JBMA's securities issue.",
		},
		// --- Batch 2 of the full-501 expansion (18 stocks) ---
		{
			Symbol: "AEGISLOG", ReportDate: date(2026, 8, 6), Quarter: "Q1 FY27",
			RevenueCr: 2356.86, RevenueYoYPct: 37, PATCr: 484.44, PATYoYPct: 268.9,
			SourceURL: "https://tradebrains.in (Aegis Logistics Q1 FY27), sahi.com",
			Notes:     "Sources gave several different PAT figures (Rs 484/545/664 Cr) likely mixing standalone/consolidated/other-quarter snippets -- used the consolidated figure explicitly paired with the revenue growth in the same sentence. Driven by gas terminalling; EBITDA margin doubled to 30.3%.",
		},
		{
			Symbol: "AEGISVOPAK", ReportDate: date(2026, 8, 7), Quarter: "Q1 FY27",
			RevenueCr: 233.8, RevenueYoYPct: 12.4, PATCr: 66.1, PATYoYPct: -6.9,
			SourceURL: "https://www.sahi.com (Aegis Vopak Terminals Q1 FY27), investywise.com",
			Notes:     "Revenue/EBITDA grew but PAT dipped slightly -- a margin-below-the-EBITDA-line story (financing/depreciation on new capacity, most likely, for a recently-listed terminals entity).",
		},
		{
			Symbol: "AFCONS", ReportDate: date(2026, 8, 7), Quarter: "Q1 FY27",
			RevenueCr: 2727, RevenueYoYPct: -20.3, PATCr: 30, PATYoYPct: 0,
			SourceURL: "https://www.investywise.com (Afcons Infrastructure Q1 FY27), scanx.trade",
			Notes:     "PAT collapsed to just Rs 30 Cr; no clean prior-year PAT figure found in this search pass to compute YoY% -- left at 0, not a real 0%. Revenue also fell sharply (total income Rs 2,727 Cr vs Rs 3,419 Cr) despite a healthy Rs 43,290 Cr order book -- a genuine weak-execution quarter, not a one-off.",
		},
		{
			Symbol: "AFFLE", ReportDate: date(2026, 8, 8), Quarter: "Q1 FY27",
			RevenueCr: 747.16, RevenueYoYPct: 20.4, PATCr: 128.44, PATYoYPct: 21.7,
			SourceURL: "https://www.sahi.com (Affle Q1 FY27), whalesbook.com",
			Notes:     "Formerly reported as Affle India, now Affle 3i.",
		},
		{
			Symbol: "AIAENG", ReportDate: date(2026, 8, 12), Quarter: "Q1 FY27",
			RevenueCr: 1168.02, RevenueYoYPct: 12.4, PATCr: 300.99, PATYoYPct: -1.3,
			SourceURL: "https://scanx.trade (AIA Engineering Q1 FY27), investywise.com",
			Notes:     "Revenue up, PAT essentially flat/slightly down -- capex guidance raised to Rs 350-400 Cr on a large South America mine breakthrough not yet reflected in the P&L.",
		},
		{
			Symbol: "AIIL", ReportDate: date(2026, 7, 20), Quarter: "Q1 FY27",
			RevenueCr: 1469.54, RevenueYoYPct: 20.94, PATCr: 1108.22, PATYoYPct: 17.52,
			SourceURL: "https://www.equitybulls.com (Authum Investment & Infrastructure Q1 FY27), sahi.com",
			Notes:     "Ticker AIIL is Authum Investment & Infrastructure. One source flagged 'stellar profit surge masks underlying concerns' -- not investigated further here.",
		},
		{
			Symbol: "AJANTPHARM", ReportDate: date(2026, 7, 30), Quarter: "Q1 FY27",
			RevenueCr: 1626, RevenueYoYPct: 25, PATCr: 334, PATYoYPct: 31,
			SourceURL: "https://www.investywise.com (Ajanta Pharma Q1 FY27), scanx.trade",
			Notes:     "US Generic revenue +57% YoY was the main driver; Rs 32/share interim dividend declared same day.",
		},
		{
			Symbol: "ALKEM", ReportDate: date(2026, 8, 14), Quarter: "Q1 FY27",
			RevenueCr: 3740, RevenueYoYPct: 11, PATCr: 520, PATYoYPct: -21,
			SourceURL: "https://upstox.com (Alkem Laboratories Q1 FY27), sahi.com",
			Notes:     "Revenue grew but PAT fell on a tax hit -- EBITDA actually grew 4%, so this is a below-the-operating-line story specifically, not an operating miss.",
		},
		{
			Symbol: "AMBER", ReportDate: date(2026, 8, 14), Quarter: "Q1 FY27",
			RevenueCr: 3888, RevenueYoYPct: 13, PATCr: 3.09, PATYoYPct: -97,
			SourceURL: "https://www.tipranks.com (Amber Enterprises Q1 FY27), scanx.trade",
			Notes:     "Headline PAT collapsed on a one-off exceptional loss tied to the Ascent Circuits acquisition; adjusted (ex-exceptional) PAT actually grew +19% to Rs 126 Cr. Used the headline (unadjusted) figure for consistency with how every other row in this dataset is sourced, but this is one of the largest headline-vs-adjusted gaps in the set -- read the Total column for this row with real caution.",
		},
		{
			Symbol: "AMBUJACEM", ReportDate: date(2026, 7, 28), Quarter: "Q1 FY27",
			RevenueCr: 9474, RevenueYoYPct: -7.5, PATCr: 577, PATYoYPct: -34,
			SourceURL: "https://upstox.com (Ambuja Cements Q1 FY27), univest.in",
			Notes:     "Sources gave slightly different PAT cuts (Rs 504/577/660 Cr) -- used the pairing stated alongside the revenue figure in the same article. Both revenue and PAT down YoY, consistent with ACC's weak cement-sector quarter above.",
		},
		{
			Symbol: "ANANDRATHI", ReportDate: date(2026, 7, 10), Quarter: "Q1 FY27",
			RevenueCr: 336, RevenueYoYPct: 18, PATCr: 116, PATYoYPct: 24,
			SourceURL: "https://www.freepressjournal.in (Anand Rathi Wealth Q1 FY27), investing.com",
			Notes:     "RevenueCr excludes fair-value gains/ESOP expense per the company's own adjusted metric. Other sources quoted a noisier 73.6% PAT / 52.1% revenue pairing off a different (unadjusted, total-income) base -- used the more precise, company-defined figures instead. AUM crossed Rs 1 lakh Cr for the first time.",
		},
		{
			Symbol: "ANANTRAJ", ReportDate: date(2026, 8, 8), Quarter: "Q1 FY27",
			RevenueCr: 631.40, RevenueYoYPct: 6.58, PATCr: 149.19, PATYoYPct: 18.5,
			SourceURL: "https://www.sahi.com (Anant Raj Q1 FY27), scanx.trade",
			Notes:     "Approved a composite scheme to demerge its cloud-services unit same quarter -- a structural change worth remembering when interpreting future quarters for this symbol.",
		},
		{
			Symbol: "ANGELONE", ReportDate: date(2026, 7, 16), Quarter: "Q1 FY27",
			RevenueCr: 1429.69, RevenueYoYPct: 25.35, PATCr: 231.40, PATYoYPct: 102.14,
			SourceURL: "https://www.business-standard.com (Angel One Q1 FY27), equitybulls.com",
			Notes:     "PAT more than doubled -- credit and wealth-management verticals were the stated growth drivers, a platform-diversification story beyond the core broking business.",
		},
		{
			Symbol: "ANTHEM", ReportDate: date(2026, 7, 21), Quarter: "Q1 FY27",
			RevenueCr: 418.2, RevenueYoYPct: -22.6, PATCr: 119.94, PATYoYPct: 0,
			SourceURL: "https://www.freepressjournal.in (Anthem Biosciences Q1 FY27), equitybulls.com",
			Notes:     "Revenue fell YoY and sequentially on deferred CRDMO deliveries; no clean prior-year PAT figure found to compute YoY% -- left at 0, not a real 0%. PAT margin still expanded to 27.1%, so this reads as a timing/deferral story rather than a demand problem.",
		},
		{
			Symbol: "ANURAS", ReportDate: date(2026, 8, 14), Quarter: "Q1 FY27",
			RevenueCr: 667.5, RevenueYoYPct: 36, PATCr: 51.2, PATYoYPct: 6,
			SourceURL: "https://businessupturn.com (Anupam Rasayan Q1 FY27), multibagg.ai",
			Notes:     "Revenue growth far outpaced PAT growth -- a margin-compression story despite the strong top line. Ticker ANURAS is Anupam Rasayan.",
		},
		{
			Symbol: "APARINDS", ReportDate: date(2026, 7, 25), Quarter: "Q1 FY27",
			RevenueCr: 6591.06, RevenueYoYPct: 29.15, PATCr: 467.45, PATYoYPct: 78,
			SourceURL: "https://www.sahi.com (Apar Industries Q1 FY27), equitybulls.com",
			Notes:     "Record June-quarter revenue, EBITDA, and profit -- broad-based growth across Conductors, Specialty Oils, and Power/Telecom Cables segments.",
		},
		{
			Symbol: "APLAPOLLO", ReportDate: date(2026, 8, 1), Quarter: "Q1 FY27",
			RevenueCr: 5606.71, RevenueYoYPct: 8.5, PATCr: 263.11, PATYoYPct: 10.9,
			SourceURL: "https://www.freepressjournal.in (APL Apollo Tubes Q1 FY27), business-standard.com",
			Notes:     "Sales volume actually fell 6% YoY despite revenue/PAT growth -- a price/mix story, not a volume one.",
		},
		{
			Symbol: "APOLLOTYRE", ReportDate: date(2026, 8, 7), Quarter: "Q1 FY27",
			RevenueCr: 7397.79, RevenueYoYPct: 12.8, PATCr: 348.87, PATYoYPct: 2608.6,
			SourceURL: "https://www.business-standard.com (Apollo Tyres Q1 FY27), kotakneo.com",
			Notes:     "Extreme PAT growth is a low-base artifact -- prior-year Q1 FY26 PAT was only Rs 12.88 Cr (an unusually weak comparison quarter), not a genuine 26x improvement in the underlying business. Treat this row's PAT YoY like NEULANDLAB/ETERNAL's outlier percentages, not as a normal growth rate. Operating margin actually compressed to 11.7% on higher raw material costs.",
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
