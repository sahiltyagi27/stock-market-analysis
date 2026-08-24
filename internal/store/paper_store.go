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
//	paper_account   — one row per strategy: starting capital and current cash
//	paper_positions — currently open paper positions
//	paper_pending   — entries waiting to fill at the next session's open
//	paper_trades    — closed trades (also the strategy-health history source)
//
// Every table carries a strategy column so independent strategies (e.g.
// "swing" and "longhold") can each run their own paper account concurrently
// without seeing or mutating each other's cash, positions, or trade history.
// A PaperStore instance is bound to one strategy at construction.
type PaperStore struct {
	db       *sql.DB
	strategy string
}

// strategyAccountID maps a strategy name to a fixed paper_account.id. id is a
// legacy identity column (predates multi-strategy support); strategy is the
// real key now (see the unique index in Migrate). Kept as a small static map
// rather than a sequence so adding a strategy is a one-line, reviewable change
// — this repo's strategy set only grows when new adapter code is written
// anyway (see cmd/paper-trade's --strategy flag).
var strategyAccountID = map[string]int{
	"swing":    1,
	"longhold": 2,
}

func accountID(strategy string) int {
	if id, ok := strategyAccountID[strategy]; ok {
		return id
	}
	return 1
}

func NewPaperStore(db *sql.DB, strategy string) *PaperStore {
	return &PaperStore{db: db, strategy: strategy}
}

// PaperAccount is the per-strategy cash account.
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
	Symbol    string
	EntryDate time.Time
	ExitDate  time.Time
	Entry     float64
	Exit      float64
	Shares    int64
	SL        float64
	RealizedR float64
	PnL       float64
	Outcome   string
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

		-- Multi-strategy support: every paper_* table gets a strategy column so
		-- independent strategies (swing, longhold, ...) each get their own
		-- account, positions, pending, and trade history. Existing rows predate
		-- this and are backfilled as "swing" (the only strategy that ran before
		-- --strategy existed). The old singleton/per-symbol-global uniqueness
		-- constraints are replaced with strategy-scoped ones.
		ALTER TABLE paper_account ADD COLUMN IF NOT EXISTS strategy TEXT NOT NULL DEFAULT 'swing';
		ALTER TABLE paper_account DROP CONSTRAINT IF EXISTS paper_account_singleton;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_paper_account_strategy ON paper_account (strategy);

		ALTER TABLE paper_positions ADD COLUMN IF NOT EXISTS strategy TEXT NOT NULL DEFAULT 'swing';
		ALTER TABLE paper_positions DROP CONSTRAINT IF EXISTS paper_positions_symbol_key;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_paper_positions_strategy_symbol ON paper_positions (strategy, symbol);

		ALTER TABLE paper_pending ADD COLUMN IF NOT EXISTS strategy TEXT NOT NULL DEFAULT 'swing';
		ALTER TABLE paper_pending DROP CONSTRAINT IF EXISTS paper_pending_symbol_key;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_paper_pending_strategy_symbol ON paper_pending (strategy, symbol);

		ALTER TABLE paper_trades ADD COLUMN IF NOT EXISTS strategy TEXT NOT NULL DEFAULT 'swing';
		CREATE INDEX IF NOT EXISTS idx_paper_trades_strategy_exit ON paper_trades (strategy, exit_date);

		ALTER TABLE paper_shadow_positions ADD COLUMN IF NOT EXISTS strategy TEXT NOT NULL DEFAULT 'swing';
		ALTER TABLE paper_shadow_positions DROP CONSTRAINT IF EXISTS paper_shadow_positions_symbol_key;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_paper_shadow_positions_strategy_symbol ON paper_shadow_positions (strategy, symbol);

		ALTER TABLE paper_shadow_pending ADD COLUMN IF NOT EXISTS strategy TEXT NOT NULL DEFAULT 'swing';
		ALTER TABLE paper_shadow_pending DROP CONSTRAINT IF EXISTS paper_shadow_pending_symbol_key;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_paper_shadow_pending_strategy_symbol ON paper_shadow_pending (strategy, symbol);
	`)
	if err != nil {
		return fmt.Errorf("paper migrate: %w", err)
	}
	return nil
}

// Account returns the singleton account for this store's strategy, or (nil, nil)
// if not yet initialised.
func (s *PaperStore) Account(ctx context.Context) (*PaperAccount, error) {
	var a PaperAccount
	err := s.db.QueryRowContext(ctx,
		`SELECT start_capital, cash, updated_at, last_eod_date FROM paper_account WHERE strategy = $1`,
		s.strategy).
		Scan(&a.StartCapital, &a.Cash, &a.UpdatedAt, &a.LastEOD)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// InitAccount creates this strategy's account with the given starting capital.
func (s *PaperStore) InitAccount(ctx context.Context, startCapital float64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO paper_account (id, strategy, start_capital, cash) VALUES ($1, $2, $3, $3)
		 ON CONFLICT (strategy) DO NOTHING`, accountID(s.strategy), s.strategy, startCapital)
	return err
}

// SetCash updates this strategy's account cash balance.
func (s *PaperStore) SetCash(ctx context.Context, cash float64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE paper_account SET cash = $1, updated_at = now() WHERE strategy = $2`, cash, s.strategy)
	return err
}

// SetLastEOD records the calendar date (YYYY-MM-DD) of the most recent processed
// day-end cycle. Pass the date already normalised to the session timezone.
func (s *PaperStore) SetLastEOD(ctx context.Context, d time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE paper_account SET last_eod_date = $1 WHERE strategy = $2`, d.Format("2006-01-02"), s.strategy)
	return err
}

func (s *PaperStore) Positions(ctx context.Context) ([]PaperPosition, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, symbol, shares, entry, entry_date, sl, target, atr
		 FROM paper_positions WHERE strategy = $1 ORDER BY entry_date, symbol`, s.strategy)
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
		`INSERT INTO paper_positions (strategy, symbol, shares, entry, entry_date, sl, target, atr)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (strategy, symbol) DO UPDATE SET
		   shares=EXCLUDED.shares, entry=EXCLUDED.entry, entry_date=EXCLUDED.entry_date,
		   sl=EXCLUDED.sl, target=EXCLUDED.target, atr=EXCLUDED.atr`,
		s.strategy, p.Symbol, p.Shares, p.Entry, p.EntryDate, p.SL, p.Target, p.ATR)
	return err
}

func (s *PaperStore) DeletePosition(ctx context.Context, symbol string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM paper_positions WHERE strategy = $1 AND symbol = $2`, s.strategy, symbol)
	return err
}

func (s *PaperStore) Pending(ctx context.Context) ([]PaperPending, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, symbol, signal_date, sl, target, atr FROM paper_pending WHERE strategy = $1 ORDER BY symbol`,
		s.strategy)
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
		`INSERT INTO paper_pending (strategy, symbol, signal_date, sl, target, atr) VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (strategy, symbol) DO UPDATE SET
		   signal_date=EXCLUDED.signal_date, sl=EXCLUDED.sl, target=EXCLUDED.target, atr=EXCLUDED.atr`,
		s.strategy, p.Symbol, p.SignalDate, p.SL, p.Target, p.ATR)
	return err
}

func (s *PaperStore) ClearPending(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM paper_pending WHERE strategy = $1`, s.strategy)
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
		`INSERT INTO paper_trades (strategy, symbol, entry_date, exit_date, entry, exit, shares, sl, realized_r, pnl, outcome, seeded)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		s.strategy, t.Symbol, t.EntryDate, t.ExitDate, t.Entry, t.Exit, t.Shares, t.SL, t.RealizedR, t.PnL, t.Outcome, seeded)
	return err
}

// InsertShadowTrade records a closed shadow trade (gate-closed simulation). Like
// a seed trade it feeds the strategy-health window (RecentTradeR) but is excluded
// from account-performance stats — it just keeps the gate measuring while flat.
func (s *PaperStore) InsertShadowTrade(ctx context.Context, t PaperTrade) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO paper_trades (strategy, symbol, entry_date, exit_date, entry, exit, shares, sl, realized_r, pnl, outcome, shadow)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,TRUE)`,
		s.strategy, t.Symbol, t.EntryDate, t.ExitDate, t.Entry, t.Exit, t.Shares, t.SL, t.RealizedR, t.PnL, t.Outcome)
	return err
}

// ── Shadow positions / pending (no capital; mirror the real tables) ──────────

// ShadowPositions returns open shadow positions, ordered for determinism.
func (s *PaperStore) ShadowPositions(ctx context.Context) ([]PaperPosition, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, symbol, 0, entry, entry_date, sl, target, atr
		 FROM paper_shadow_positions WHERE strategy = $1 ORDER BY entry_date, symbol`, s.strategy)
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
		`INSERT INTO paper_shadow_positions (strategy, symbol, entry, entry_date, sl, target, atr)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (strategy, symbol) DO UPDATE SET
		   entry=EXCLUDED.entry, entry_date=EXCLUDED.entry_date,
		   sl=EXCLUDED.sl, target=EXCLUDED.target, atr=EXCLUDED.atr`,
		s.strategy, p.Symbol, p.Entry, p.EntryDate, p.SL, p.Target, p.ATR)
	return err
}

func (s *PaperStore) DeleteShadowPosition(ctx context.Context, symbol string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM paper_shadow_positions WHERE strategy = $1 AND symbol = $2`, s.strategy, symbol)
	return err
}

func (s *PaperStore) ShadowPending(ctx context.Context) ([]PaperPending, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, symbol, signal_date, sl, target, atr FROM paper_shadow_pending WHERE strategy = $1 ORDER BY symbol`,
		s.strategy)
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
		`INSERT INTO paper_shadow_pending (strategy, symbol, signal_date, sl, target, atr) VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (strategy, symbol) DO UPDATE SET
		   signal_date=EXCLUDED.signal_date, sl=EXCLUDED.sl, target=EXCLUDED.target, atr=EXCLUDED.atr`,
		s.strategy, p.Symbol, p.SignalDate, p.SL, p.Target, p.ATR)
	return err
}

func (s *PaperStore) ClearShadowPending(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM paper_shadow_pending WHERE strategy = $1`, s.strategy)
	return err
}

// ClearSeedTrades removes only this strategy's seeded trades (e.g. before re-seeding).
func (s *PaperStore) ClearSeedTrades(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM paper_trades WHERE seeded = TRUE AND strategy = $1`, s.strategy)
	return err
}

// RecentTradeR returns the realised R of this strategy's last n closed trades,
// oldest first, ready to seed/evaluate the strategy-health gate.
func (s *PaperStore) RecentTradeR(ctx context.Context, n int) ([]float64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT realized_r FROM (
		   SELECT realized_r, exit_date, id FROM paper_trades
		   WHERE strategy = $1
		   ORDER BY exit_date DESC, id DESC LIMIT $2
		 ) t ORDER BY exit_date ASC, id ASC`, s.strategy, n)
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

// Reset clears this strategy's paper state (account, positions, pending, trades,
// shadow) so a fresh session can start. Other strategies' state is untouched.
// Irreversible.
func (s *PaperStore) Reset(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{
		"paper_account", "paper_positions", "paper_pending", "paper_trades",
		"paper_shadow_positions", "paper_shadow_pending",
	} {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE strategy = $1`, table), s.strategy); err != nil {
			return fmt.Errorf("reset %s: %w", table, err)
		}
	}
	return tx.Commit()
}

// TradeStats returns aggregate counts for this strategy, for reporting.
func (s *PaperStore) TradeStats(ctx context.Context) (total, wins, losses int, sumPnL float64, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE realized_r > 0),
		       COUNT(*) FILTER (WHERE realized_r < 0),
		       COALESCE(SUM(pnl), 0)
		FROM paper_trades WHERE seeded = FALSE AND shadow = FALSE AND strategy = $1`, s.strategy).
		Scan(&total, &wins, &losses, &sumPnL)
	return
}
