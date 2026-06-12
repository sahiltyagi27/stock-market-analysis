package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PaperStore persists a paper-trading account so a forward, day-by-day paper
// session survives restarts and continues across trading days. Four tables:
//
//	paper_account   — singleton: starting capital and current cash
//	paper_positions — currently open paper positions
//	paper_pending   — entries waiting to fill at the next session's open
//	paper_trades    — closed trades (also the strategy-health history source)
type PaperStore struct{ db *sql.DB }

func NewPaperStore(db *sql.DB) *PaperStore { return &PaperStore{db: db} }

// PaperAccount is the singleton cash account.
type PaperAccount struct {
	StartCapital float64
	Cash         float64
	UpdatedAt    time.Time
	// LastEOD is the calendar date of the most recent processed EOD cycle, used
	// to prevent accidentally running the day-end cycle twice. Invalid until the
	// first cycle has run.
	LastEOD sql.NullTime
}

// PaperPosition is one open paper position.
type PaperPosition struct {
	ID        int64
	Symbol    string
	Shares    int64
	Entry     float64
	EntryDate time.Time
	SL        float64
	Target    float64
	ATR       float64
}

// PaperPending is an intended entry, to be filled at the next session's open.
type PaperPending struct {
	ID         int64
	Symbol     string
	SignalDate time.Time
	SL         float64
	Target     float64
	ATR        float64
}

// PaperTrade is a closed paper trade.
type PaperTrade struct {
	Symbol     string
	EntryDate  time.Time
	ExitDate   time.Time
	Entry      float64
	Exit       float64
	Shares     int64
	SL         float64
	RealizedR  float64
	PnL        float64
	Outcome    string
}

func (s *PaperStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS paper_account (
			id            INT PRIMARY KEY DEFAULT 1,
			start_capital NUMERIC(18,4) NOT NULL,
			cash          NUMERIC(18,4) NOT NULL,
			updated_at    TIMESTAMPTZ   NOT NULL DEFAULT now(),
			CONSTRAINT paper_account_singleton CHECK (id = 1)
		);
		CREATE TABLE IF NOT EXISTS paper_positions (
			id         BIGSERIAL PRIMARY KEY,
			symbol     TEXT NOT NULL UNIQUE,
			shares     BIGINT NOT NULL,
			entry      NUMERIC(18,4) NOT NULL,
			entry_date DATE NOT NULL,
			sl         NUMERIC(18,4) NOT NULL,
			target     NUMERIC(18,4) NOT NULL,
			atr        NUMERIC(18,4) NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS paper_pending (
			id          BIGSERIAL PRIMARY KEY,
			symbol      TEXT NOT NULL UNIQUE,
			signal_date DATE NOT NULL,
			sl          NUMERIC(18,4) NOT NULL,
			target      NUMERIC(18,4) NOT NULL,
			atr         NUMERIC(18,4) NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS paper_trades (
			id          BIGSERIAL PRIMARY KEY,
			symbol      TEXT NOT NULL,
			entry_date  DATE NOT NULL,
			exit_date   DATE NOT NULL,
			entry       NUMERIC(18,4) NOT NULL,
			exit        NUMERIC(18,4) NOT NULL,
			shares      BIGINT NOT NULL,
			sl          NUMERIC(18,4) NOT NULL,
			realized_r  NUMERIC(10,4) NOT NULL,
			pnl         NUMERIC(18,4) NOT NULL,
			outcome     TEXT NOT NULL,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_paper_trades_exit ON paper_trades (exit_date);
		ALTER TABLE paper_account ADD COLUMN IF NOT EXISTS last_eod_date DATE;
		ALTER TABLE paper_trades  ADD COLUMN IF NOT EXISTS seeded BOOLEAN NOT NULL DEFAULT FALSE;

		-- Shadow trading keeps the strategy-health gate measuring while it is
		-- closed: hypothetical positions that use no capital but whose realised R
		-- feeds the gate, so it can reopen (fixes the one-way-door lockout). These
		-- mirror paper_positions / paper_pending but consume no cash. Closed shadow
		-- trades are written to paper_trades with shadow = TRUE (they feed the gate
		-- via RecentTradeR but are excluded from account-performance stats).
		ALTER TABLE paper_trades ADD COLUMN IF NOT EXISTS shadow BOOLEAN NOT NULL DEFAULT FALSE;
		CREATE TABLE IF NOT EXISTS paper_shadow_positions (
			id         BIGSERIAL PRIMARY KEY,
			symbol     TEXT NOT NULL UNIQUE,
			entry      NUMERIC(18,4) NOT NULL,
			entry_date DATE NOT NULL,
			sl         NUMERIC(18,4) NOT NULL,
			target     NUMERIC(18,4) NOT NULL,
			atr        NUMERIC(18,4) NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS paper_shadow_pending (
			id          BIGSERIAL PRIMARY KEY,
			symbol      TEXT NOT NULL UNIQUE,
			signal_date DATE NOT NULL,
			sl          NUMERIC(18,4) NOT NULL,
			target      NUMERIC(18,4) NOT NULL,
			atr         NUMERIC(18,4) NOT NULL DEFAULT 0
		);
	`)
	if err != nil {
		return fmt.Errorf("paper migrate: %w", err)
	}
	return nil
}

// Account returns the singleton account, or (nil, nil) if not yet initialised.
func (s *PaperStore) Account(ctx context.Context) (*PaperAccount, error) {
	var a PaperAccount
	err := s.db.QueryRowContext(ctx,
		`SELECT start_capital, cash, updated_at, last_eod_date FROM paper_account WHERE id = 1`).
		Scan(&a.StartCapital, &a.Cash, &a.UpdatedAt, &a.LastEOD)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// InitAccount creates the singleton account with the given starting capital.
func (s *PaperStore) InitAccount(ctx context.Context, startCapital float64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO paper_account (id, start_capital, cash) VALUES (1, $1, $1)
		 ON CONFLICT (id) DO NOTHING`, startCapital)
	return err
}

// SetCash updates the account cash balance.
func (s *PaperStore) SetCash(ctx context.Context, cash float64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE paper_account SET cash = $1, updated_at = now() WHERE id = 1`, cash)
	return err
}

// SetLastEOD records the calendar date (YYYY-MM-DD) of the most recent processed
// day-end cycle. Pass the date already normalised to the session timezone.
func (s *PaperStore) SetLastEOD(ctx context.Context, d time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE paper_account SET last_eod_date = $1 WHERE id = 1`, d.Format("2006-01-02"))
	return err
}

func (s *PaperStore) Positions(ctx context.Context) ([]PaperPosition, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, symbol, shares, entry, entry_date, sl, target, atr
		 FROM paper_positions ORDER BY entry_date, symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaperPosition
	for rows.Next() {
		var p PaperPosition
		if err := rows.Scan(&p.ID, &p.Symbol, &p.Shares, &p.Entry, &p.EntryDate, &p.SL, &p.Target, &p.ATR); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PaperStore) InsertPosition(ctx context.Context, p PaperPosition) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO paper_positions (symbol, shares, entry, entry_date, sl, target, atr)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (symbol) DO UPDATE SET
		   shares=EXCLUDED.shares, entry=EXCLUDED.entry, entry_date=EXCLUDED.entry_date,
		   sl=EXCLUDED.sl, target=EXCLUDED.target, atr=EXCLUDED.atr`,
		p.Symbol, p.Shares, p.Entry, p.EntryDate, p.SL, p.Target, p.ATR)
	return err
}

func (s *PaperStore) DeletePosition(ctx context.Context, symbol string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM paper_positions WHERE symbol = $1`, symbol)
	return err
}

func (s *PaperStore) Pending(ctx context.Context) ([]PaperPending, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, symbol, signal_date, sl, target, atr FROM paper_pending ORDER BY symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaperPending
	for rows.Next() {
		var p PaperPending
		if err := rows.Scan(&p.ID, &p.Symbol, &p.SignalDate, &p.SL, &p.Target, &p.ATR); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PaperStore) InsertPending(ctx context.Context, p PaperPending) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO paper_pending (symbol, signal_date, sl, target, atr) VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (symbol) DO UPDATE SET
		   signal_date=EXCLUDED.signal_date, sl=EXCLUDED.sl, target=EXCLUDED.target, atr=EXCLUDED.atr`,
		p.Symbol, p.SignalDate, p.SL, p.Target, p.ATR)
	return err
}

func (s *PaperStore) ClearPending(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM paper_pending`)
	return err
}

func (s *PaperStore) InsertTrade(ctx context.Context, t PaperTrade) error {
	return s.insertTrade(ctx, t, false)
}

// InsertSeedTrade records a synthetic trade (from a backtest) used only to warm
// the strategy-health gate. Seeded trades feed the health window but are excluded
// from account performance stats.
func (s *PaperStore) InsertSeedTrade(ctx context.Context, t PaperTrade) error {
	return s.insertTrade(ctx, t, true)
}

func (s *PaperStore) insertTrade(ctx context.Context, t PaperTrade, seeded bool) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO paper_trades (symbol, entry_date, exit_date, entry, exit, shares, sl, realized_r, pnl, outcome, seeded)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		t.Symbol, t.EntryDate, t.ExitDate, t.Entry, t.Exit, t.Shares, t.SL, t.RealizedR, t.PnL, t.Outcome, seeded)
	return err
}

// InsertShadowTrade records a closed shadow trade (gate-closed simulation). Like
// a seed trade it feeds the strategy-health window (RecentTradeR) but is excluded
// from account-performance stats — it just keeps the gate measuring while flat.
func (s *PaperStore) InsertShadowTrade(ctx context.Context, t PaperTrade) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO paper_trades (symbol, entry_date, exit_date, entry, exit, shares, sl, realized_r, pnl, outcome, shadow)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,TRUE)`,
		t.Symbol, t.EntryDate, t.ExitDate, t.Entry, t.Exit, t.Shares, t.SL, t.RealizedR, t.PnL, t.Outcome)
	return err
}

// ── Shadow positions / pending (no capital; mirror the real tables) ──────────

// ShadowPositions returns open shadow positions, ordered for determinism.
func (s *PaperStore) ShadowPositions(ctx context.Context) ([]PaperPosition, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, symbol, 0, entry, entry_date, sl, target, atr
		 FROM paper_shadow_positions ORDER BY entry_date, symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaperPosition
	for rows.Next() {
		var p PaperPosition
		if err := rows.Scan(&p.ID, &p.Symbol, &p.Shares, &p.Entry, &p.EntryDate, &p.SL, &p.Target, &p.ATR); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PaperStore) InsertShadowPosition(ctx context.Context, p PaperPosition) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO paper_shadow_positions (symbol, entry, entry_date, sl, target, atr)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (symbol) DO UPDATE SET
		   entry=EXCLUDED.entry, entry_date=EXCLUDED.entry_date,
		   sl=EXCLUDED.sl, target=EXCLUDED.target, atr=EXCLUDED.atr`,
		p.Symbol, p.Entry, p.EntryDate, p.SL, p.Target, p.ATR)
	return err
}

func (s *PaperStore) DeleteShadowPosition(ctx context.Context, symbol string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM paper_shadow_positions WHERE symbol = $1`, symbol)
	return err
}

func (s *PaperStore) ShadowPending(ctx context.Context) ([]PaperPending, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, symbol, signal_date, sl, target, atr FROM paper_shadow_pending ORDER BY symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaperPending
	for rows.Next() {
		var p PaperPending
		if err := rows.Scan(&p.ID, &p.Symbol, &p.SignalDate, &p.SL, &p.Target, &p.ATR); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PaperStore) InsertShadowPending(ctx context.Context, p PaperPending) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO paper_shadow_pending (symbol, signal_date, sl, target, atr) VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (symbol) DO UPDATE SET
		   signal_date=EXCLUDED.signal_date, sl=EXCLUDED.sl, target=EXCLUDED.target, atr=EXCLUDED.atr`,
		p.Symbol, p.SignalDate, p.SL, p.Target, p.ATR)
	return err
}

func (s *PaperStore) ClearShadowPending(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM paper_shadow_pending`)
	return err
}

// ClearSeedTrades removes only seeded trades (e.g. before re-seeding).
func (s *PaperStore) ClearSeedTrades(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM paper_trades WHERE seeded = TRUE`)
	return err
}

// RecentTradeR returns the realised R of the last n closed trades, oldest first,
// ready to seed/evaluate the strategy-health gate.
func (s *PaperStore) RecentTradeR(ctx context.Context, n int) ([]float64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT realized_r FROM (
		   SELECT realized_r, exit_date, id FROM paper_trades
		   ORDER BY exit_date DESC, id DESC LIMIT $1
		 ) t ORDER BY exit_date ASC, id ASC`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var r float64
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Reset clears all paper state (account, positions, pending, trades, shadow) so a
// fresh session can start. Irreversible.
func (s *PaperStore) Reset(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`TRUNCATE paper_account, paper_positions, paper_pending, paper_trades,
		          paper_shadow_positions, paper_shadow_pending`)
	return err
}

// TradeStats returns aggregate counts for reporting.
func (s *PaperStore) TradeStats(ctx context.Context) (total, wins, losses int, sumPnL float64, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE realized_r > 0),
		       COUNT(*) FILTER (WHERE realized_r < 0),
		       COALESCE(SUM(pnl), 0)
		FROM paper_trades WHERE seeded = FALSE AND shadow = FALSE`).Scan(&total, &wins, &losses, &sumPnL)
	return
}
