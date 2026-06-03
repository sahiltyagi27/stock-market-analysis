package meanrev

import (
	"errors"
	"fmt"
	"sort"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// Input is one stock's candle data fed into the scanner.
type Input struct {
	Symbol  string
	Candles []models.Candle
}

// Options controls mean-reversion scanner behaviour. The zero value is filled
// with sensible defaults by withDefaults().
type Options struct {
	// RSIPeriod is the lookback for the oversold RSI trigger. Default: 2
	// (Connors' RSI-2 — a fast, well-documented mean-reversion oscillator).
	RSIPeriod int

	// MaxRSI is the upper bound on the signal-day RSI: a signal fires only when
	// RSI(RSIPeriod) < MaxRSI (deeply oversold). Default: 10.
	MaxRSI float64

	// TrendPeriod is the long-term trend EMA. A signal requires Close above it
	// so we only ever buy dips in structurally healthy names (the "quality"
	// proxy, using price alone since we have no fundamentals). Default: 200.
	TrendPeriod int

	// MeanPeriod is the EMA used as the reversion target — the short-term mean
	// the price is expected to snap back to. Default: 10.
	MeanPeriod int

	// StopATRMult sets the stop distance below the signal close:
	// SL = Close − StopATRMult × ATR(ATRPeriod). It is deliberately wide so the
	// mean-revert target or the time stop (MaxHoldDays, enforced by the engine)
	// does the work — a tight stop would shake the trade out of the very
	// washout it is trying to buy. Default: 2.5.
	StopATRMult float64

	// ATRPeriod is the ATR lookback for the stop. Default: 14.
	ATRPeriod int

	// MinCandles is the minimum candle count required before any analysis.
	// EMA200 needs 200 candles to seed; 210 gives a small margin. Default: 210.
	MinCandles int
}

func (o Options) withDefaults() Options {
	out := o
	if out.RSIPeriod <= 0 {
		out.RSIPeriod = 2
	}
	if out.MaxRSI <= 0 {
		out.MaxRSI = 10
	}
	if out.TrendPeriod <= 0 {
		out.TrendPeriod = 200
	}
	if out.MeanPeriod <= 0 {
		out.MeanPeriod = 10
	}
	if out.StopATRMult <= 0 {
		out.StopATRMult = 2.5
	}
	if out.ATRPeriod <= 0 {
		out.ATRPeriod = 14
	}
	if out.MinCandles <= 0 {
		out.MinCandles = out.TrendPeriod + 10
	}
	return out
}

// Scan runs the mean-reversion pipeline for every input and returns signals
// sorted by Score descending (deeper oversold first), with Symbol as a
// deterministic tiebreak. Filtered inputs are collected in the returned map.
func Scan(inputs []Input, opts Options) ([]Signal, map[string]error) {
	o := opts.withDefaults()
	errs := make(map[string]error)
	var signals []Signal

	for _, in := range inputs {
		sig, err := analyzeOne(in, o)
		if err != nil {
			errs[in.Symbol] = err
			continue
		}
		signals = append(signals, *sig)
	}

	sort.SliceStable(signals, func(i, j int) bool {
		if signals[i].Score != signals[j].Score {
			return signals[i].Score > signals[j].Score
		}
		return signals[i].Symbol < signals[j].Symbol
	})

	return signals, errs
}

// analyzeOne runs the full mean-reversion pipeline for a single symbol.
func analyzeOne(in Input, opts Options) (*Signal, error) {
	if len(in.Candles) == 0 {
		return nil, errors.New("no candles")
	}
	if len(in.Candles) < opts.MinCandles {
		return nil, fmt.Errorf("only %d candles (need %d)", len(in.Candles), opts.MinCandles)
	}

	closes := make([]float64, len(in.Candles))
	for i, c := range in.Candles {
		closes[i] = c.Close
	}
	n := len(closes)
	price := closes[n-1]

	// Long-term trend filter: only buy dips in names trading above their
	// long-term mean (structurally healthy — not falling knives).
	trend, err := analysis.EMA(closes, opts.TrendPeriod)
	if err != nil {
		return nil, fmt.Errorf("EMA%d: %w", opts.TrendPeriod, err)
	}
	if trend[n-1] <= 0 {
		return nil, fmt.Errorf("EMA%d not seeded", opts.TrendPeriod)
	}
	if price <= trend[n-1] {
		return nil, fmt.Errorf(
			"price %.2f not above EMA%d %.2f — no long-term up-trend",
			price, opts.TrendPeriod, trend[n-1],
		)
	}

	// Oversold trigger: a sharp short-term washout.
	rsi, err := analysis.RSI(closes, opts.RSIPeriod)
	if err != nil {
		return nil, fmt.Errorf("RSI%d: %w", opts.RSIPeriod, err)
	}
	rsiVal := rsi[n-1]
	if rsiVal >= opts.MaxRSI {
		return nil, fmt.Errorf(
			"RSI%d %.2f not below %.2f — not oversold enough",
			opts.RSIPeriod, rsiVal, opts.MaxRSI,
		)
	}

	// Reversion target: the short-term mean. Must sit above the current price
	// (we are below the mean after a washout); otherwise there is no edge.
	mean, err := analysis.EMA(closes, opts.MeanPeriod)
	if err != nil {
		return nil, fmt.Errorf("EMA%d: %w", opts.MeanPeriod, err)
	}
	target := mean[n-1]
	if target <= price {
		return nil, fmt.Errorf(
			"reversion target EMA%d %.2f not above price %.2f",
			opts.MeanPeriod, target, price,
		)
	}

	// Stop: a wide ATR-based floor. The mean/time exit is meant to lead.
	atr := analysis.ATR(in.Candles, opts.ATRPeriod)
	if atr <= 0 {
		return nil, errors.New("ATR unavailable")
	}
	sl := price - opts.StopATRMult*atr
	if sl <= 0 || sl >= price {
		return nil, fmt.Errorf("invalid SL %.2f for price %.2f", sl, price)
	}

	sig := &Signal{
		Symbol: in.Symbol,
		Price:  price,
		RSI:    rsiVal,
		EMA200: trend[n-1],
		Entry:  price,
		SL:     sl,
		Target: target,
		ATR:    atr,
	}
	sig.Score = score(sig, opts)
	sig.Reasons = buildReasons(sig, opts)
	return sig, nil
}

// score maps a signal to 0–100. Deeper oversold readings score higher: a stock
// at RSI 2 is a more extreme washout than one at RSI 9, so it gets first claim
// on scarce portfolio slots. Linear in (MaxRSI − RSI), centred at 50–100.
func score(sig *Signal, opts Options) float64 {
	depth := (opts.MaxRSI - sig.RSI) / opts.MaxRSI // 0 (at threshold) … 1 (RSI 0)
	if depth < 0 {
		depth = 0
	}
	if depth > 1 {
		depth = 1
	}
	return 50 + depth*50
}

func buildReasons(sig *Signal, opts Options) []string {
	return []string{
		fmt.Sprintf("RSI%d oversold at %.1f (< %.0f) within EMA%d up-trend",
			opts.RSIPeriod, sig.RSI, opts.MaxRSI, opts.TrendPeriod),
		fmt.Sprintf("Reversion target EMA%d %.2f (%.1f%% above entry)",
			opts.MeanPeriod, sig.Target, (sig.Target-sig.Entry)/sig.Entry*100),
		fmt.Sprintf("Stop %.2f = entry − %.1f×ATR(%.2f), %.1f%% risk",
			sig.SL, opts.StopATRMult, sig.ATR, (sig.Entry-sig.SL)/sig.Entry*100),
	}
}
