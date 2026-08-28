// Package store: IntradayCandleStore persists sub-daily Candle records
// (5-minute bars and similar) for day-trading strategy work. Kept as a
// separate table from the daily `candles` table (see candle_store.go) rather
// than adding an interval column there — the daily pipeline is
// well-established and every existing caller assumes one row per symbol per
// day; this keeps that assumption intact and the intraday pipeline fully
// additive.
//
// IST/UTC gotcha (same one this project has hit before with daily candles):
// Kite returns intraday timestamps as IST wall-clock time, e.g. 09:15 IST for
// the market open. Candle.Timestamp is stored/decoded as UTC, so a stored bar
// prints as 03:45 UTC if you format it directly — the DATE doesn't shift
// (unlike the midnight-anchored daily case), but the CLOCK TIME is off by
// 5:30. Any code that needs the true IST time-of-day (e.g. "is this the last
// bar before the 15:30 close", a same-day MIS flatten check) must add
// `5*time.Hour + 30*time.Minute` before reading the hour/minute — see
// internal/earnings/reaction.go's ISTOffset for the established pattern.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

type IntradayCandleStore struct {
	db *sql.DB
}

func NewIntradayCandleStore(db *sql.DB) *IntradayCandleStore {
	return &IntradayCandleStore{db: db}
}

func (s *IntradayCandleStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS intraday_candles (
			id        BIGSERIAL PRIMARY KEY,
			symbol    TEXT        NOT NULL,
			interval  TEXT        NOT NULL,
			timestamp TIMESTAMPTZ NOT NULL,
			open      NUMERIC(18,6) NOT NULL,
			high      NUMERIC(18,6) NOT NULL,
			low       NUMERIC(18,6) NOT NULL,
			close     NUMERIC(18,6) NOT NULL,
			volume    BIGINT        NOT NULL,
			UNIQUE (symbol, interval, timestamp)
		);
		CREATE INDEX IF NOT EXISTS idx_intraday_candles_symbol_interval_ts
			ON intraday_candles (symbol, interval, timestamp DESC);
	`)
	if err != nil {
		return fmt.Errorf("intraday candle migrate: %w", err)
	}
	return nil
}

// UpsertCandles inserts candles for the given interval and updates OHLCV on
// conflict. Safe to run repeatedly — existing rows are overwritten with fresh
// data (a bar Kite returns before its window fully closes can still move).
func (s *IntradayCandleStore) UpsertCandles(ctx context.Context, interval string, candles []models.Candle) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO intraday_candles (symbol, interval, timestamp, open, high, low, close, volume)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (symbol, interval, timestamp) DO UPDATE SET
			open   = EXCLUDED.open,
			high   = EXCLUDED.high,
			low    = EXCLUDED.low,
			close  = EXCLUDED.close,
			volume = EXCLUDED.volume
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, c := range candles {
		if _, err := stmt.ExecContext(ctx, c.Symbol, interval, c.Timestamp, c.Open, c.High, c.Low, c.Close, c.Volume); err != nil {
			return fmt.Errorf("upsert intraday candle %s@%s: %w", c.Symbol, c.Timestamp, err)
		}
	}
	return tx.Commit()
}

type IntradayCandleFilter struct {
	From  *time.Time
	To    *time.Time
	Limit int
}

func (s *IntradayCandleStore) GetCandles(ctx context.Context, symbol, interval string, f IntradayCandleFilter) ([]models.Candle, error) {
	query := `SELECT id, symbol, timestamp, open, high, low, close, volume FROM intraday_candles WHERE symbol = $1 AND interval = $2`
	args := []any{symbol, interval}
	i := 3

	if f.From != nil {
		query += fmt.Sprintf(" AND timestamp >= $%d", i)
		args = append(args, *f.From)
		i++
	}
	if f.To != nil {
		query += fmt.Sprintf(" AND timestamp <= $%d", i)
		args = append(args, *f.To)
		i++
	}
	query += " ORDER BY timestamp ASC"
	if f.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", i)
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Candle
	for rows.Next() {
		var c models.Candle
		if err := rows.Scan(&c.ID, &c.Symbol, &c.Timestamp, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetLatest returns the most recent candle for symbol+interval, or nil if none.
func (s *IntradayCandleStore) GetLatest(ctx context.Context, symbol, interval string) (*models.Candle, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, symbol, timestamp, open, high, low, close, volume
		FROM intraday_candles WHERE symbol = $1 AND interval = $2
		ORDER BY timestamp DESC LIMIT 1
	`, symbol, interval)

	var c models.Candle
	if err := row.Scan(&c.ID, &c.Symbol, &c.Timestamp, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}
