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
