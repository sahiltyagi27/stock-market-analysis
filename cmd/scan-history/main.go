// Command scan-history queries the scan_results table written by live-scan
// and prints a compact, colored summary of past signals.
//
// Usage:
//
//	go run ./cmd/scan-history                       # last 100 rows
//	go run ./cmd/scan-history --today               # today's signals
//	go run ./cmd/scan-history --symbol HDFCBANK     # one symbol
//	go run ./cmd/scan-history --min-streak 3        # held 3+ consecutive scans
//	go run ./cmd/scan-history --min-score 80        # high-quality signals only
//
// Required: same PostgreSQL env vars as live-scan (DB_HOST, DB_PORT, etc.).
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
	"github.com/sahiltyagi27/stock-market-analysis/internal/display"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
)

var ist *time.Location

func init() {
	var err error
	ist, err = time.LoadLocation("Asia/Kolkata")
	if err != nil {
		log.Fatalf("load IST timezone: %v", err)
	}
}

func main() {
	today     := flag.Bool("today", false, "show only today's signals (IST)")
	symbol    := flag.String("symbol", "", "filter to a single symbol")
	minStreak := flag.Int("min-streak", 0, "only signals that appeared in N+ consecutive scans")
	minScore  := flag.Float64("min-score", 0, "only signals with score ≥ this value")
	limit     := flag.Int("limit", 100, "max rows to show")
	from      := flag.String("from", "", "from date, YYYY-MM-DD (IST)")
	to        := flag.String("to", "", "to date, YYYY-MM-DD (IST)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	rs, err := store.NewScanResultStore(db)
	if err != nil {
		log.Fatalf("scan result store: %v", err)
	}

	f := store.ScanResultFilter{
		Symbol:    strings.ToUpper(strings.TrimSpace(*symbol)),
		MinStreak: *minStreak,
		MinScore:  *minScore,
		Limit:     *limit,
	}

	if *today {
		now := time.Now().In(ist)
		y, m, d := now.Date()
		start := time.Date(y, m, d, 0, 0, 0, 0, ist)
		end   := time.Date(y, m, d, 23, 59, 59, 0, ist)
		f.From = &start
		f.To   = &end
	} else {
		if *from != "" {
			t, err := time.ParseInLocation("2006-01-02", *from, ist)
			if err != nil {
				log.Fatalf("invalid --from %q: %v", *from, err)
			}
			f.From = &t
		}
		if *to != "" {
			t, err := time.ParseInLocation("2006-01-02", *to, ist)
			if err != nil {
				log.Fatalf("invalid --to %q: %v", *to, err)
			}
			end := t.Add(24*time.Hour - time.Second)
			f.To = &end
		}
	}

	rows, err := rs.Query(ctx, f)
	if err != nil {
		log.Fatalf("query: %v", err)
	}

	printHistory(rows, f)
}

func printHistory(rows []store.ScanResultRow, f store.ScanResultFilter) {
	// Header.
	title := "Scan History"
	if f.Symbol != "" {
		title += " — " + f.Symbol
	}
	if f.From != nil {
		title += " — " + f.From.In(ist).Format("02-Jan-2006")
		if f.To != nil && f.To.In(ist).Format("02-Jan-2006") != f.From.In(ist).Format("02-Jan-2006") {
			title += " → " + f.To.In(ist).Format("02-Jan-2006")
		}
	}
	banner := fmt.Sprintf("━━━  %s  ━━━", title)
	fmt.Printf("\n%s\n\n", display.BoldCyan.Sprint(banner))

	if len(rows) == 0 {
		fmt.Println("  No results found.")
		fmt.Printf("\n%s\n", display.BoldCyan.Sprint(strings.Repeat("━", len(banner))))
		return
	}

	// Column header.
	fmt.Printf("  %s  %-16s %-10s  %s  %-5s  %-8s  %s\n",
		display.Dim.Sprint("TIME    "),
		display.Dim.Sprint("SYMBOL"),
		display.Dim.Sprint("PRICE"),
		display.Dim.Sprint("SCORE"),
		display.Dim.Sprint("R/R"),
		display.Dim.Sprint("STREAK"),
		display.Dim.Sprint("RS vs NIFTY"))
	fmt.Printf("  %s\n", display.Dim.Sprint(strings.Repeat("─", 72)))

	symbolSet := make(map[string]struct{})

	for _, r := range rows {
		symbolSet[r.Symbol] = struct{}{}

		timeStr  := r.ScannedAt.In(ist).Format("15:04:05")
		scoreStr := display.TotalScore(r.Score)

		// Streak tag.
		streakStr := ""
		switch {
		case r.IsNew:
			streakStr = display.BoldGreen.Sprint("[NEW]")
		case r.Streak > 1:
			streakStr = display.Cyan.Sprintf("×%d", r.Streak)
		default:
			streakStr = display.Dim.Sprint("×1")
		}

		// RS.
		rsStr := display.Dim.Sprint("n/a")
		if r.RelStrength != nil {
			rsStr = display.Sign(*r.RelStrength, "%+.2f%%")
		}

		fmt.Printf("  %s  %s  ₹%-9.2f  %s/100  %-5.2f  %-8s  %s\n",
			display.Dim.Sprint(timeStr),
			display.BoldWhite.Sprint(fmt.Sprintf("%-16s", r.Symbol)),
			r.Price,
			scoreStr,
			r.RR,
			streakStr,
			rsStr)
	}

	fmt.Printf("\n  %s\n", display.Dim.Sprint(strings.Repeat("─", 72)))
	fmt.Printf("  %s\n",
		display.Dim.Sprintf("Total: %d rows  │  %d unique symbol(s)", len(rows), len(symbolSet)))
	fmt.Printf("%s\n", display.BoldCyan.Sprint(strings.Repeat("━", len(banner))))
}
