package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// EarningsEvent is one quarterly-results declaration for a symbol: the date
// the board approved and announced the numbers, plus the headline
// year-over-year figures needed to judge whether the market's reaction
// matched the fundamentals. Sourced manually from company press releases /
// financial news (NSE/BSE's own filing APIs are not reachable from this
// environment) — see SourceURL for provenance on every row.
type EarningsEvent struct {
	Symbol        string
	ReportDate    time.Time // date the board approved/announced results (IST calendar date)
	Quarter       string    // e.g. "Q1 FY27"
	RevenueCr     float64   // revenue from operations, INR crore
	RevenueYoYPct float64   // YoY revenue growth, %
	PATCr         float64   // profit after tax, INR crore
	PATYoYPct     float64   // YoY PAT growth, %
	EBITDAMarginPct float64 // EBITDA margin, % (0 if not recorded)
	SourceURL     string
	Notes         string
}

// EarningsStore persists quarterly-results declarations so the price
// reaction around each one can be studied and re-studied without re-fetching
// the same news search every time.
type EarningsStore struct {
	db *sql.DB
}

// NewEarningsStore creates the earnings_events table (if it does not already
// exist) and returns a ready-to-use store.
func NewEarningsStore(db *sql.DB) (*EarningsStore, error) {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS earnings_events (
			id                BIGSERIAL     PRIMARY KEY,
			symbol            TEXT          NOT NULL,
			report_date       DATE          NOT NULL,
			quarter           TEXT          NOT NULL,
			revenue_cr        NUMERIC(14,2),
			revenue_yoy_pct   NUMERIC(8,2),
			pat_cr            NUMERIC(14,2),
			pat_yoy_pct       NUMERIC(8,2),
			ebitda_margin_pct NUMERIC(6,2),
			source_url        TEXT,
			notes             TEXT,
			UNIQUE (symbol, report_date)
		);
		CREATE INDEX IF NOT EXISTS idx_earnings_events_symbol
			ON earnings_events (symbol, report_date DESC);
	`)
	if err != nil {
		return nil, fmt.Errorf("earnings_events migrate: %w", err)
	}
	return &EarningsStore{db: db}, nil
}

// Upsert inserts or replaces one earnings event, keyed on (symbol, report_date).
func (s *EarningsStore) Upsert(ctx context.Context, e EarningsEvent) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO earnings_events
			(symbol, report_date, quarter, revenue_cr, revenue_yoy_pct, pat_cr, pat_yoy_pct, ebitda_margin_pct, source_url, notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (symbol, report_date) DO UPDATE SET
			quarter = EXCLUDED.quarter,
			revenue_cr = EXCLUDED.revenue_cr,
			revenue_yoy_pct = EXCLUDED.revenue_yoy_pct,
			pat_cr = EXCLUDED.pat_cr,
			pat_yoy_pct = EXCLUDED.pat_yoy_pct,
			ebitda_margin_pct = EXCLUDED.ebitda_margin_pct,
			source_url = EXCLUDED.source_url,
			notes = EXCLUDED.notes
	`, e.Symbol, e.ReportDate, e.Quarter, e.RevenueCr, e.RevenueYoYPct,
		e.PATCr, e.PATYoYPct, e.EBITDAMarginPct, e.SourceURL, e.Notes)
	if err != nil {
		return fmt.Errorf("upsert earnings_event %s %s: %w", e.Symbol, e.Quarter, err)
	}
	return nil
}

// EarningsFilter narrows the rows returned by Query. Zero-value fields are
// ignored (no filter applied for that field).
type EarningsFilter struct {
	Symbol string // exact match; empty = all symbols
}

// Query returns earnings events matching the filter, ordered by
// report_date ASC.
func (s *EarningsStore) Query(ctx context.Context, f EarningsFilter) ([]EarningsEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT symbol, report_date, quarter, revenue_cr, revenue_yoy_pct,
		       pat_cr, pat_yoy_pct, ebitda_margin_pct, source_url, notes
		FROM earnings_events
		WHERE ($1 = '' OR symbol = $1)
		ORDER BY report_date ASC
	`, f.Symbol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EarningsEvent
	for rows.Next() {
		var e EarningsEvent
		if err := rows.Scan(&e.Symbol, &e.ReportDate, &e.Quarter, &e.RevenueCr,
			&e.RevenueYoYPct, &e.PATCr, &e.PATYoYPct, &e.EBITDAMarginPct,
			&e.SourceURL, &e.Notes); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
