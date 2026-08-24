// Package earnings computes how a stock's price reacted around a quarterly
// results declaration: the calendar week before the report date through the
// calendar week after it, using whatever trading days fall in that span.
package earnings

import (
	"context"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/internal/store"
	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// ISTOffset converts a UTC daily-candle timestamp back to its true IST
// trading date. Kite's daily candles are midnight IST (e.g.
// "2026-07-17T00:00:00+0530"); parseKiteTime converts that to UTC, which
// lands on 18:30 the *previous* calendar day. Without this correction, every
// date comparison against an externally-sourced (true IST) date is off by
// one trading day.
const ISTOffset = 5*time.Hour + 30*time.Minute

// ISTDate returns the candle's true IST trading date, truncated to midnight
// UTC so it can be compared directly against externally-sourced dates.
func ISTDate(c models.Candle) time.Time {
	ist := c.Timestamp.Add(ISTOffset)
	return time.Date(ist.Year(), ist.Month(), ist.Day(), 0, 0, 0, 0, time.UTC)
}

// Reaction holds the computed price-reaction figures for one earnings event.
type Reaction struct {
	Day0Date                          time.Time
	PreWeekPct                        float64
	PostWeekPct                       float64
	TotalPct                          float64
	FirstClose, Day0Close, LastClose  float64
	OK                                bool
}

// Compute pulls candles in the calendar window (report date ± 7 calendar
// days) and computes the reaction. OK=false means there wasn't enough
// candle data to compute it (e.g. a future/undeclared event). The returned
// candles slice and day0 index are exposed so callers can also render a
// day-by-day breakdown without a second DB round-trip.
func Compute(ctx context.Context, cs *store.CandleStore, e store.EarningsEvent) (Reaction, []models.Candle, int) {
	windowFrom := e.ReportDate.AddDate(0, 0, -7)
	windowTo := e.ReportDate.AddDate(0, 0, 7)
	queryFrom := windowFrom.AddDate(0, 0, -5)
	queryTo := windowTo.AddDate(0, 0, 5)

	candles, err := cs.GetCandles(ctx, e.Symbol, store.CandleFilter{From: &queryFrom, To: &queryTo})
	if err != nil || len(candles) == 0 {
		return Reaction{}, nil, -1
	}

	day0 := -1
	for i, c := range candles {
		if !ISTDate(c).Before(e.ReportDate) {
			day0 = i
			break
		}
	}
	if day0 < 0 {
		return Reaction{}, candles, -1
	}
	day0Close := candles[day0].Close

	var firstInWindow, lastInWindow float64
	haveFirst := false
	for _, c := range candles {
		d := ISTDate(c)
		if d.Before(windowFrom) || d.After(windowTo) {
			continue
		}
		if !haveFirst {
			firstInWindow = c.Close
			haveFirst = true
		}
		lastInWindow = c.Close
	}
	if !haveFirst {
		return Reaction{}, candles, day0
	}

	return Reaction{
		Day0Date:    ISTDate(candles[day0]),
		PreWeekPct:  (day0Close/firstInWindow - 1) * 100,
		PostWeekPct: (lastInWindow/day0Close - 1) * 100,
		TotalPct:    (lastInWindow/firstInWindow - 1) * 100,
		FirstClose:  firstInWindow, Day0Close: day0Close, LastClose: lastInWindow,
		OK: true,
	}, candles, day0
}

// Window returns the calendar-day bounds (report date ± 7 days) used by
// Compute, so callers rendering a day-by-day table can filter the same
// candles slice consistently without recomputing the offsets themselves.
func Window(e store.EarningsEvent) (from, to time.Time) {
	return e.ReportDate.AddDate(0, 0, -7), e.ReportDate.AddDate(0, 0, 7)
}
