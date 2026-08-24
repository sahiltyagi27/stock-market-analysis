// Command earnings-dashboard serves a local, live view of the
// earnings-reaction data: a single JSON API endpoint backed by a direct
// Postgres query on every request (no caching), plus the dashboard page
// itself. Unlike the published Artifact snapshot, this always reflects
// whatever is currently in earnings_events -- refresh the browser (or
// click the page's Refresh button) after running
// `go run ./cmd/earnings-reaction --seed` and the new data shows up with
// no republish step.
//
//	go run ./cmd/earnings-dashboard
//	go run ./cmd/earnings-dashboard --port 9000
//
// Binds to 127.0.0.1 only -- this is a personal read-only view of local
// research data, not a service, and has no reason to be reachable from the
// network.
package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	_ "github.com/lib/pq"
	"github.com/sahiltyagi27/stock-market-analysis/config"
	"github.com/sahiltyagi27/stock-market-analysis/internal/earnings"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
)

//go:embed dashboard.html
var dashboardFS embed.FS

// reactionRow is the JSON shape served at /api/reactions -- one row per
// stored earnings event, with the computed price reaction folded in so the
// frontend doesn't need to know about candles or trading-day math at all.
type reactionRow struct {
	Symbol      string  `json:"symbol"`
	Sector      string  `json:"sector"`
	ReportDate  string  `json:"reportDate"`
	Quarter     string  `json:"quarter"`
	RevenueCr   float64 `json:"revenueCr"`
	RevenueYoY  float64 `json:"revenueYoy"`
	PATCr       float64 `json:"patCr"`
	PATYoY      float64 `json:"patYoy"`
	Notes       string  `json:"notes"`
	WeekBefore  float64 `json:"weekBefore"`
	PostWeek    float64 `json:"postWeek"`
	Total       float64 `json:"total"`
	HasReaction bool    `json:"hasReaction"`
}

// moverRow is the JSON shape served at /api/movers -- one row per symbol in
// the watchlist, showing its most recent daily candle and the day-over-day
// change against the prior candle.
type moverRow struct {
	Symbol     string  `json:"symbol"`
	Sector     string  `json:"sector"`
	Date       string  `json:"date"`
	Open       float64 `json:"open"`
	High       float64 `json:"high"`
	Low        float64 `json:"low"`
	Close      float64 `json:"close"`
	PrevClose  float64 `json:"prevClose"`
	ChangePct  float64 `json:"changePct"`
	Volume     int64   `json:"volume"`
	HasChange  bool    `json:"hasChange"`
}

func main() {
	port := flag.Int("port", 7845, "port to listen on (bound to 127.0.0.1 only)")
	noBrowser := flag.Bool("no-browser", false, "don't auto-open the dashboard in the default browser")
	symbolsFile := flag.String("symbols", "config/symbols.txt", "path to watchlist file (for the daily-movers table)")
	sectorMapPath := flag.String("sector-map", "config/sector-map.csv", "CSV mapping stock symbols to sector index")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	db, err := sql.Open("postgres", cfg.DSN())
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	defer db.Close()

	es, err := store.NewEarningsStore(db)
	if err != nil {
		log.Fatalf("earnings store: %v", err)
	}
	cs := store.NewCandleStore(db)

	symbols, err := config.LoadSymbols(*symbolsFile)
	if err != nil {
		log.Fatalf("load symbols: %v", err)
	}
	sectorMap, err := config.LoadSectorMap(*sectorMapPath)
	if err != nil {
		log.Printf("warn: sector map unavailable (%v) -- sector column/filter will be empty", err)
		sectorMap = map[string]string{}
	}

	dashboardHTML, err := dashboardFS.ReadFile("dashboard.html")
	if err != nil {
		log.Fatalf("read embedded dashboard.html: %v", err)
	}

	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(dashboardHTML)
	})
	r.Get("/api/reactions", func(w http.ResponseWriter, req *http.Request) {
		rows, err := fetchReactions(req.Context(), es, cs, sectorMap)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, rows)
	})
	r.Get("/api/movers", func(w http.ResponseWriter, req *http.Request) {
		rows, err := fetchMovers(req.Context(), cs, symbols, sectorMap)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, rows)
	})

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	url := "http://" + addr

	go func() {
		log.Printf("earnings dashboard listening on %s", url)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	if !*noBrowser {
		if err := openBrowser(url); err != nil {
			fmt.Printf("Could not open browser automatically -- open this URL manually:\n%s\n", url)
		}
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
}

// fetchReactions queries earnings_events fresh on every call and computes
// each row's price reaction from the candles table -- there is no caching,
// so every request reflects the current DB state.
func fetchReactions(ctx context.Context, es *store.EarningsStore, cs *store.CandleStore, sectorMap map[string]string) ([]reactionRow, error) {
	events, err := es.Query(ctx, store.EarningsFilter{})
	if err != nil {
		return nil, fmt.Errorf("query earnings_events: %w", err)
	}

	rows := make([]reactionRow, 0, len(events))
	for _, e := range events {
		row := reactionRow{
			Symbol: e.Symbol, Sector: sectorMap[e.Symbol],
			ReportDate: e.ReportDate.Format("2006-01-02"), Quarter: e.Quarter,
			RevenueCr: e.RevenueCr, RevenueYoY: e.RevenueYoYPct, PATCr: e.PATCr, PATYoY: e.PATYoYPct,
			Notes: e.Notes,
		}
		if r, _, _ := earnings.Compute(ctx, cs, e); r.OK {
			row.WeekBefore = r.PreWeekPct
			row.PostWeek = r.PostWeekPct
			row.Total = r.TotalPct
			row.HasReaction = true
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// fetchMovers queries the two most recent candles for every symbol in the
// watchlist in a single round trip and computes each one's day-over-day
// change. Symbols with fewer than 2 candles (no history yet) are skipped.
func fetchMovers(ctx context.Context, cs *store.CandleStore, symbols []string, sectorMap map[string]string) ([]moverRow, error) {
	pairs, err := cs.LatestTwo(ctx, symbols)
	if err != nil {
		return nil, fmt.Errorf("latest two candles: %w", err)
	}

	rows := make([]moverRow, 0, len(pairs))
	for _, sym := range symbols {
		pair, ok := pairs[sym]
		if !ok || pair[0].Close == 0 {
			continue
		}
		row := moverRow{
			Symbol: sym, Sector: sectorMap[sym],
			Date: earnings.ISTDate(pair[0]).Format("2006-01-02"),
			Open: pair[0].Open, High: pair[0].High, Low: pair[0].Low, Close: pair[0].Close,
			Volume: pair[0].Volume,
		}
		if pair[1].Close > 0 {
			row.PrevClose = pair[1].Close
			row.ChangePct = (pair[0].Close/pair[1].Close - 1) * 100
			row.HasChange = true
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// openBrowser opens url in the OS default browser. Mirrors the same helper
// in cmd/kite-token/main.go.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{url}
	case "linux":
		cmd, args = "xdg-open", []string{url}
	default:
		return fmt.Errorf("unsupported OS — open manually: %s", url)
	}
	return exec.Command(cmd, args...).Start()
}
