// Package store handles persistence of Candle records in PostgreSQL.
// It performs schema migration on startup and exposes bulk-upsert and query operations.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

type CandleStore struct {
	db *sql.DB
}

func NewCandleStore(db *sql.DB) *CandleStore {
	return &CandleStore{db: db}
}

func (s *CandleStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS candles (
			id        BIGSERIAL PRIMARY KEY,
			symbol    TEXT        NOT NULL,
			timestamp TIMESTAMPTZ NOT NULL,
			open      NUMERIC(18,6) NOT NULL,
			high      NUMERIC(18,6) NOT NULL,
			low       NUMERIC(18,6) NOT NULL,
			close     NUMERIC(18,6) NOT NULL,
			volume    BIGINT        NOT NULL,
			UNIQUE (symbol, timestamp)
		);
		CREATE INDEX IF NOT EXISTS idx_candles_symbol_ts ON candles (symbol, timestamp DESC);
	`)
	return err
}

// BulkUpsert inserts candles, skipping duplicates by (symbol, timestamp).
func (s *CandleStore) BulkUpsert(ctx context.Context, candles []models.Candle) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO candles (symbol, timestamp, open, high, low, close, volume)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (symbol, timestamp) DO NOTHING
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, c := range candles {
		if _, err := stmt.ExecContext(ctx, c.Symbol, c.Timestamp, c.Open, c.High, c.Low, c.Close, c.Volume); err != nil {
			return fmt.Errorf("upsert candle %s@%s: %w", c.Symbol, c.Timestamp, err)
		}
	}
	return tx.Commit()
}

type CandleFilter struct {
	From  *time.Time
	To    *time.Time
	Limit int
}

func (s *CandleStore) GetCandles(ctx context.Context, symbol string, f CandleFilter) ([]models.Candle, error) {
	query := `SELECT id, symbol, timestamp, open, high, low, close, volume FROM candles WHERE symbol = $1`
	args := []any{symbol}
	i := 2

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

func (s *CandleStore) GetLatest(ctx context.Context, symbol string) (*models.Candle, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, symbol, timestamp, open, high, low, close, volume
		FROM candles WHERE symbol = $1
		ORDER BY timestamp DESC LIMIT 1
	`, symbol)

	var c models.Candle
	if err := row.Scan(&c.ID, &c.Symbol, &c.Timestamp, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}
