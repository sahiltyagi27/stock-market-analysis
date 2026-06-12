package paper

import (
	"context"
	"database/sql"
	"sort"
	"testing"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/internal/scanner"
	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

func analysisTradeSetup(sl, target, atr float64) analysis.TradeSetup {
	return analysis.TradeSetup{StopLoss: sl, Target: target, ATR: atr}
}

// fakeStore is an in-memory paper.Store for hermetic engine tests (no database).
type fakeStore struct {
	acct       *store.PaperAccount
	positions  map[string]store.PaperPosition
	pending    map[string]store.PaperPending
	trades     []store.PaperTrade // chronological insert order; shadow flagged separately
	tradeShdw  []bool
	shadowPos  map[string]store.PaperPosition
	shadowPend map[string]store.PaperPending
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		positions:  map[string]store.PaperPosition{},
		pending:    map[string]store.PaperPending{},
		shadowPos:  map[string]store.PaperPosition{},
		shadowPend: map[string]store.PaperPending{},
	}
}

func (f *fakeStore) Account(context.Context) (*store.PaperAccount, error) { return f.acct, nil }
func (f *fakeStore) InitAccount(_ context.Context, sc float64) error {
	f.acct = &store.PaperAccount{StartCapital: sc, Cash: sc}
	return nil
}
func (f *fakeStore) SetCash(_ context.Context, c float64) error {
	if f.acct != nil {
		f.acct.Cash = c
	}
	return nil
}
func (f *fakeStore) SetLastEOD(_ context.Context, d time.Time) error {
	if f.acct != nil {
		f.acct.LastEOD = sql.NullTime{Time: d, Valid: true}
	}
	return nil
}

func sortedVals[V any](m map[string]V) []V {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]V, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

func (f *fakeStore) Positions(context.Context) ([]store.PaperPosition, error) {
	return sortedVals(f.positions), nil
}
func (f *fakeStore) InsertPosition(_ context.Context, p store.PaperPosition) error {
	f.positions[p.Symbol] = p
	return nil
}
func (f *fakeStore) DeletePosition(_ context.Context, s string) error {
	delete(f.positions, s)
	return nil
}
func (f *fakeStore) Pending(context.Context) ([]store.PaperPending, error) {
	return sortedVals(f.pending), nil
}
func (f *fakeStore) InsertPending(_ context.Context, p store.PaperPending) error {
	f.pending[p.Symbol] = p
	return nil
}
func (f *fakeStore) ClearPending(context.Context) error {
	f.pending = map[string]store.PaperPending{}
	return nil
}
func (f *fakeStore) InsertTrade(_ context.Context, t store.PaperTrade) error {
	f.trades = append(f.trades, t)
	f.tradeShdw = append(f.tradeShdw, false)
	return nil
}
func (f *fakeStore) InsertShadowTrade(_ context.Context, t store.PaperTrade) error {
	f.trades = append(f.trades, t)
	f.tradeShdw = append(f.tradeShdw, true)
	return nil
}

// RecentTradeR mirrors the store: last n trades (real + seeded + shadow), oldest first.
func (f *fakeStore) RecentTradeR(_ context.Context, n int) ([]float64, error) {
	all := make([]float64, len(f.trades))
	for i, t := range f.trades {
		all[i] = t.RealizedR
	}
	if n < len(all) {
		all = all[len(all)-n:]
	}
	return all, nil
}

func (f *fakeStore) ShadowPositions(context.Context) ([]store.PaperPosition, error) {
	return sortedVals(f.shadowPos), nil
}
func (f *fakeStore) InsertShadowPosition(_ context.Context, p store.PaperPosition) error {
	f.shadowPos[p.Symbol] = p
	return nil
}
func (f *fakeStore) DeleteShadowPosition(_ context.Context, s string) error {
	delete(f.shadowPos, s)
	return nil
}
func (f *fakeStore) ShadowPending(context.Context) ([]store.PaperPending, error) {
	return sortedVals(f.shadowPend), nil
}
func (f *fakeStore) InsertShadowPending(_ context.Context, p store.PaperPending) error {
	f.shadowPend[p.Symbol] = p
	return nil
}
func (f *fakeStore) ClearShadowPending(context.Context) error {
	f.shadowPend = map[string]store.PaperPending{}
	return nil
}

// fakeCandles returns a fixed per-symbol series truncated at the To filter.
type fakeCandles struct{ series map[string][]models.Candle }

func (f *fakeCandles) GetCandles(_ context.Context, sym string, filt store.CandleFilter) ([]models.Candle, error) {
	cc := f.series[sym]
	if filt.To == nil {
		return cc, nil
	}
	var out []models.Candle
	for _, c := range cc {
		if !c.Timestamp.After(*filt.To) {
			out = append(out, c)
		}
	}
	return out, nil
}

// shadowTestSetup builds a fresh fake store (gate seeded CLOSED with 3 losers)
// and a candle source + config for the reopen scenario.
func shadowTestSetup(shadow bool) (*fakeStore, *fakeCandles, []string, Config, time.Time) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	syms := []string{"AAA", "BBB", "CCC"}
	series := map[string][]models.Candle{}
	for _, s := range syms {
		series[s] = winSeries(start, 60)
	}
	fs := newFakeStore()
	// Seed the gate CLOSED: three prior losing (real) trades.
	for i := 0; i < 3; i++ {
		fs.trades = append(fs.trades, store.PaperTrade{RealizedR: -1})
		fs.tradeShdw = append(fs.tradeShdw, false)
	}
	cfg := Config{
		StartCapital: 100000, MaxPositions: 5, RiskPct: 1, MaxWeightPct: 25,
		HealthWindow: 3, HealthMin: 0, HealthShadow: shadow, MinScore: 0,
		// Inject a signal for every symbol each day (dedup/slot logic handles the rest).
		signalFunc: func(history map[string][]models.Candle, _ scanner.Options) []scanner.StockSignal {
			var out []scanner.StockSignal
			for sym, cc := range history {
				if len(cc) == 0 {
					continue
				}
				price := cc[len(cc)-1].Close
				out = append(out, scanner.StockSignal{
					Symbol: sym, Price: price, Score: 90,
					Trade: analysisTradeSetup(price-30, price+50, 1),
				})
			}
			return out
		},
	}
	return fs, &fakeCandles{series: series}, syms, cfg, start
}

// runDays drives RunDayEnd over asOf = start+from .. start+to (inclusive),
// returning how many real pending entries were queued in total (a proxy for the
// gate having reopened) and the count of closed shadow trades.
func runDays(t *testing.T, fs *fakeStore, fc *fakeCandles, syms []string, cfg Config, start time.Time, from, to int) (realQueued, shadowTrades int) {
	t.Helper()
	for d := from; d <= to; d++ {
		asOf := start.AddDate(0, 0, d)
		rep, err := RunDayEnd(context.Background(), fs, fc, syms, asOf, cfg, false, false)
		if err != nil {
			t.Fatalf("day %d: RunDayEnd: %v", d, err)
		}
		realQueued += rep.PendingMade
	}
	for _, sh := range fs.tradeShdw {
		if sh {
			shadowTrades++
		}
	}
	return realQueued, shadowTrades
}

// TestHealthGate_PaperShadowReopens is the regression test for the paper-trade
// one-way-door fix. The gate is seeded CLOSED (three prior losers):
//   - without shadow, no real trade ever closes → RecentTradeR never refreshes →
//     the gate stays locked → zero real entries are ever queued;
//   - with shadow, the strategy keeps simulating trades while flat; once enough
//     winning shadow trades land, the gate reopens and real entries resume.
func TestHealthGate_PaperShadowReopens(t *testing.T) {
	// Without shadow: a gate seeded closed must stay locked forever.
	fs, fc, syms, cfg, start := shadowTestSetup(false)
	realQueued, shadowTrades := runDays(t, fs, fc, syms, cfg, start, 21, 55)
	if shadowTrades != 0 {
		t.Fatalf("without shadow there must be no shadow trades, got %d", shadowTrades)
	}
	if realQueued != 0 {
		t.Fatalf("without shadow a gate seeded closed must stay locked (0 real entries), got %d", realQueued)
	}

	// With shadow: the gate must reopen (real entries get queued again).
	fs, fc, syms, cfg, start = shadowTestSetup(true)
	realQueued, shadowTrades = runDays(t, fs, fc, syms, cfg, start, 21, 55)
	if shadowTrades == 0 {
		t.Fatal("with shadow the strategy must record shadow trades while the gate is closed")
	}
	if realQueued == 0 {
		t.Fatal("with shadow the gate must reopen once shadow trades turn healthy (real entries queued), but none were")
	}
}

// winSeries builds a series that rises, then declines — so a position entered
// near the start of the rise exits on the EMA7<EMA21 recross (triggered by the
// later decline) at a price well above entry: a winning, EMA-exited trade.
func winSeries(start time.Time, n int) []models.Candle {
	out := make([]models.Candle, n)
	for i := 0; i < n; i++ {
		var p float64
		if i <= 38 {
			p = 100 + float64(i)*3 // strong rise: 100 → 214
		} else {
			p = 214 - float64(i-38)*4 // decline from the peak (still well above early entries)
		}
		out[i] = models.Candle{
			Symbol: "X", Timestamp: start.AddDate(0, 0, i),
			Open: p, High: p + 1, Low: p - 1, Close: p, Volume: 100000,
		}
	}
	return out
}
