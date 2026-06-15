package main

import (
	"testing"
	"time"

	"github.com/sahiltyagi27/stock-market-analysis/pkg/models"
)

// candleOnISTDate builds a NIFTY-style daily candle stamped at IST midnight of
// the given IST date (Kite's convention: stored as the prior UTC day 18:30Z).
func candleOnISTDate(istDate string, close float64) models.Candle {
	d, _ := time.ParseInLocation("2006-01-02", istDate, ist)
	return models.Candle{Timestamp: d, Close: close}
}

func TestPrevSessionClose_SkipsTodaysPartialCandle(t *testing.T) {
	// During market hours an intraday kite-sync writes a partial candle for
	// today. prevSessionClose must skip it and return the prior session's close.
	now, _ := time.ParseInLocation("2006-01-02 15:04", "2026-06-15 09:59", ist) // Monday, mid-session
	candles := []models.Candle{
		candleOnISTDate("2026-06-11", 23161.60), // Thu
		candleOnISTDate("2026-06-12", 23622.90), // Fri  ← the real prev close
		candleOnISTDate("2026-06-15", 23972.45), // Mon (TODAY, partial) ← must be skipped
	}
	got, ok := prevSessionClose(candles, now)
	if !ok {
		t.Fatal("expected a prior-session close, got none")
	}
	if got != 23622.90 {
		t.Fatalf("prev close = %.2f, want 23622.90 (Friday) — today's partial candle must be skipped", got)
	}
}

func TestPrevSessionClose_NoTodayCandle(t *testing.T) {
	// Normal case (no partial today-candle): the last row is the prior close.
	now, _ := time.ParseInLocation("2006-01-02 15:04", "2026-06-15 09:59", ist)
	candles := []models.Candle{
		candleOnISTDate("2026-06-11", 23161.60),
		candleOnISTDate("2026-06-12", 23622.90),
	}
	got, ok := prevSessionClose(candles, now)
	if !ok || got != 23622.90 {
		t.Fatalf("prev close = %.2f ok=%v, want 23622.90", got, ok)
	}
}

func TestPrevSessionClose_OnlyTodayCandle(t *testing.T) {
	// If the only candle is today's partial one, there is no prior close.
	now, _ := time.ParseInLocation("2006-01-02 15:04", "2026-06-15 09:59", ist)
	candles := []models.Candle{candleOnISTDate("2026-06-15", 23972.45)}
	if _, ok := prevSessionClose(candles, now); ok {
		t.Fatal("expected no prior close when only today's candle exists")
	}
}

func TestPrevSessionClose_Empty(t *testing.T) {
	if _, ok := prevSessionClose(nil, time.Now()); ok {
		t.Fatal("expected no prior close for empty history")
	}
}
