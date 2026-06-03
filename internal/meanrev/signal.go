// Package meanrev implements a mean-reversion scanner — the deliberate
// opposite of the momentum/trend strategies (swing, crossover) elsewhere in
// this project. Every strategy we have built buys strength; this one buys a
// short-term oversold dip *inside* a confirmed long-term up-trend, betting on
// a snap-back to the mean rather than a continuation of a move.
//
// Mean Reversion V1 (Connors RSI-2 style, adapted to our engine):
//
//	Long-term filter : Close > EMA200          (only buy dips in healthy names)
//	Oversold trigger : RSI(2) < MaxRSI         (a sharp short-term washout)
//	Target           : EMA(MeanPeriod)         (revert to the short-term mean)
//	Stop             : Close − StopATRMult×ATR (wide; the mean/time exit leads)
//
// ─── REJECTED EXPERIMENT — kept in-tree as a documented negative result ───
//
// V1 was the experiment, not the product, and it FAILED decisively. The idea
// was the "defensive compounder" leg of a regime-switcher: a mean-reversion
// mode that earns its keep in the weak 2024–2025 regime where momentum stalls.
// It does the exact opposite — it is *worst* in precisely those years
// (2024 −13%, 2025 −13%, 2026-YTD −20%; full 2022–26 −44% at −46% DD) versus
// the swing strategy's −5% / −5% / +0% / +30%. High win rate (58–74%) but
// profit factor < 1 every losing year: snap-back wins are tiny, ATR stops are
// wide, and the asymmetry sinks it. Robustness was exhausted — the health gate
// only shuts it off, and every parameter sweep (wider target, tighter stop,
// stricter RSI) is negative. Root cause: oversold Indian names in 2024–2025
// kept falling (downside trend-persistence), so dip-buying caught falling
// knives even above EMA200. See ANALYSIS.md §10. Do NOT re-enable expecting an
// edge; this is preserved only so the experiment is not blindly repeated. The
// Wilder RSI helper (internal/analysis) is the one reusable by-product.
package meanrev

// Signal is the output for one symbol that is oversold within an up-trend.
type Signal struct {
	Symbol string
	Price  float64 // latest close (signal-day close)

	// RSI is the short-period RSI value on the signal day (the oversold reading).
	RSI float64
	// EMA200 is the long-term trend reference on the signal day.
	EMA200 float64

	// Trade setup. Entry is taken at the next candle's open by the backtest
	// engine; Price here is the signal-day close used to derive levels.
	Entry  float64 // signal-day close (level reference)
	SL     float64 // Close − StopATRMult × ATR
	Target float64 // EMA(MeanPeriod) — the reversion-to-mean exit level
	ATR    float64

	Score   float64
	Reasons []string
}
