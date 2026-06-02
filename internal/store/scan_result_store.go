package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ScanResultRow is one signal's worth of data written after every live-scan run.
type ScanResultRow struct {
	ScannedAt   time.Time
	Symbol      string
	Price       float64
	Score       float64
	Trend       string
	RR          float64
	EMA50       float64
	EMA200      float64
	RelStrength *float64 // nil when NIFTY tick is unavailable
	IsNew       bool     // true → symbol was absent from the previous scan
	Streak      int      // consecutive scan count (1 = first appearance)
}

// ScanResultStore persists live-scan signals to PostgreSQL so they can be
// reviewed and back-tested after the fact.
type ScanResultStore struct {
	db *sql.DB
}

// NewScanResultStore creates the scan_results table (if it does not already
// exist) and returns a ready-to-use store.
func NewScanResultStore(db *sql.DB) (*ScanResultStore, error) {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS scan_results (
			id           BIGSERIAL     PRIMARY KEY,
			scanned_at   TIMESTAMPTZ   NOT NULL,
			symbol       TEXT          NOT NULL,
			price        NUMERIC(12,4) NOT NULL,
			score        NUMERIC(6,2)  NOT NULL,
			trend        TEXT          NOT NULL,
			rr           NUMERIC(8,4)  NOT NULL,
			ema50        NUMERIC(12,4),
			ema200       NUMERIC(12,4),
			rel_strength NUMERIC(8,4),
			is_new       BOOLEAN       NOT NULL DEFAULT FALSE,
			streak       INT           NOT NULL DEFAULT 1
		);
		CREATE INDEX IF NOT EXISTS idx_scan_results_time
			ON scan_results (scanned_at DESC);
		CREATE INDEX IF NOT EXISTS idx_scan_results_symbol
			ON scan_results (symbol, scanned_at DESC);
	`)
	if err != nil {
		return nil, fmt.Errorf("scan_results migrate: %w", err)
	}
	return &ScanResultStore{db: db}, nil
}

// ScanResultFilter narrows the rows returned by Query.
// Zero-value fields are ignored (no filter applied for that field).
type ScanResultFilter struct {
	Symbol    string     // exact match; empty = all symbols
	From      *time.Time // inclusive lower bound on scanned_at
	To        *time.Time // inclusive upper bound on scanned_at
	MinStreak int        // only rows where streak >= MinStreak
	MinScore  float64    // only rows where score >= MinScore
	Limit     int        // max rows (default 100 when ≤ 0)
}

// Query returns scan result rows matching the filter, ordered by
// scanned_at DESC, score DESC.
func (s *ScanResultStore) Query(ctx context.Context, f ScanResultFilter) ([]ScanResultRow, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT scanned_at, symbol, price, score, trend, rr,
		       ema50, ema200, rel_strength, is_new, streak
		FROM scan_results
		WHERE ($1 = '' OR symbol = $1)
		  AND ($2::timestamptz IS NULL OR scanned_at >= $2)
		  AND ($3::timestamptz IS NULL OR scanned_at <= $3)
		  AND score    >= $4
		  AND streak   >= $5
		ORDER BY scanned_at DESC, score DESC
		LIMIT $6
	`, f.Symbol, timeOrNil(f.From), timeOrNil(f.To),
		f.MinScore, f.MinStreak, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ScanResultRow
	for rows.Next() {
		var r ScanResultRow
		var rs *float64
		if err := rows.Scan(
			&r.ScannedAt, &r.Symbol, &r.Price, &r.Score, &r.Trend, &r.RR,
			&r.EMA50, &r.EMA200, &rs, &r.IsNew, &r.Streak,
		); err != nil {
			return nil, err
		}
		r.RelStrength = rs
		out = append(out, r)
	}
	return out, rows.Err()
}

func timeOrNil(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return *t
}

// ScanStateSnapshot holds the in-memory scan state reconstructed from the
// most recent scan run today. Used to restore a live-scan process after a
// restart so streak counts and new-signal detection continue seamlessly.
type ScanStateSnapshot struct {
	// PrevSymbols is the set of symbols that appeared in the last scan run.
	PrevSymbols map[string]bool
	// Streaks maps each symbol to its consecutive-scan count at that run.
	Streaks map[string]int
}

// LatestTodayScanState returns a snapshot from the most recent scan run
// on today's IST calendar date. Returns (nil, nil) when no scan has been
// recorded today — the caller should start fresh.
//
// "Today" is evaluated in the provided IST location so that a midnight
// restart does not bleed yesterday's streaks into the new session.
func (s *ScanResultStore) LatestTodayScanState(ctx context.Context, ist *time.Location) (*ScanStateSnapshot, error) {
	now := time.Now().In(ist)
	y, m, d := now.Date()
	dayStart := time.Date(y, m, d, 0, 0, 0, 0, ist)
	dayEnd   := dayStart.Add(24 * time.Hour)

	// Find the most recent scanned_at within today's IST window.
	// MAX() over an empty set returns SQL NULL, so scan into sql.NullTime
	// rather than time.Time (which cannot hold NULL).
	var latest sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(scanned_at)
		FROM scan_results
		WHERE scanned_at >= $1 AND scanned_at < $2
	`, dayStart, dayEnd).Scan(&latest)
	if err != nil {
		return nil, fmt.Errorf("latest today scan: %w", err)
	}
	if !latest.Valid {
		return nil, nil // no scan recorded today
	}
	latestAt := latest.Time

	// Load all rows from that specific scan run.
	rows, err := s.db.QueryContext(ctx, `
		SELECT symbol, streak
		FROM scan_results
		WHERE scanned_at = $1
		ORDER BY score DESC
	`, latestAt)
	if err != nil {
		return nil, fmt.Errorf("load scan state rows: %w", err)
	}
	defer rows.Close()

	snap := &ScanStateSnapshot{
		PrevSymbols: make(map[string]bool),
		Streaks:     make(map[string]int),
	}
	for rows.Next() {
		var sym string
		var streak int
		if err := rows.Scan(&sym, &streak); err != nil {
			return nil, err
		}
		snap.PrevSymbols[sym] = true
		snap.Streaks[sym] = streak
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(snap.PrevSymbols) == 0 {
		return nil, nil // run existed but had no signals
	}
	return snap, nil
}

// PurgeOlderThan deletes scan_results rows whose scanned_at is before cutoff.
// Returns the number of rows deleted. Safe to call at startup — a full table
// scan is avoided because scanned_at is indexed.
func (s *ScanResultStore) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM scan_results WHERE scanned_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge scan_results: %w", err)
	}
	return res.RowsAffected()
}

// Save bulk-inserts rows for one complete scan run inside a single transaction.
func (s *ScanResultStore) Save(ctx context.Context, rows []ScanResultRow) error {
	if len(rows) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO scan_results
			(scanned_at, symbol, price, score, trend, rr, ema50, ema200, rel_strength, is_new, streak)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rows {
		var rs interface{} // nil maps to SQL NULL
		if r.RelStrength != nil {
			rs = *r.RelStrength
		}
		if _, err := stmt.ExecContext(ctx,
			r.ScannedAt, r.Symbol, r.Price, r.Score, r.Trend, r.RR,
			r.EMA50, r.EMA200, rs, r.IsNew, r.Streak,
		); err != nil {
			return fmt.Errorf("insert scan_result %s: %w", r.Symbol, err)
		}
	}
	return tx.Commit()
}
