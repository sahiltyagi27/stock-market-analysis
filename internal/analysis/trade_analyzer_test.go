package analysis_test

import (
	"errors"
	"math"
	"testing"

	"github.com/sahiltyagi27/stock-market-analysis/internal/analysis"
)

// Fixtures used across tests.
var (
	support    = analysis.Zone{Low: 95, High: 100, Mid: 97.5, Touches: 3}
	resistance = analysis.Zone{Low: 148, High: 152, Mid: 150, Touches: 2}
	midPrice   = 125.0 // comfortably between the two zones
	analyzerOpts = analysis.AnalyzerOptions{SLBufferPct: 0.005}
)

// ---------------------------------------------------------------------------
// Happy path
// ---------------------------------------------------------------------------

func TestAnalyze_ReturnsBothSetups(t *testing.T) {
	a, err := analysis.Analyze(midPrice, support, resistance, analyzerOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Long == nil {
		t.Error("expected Long setup, got nil")
	}
	if a.Short == nil {
		t.Error("expected Short setup, got nil")
	}
}

func TestAnalyze_LongDirection(t *testing.T) {
	a, _ := analysis.Analyze(midPrice, support, resistance, analyzerOpts)
	if a.Long.Direction != analysis.DirectionLong {
		t.Errorf("long direction = %q, want %q", a.Long.Direction, analysis.DirectionLong)
	}
}

func TestAnalyze_ShortDirection(t *testing.T) {
	a, _ := analysis.Analyze(midPrice, support, resistance, analyzerOpts)
	if a.Short.Direction != analysis.DirectionShort {
		t.Errorf("short direction = %q, want %q", a.Short.Direction, analysis.DirectionShort)
	}
}

// ---------------------------------------------------------------------------
// Long setup geometry
// ---------------------------------------------------------------------------

func TestAnalyze_Long_EntryIsCurrentPrice(t *testing.T) {
	a, _ := analysis.Analyze(midPrice, support, resistance, analyzerOpts)
	if !approxEqual(a.Long.Entry, midPrice) {
		t.Errorf("long entry = %.4f, want %.4f", a.Long.Entry, midPrice)
	}
}

func TestAnalyze_Long_SLBelowSupportLow(t *testing.T) {
	a, _ := analysis.Analyze(midPrice, support, resistance, analyzerOpts)
	if a.Long.StopLoss >= support.Low {
		t.Errorf("long SL %.4f must be below support.Low %.4f", a.Long.StopLoss, support.Low)
	}
}

func TestAnalyze_Long_TargetIsResistanceMid(t *testing.T) {
	a, _ := analysis.Analyze(midPrice, support, resistance, analyzerOpts)
	if !approxEqual(a.Long.Target, resistance.Mid) {
		t.Errorf("long target = %.4f, want resistance.Mid %.4f", a.Long.Target, resistance.Mid)
	}
}

func TestAnalyze_Long_RiskRewardConsistency(t *testing.T) {
	a, _ := analysis.Analyze(midPrice, support, resistance, analyzerOpts)
	l := a.Long
	// All fields are rounded; derive expectations from the already-rounded values
	// to avoid phantom mismatches caused by intermediate rounding.
	wantRisk := l.Entry - l.StopLoss
	wantReward := l.Target - l.Entry

	if !approxEqual(l.Risk, wantRisk) {
		t.Errorf("long Risk = %.4f, want %.4f", l.Risk, wantRisk)
	}
	if !approxEqual(l.Reward, wantReward) {
		t.Errorf("long Reward = %.4f, want %.4f", l.Reward, wantReward)
	}
	// RiskReward is rounded to 2dp, so allow up to half a cent of tolerance.
	if l.Risk > 0 && math.Abs(l.RiskReward-l.Reward/l.Risk) > 0.005 {
		t.Errorf("long RR = %.4f, want ≈%.4f", l.RiskReward, l.Reward/l.Risk)
	}
}

// ---------------------------------------------------------------------------
// Short setup geometry
// ---------------------------------------------------------------------------

func TestAnalyze_Short_EntryIsCurrentPrice(t *testing.T) {
	a, _ := analysis.Analyze(midPrice, support, resistance, analyzerOpts)
	if !approxEqual(a.Short.Entry, midPrice) {
		t.Errorf("short entry = %.4f, want %.4f", a.Short.Entry, midPrice)
	}
}

func TestAnalyze_Short_SLAboveResistanceHigh(t *testing.T) {
	a, _ := analysis.Analyze(midPrice, support, resistance, analyzerOpts)
	if a.Short.StopLoss <= resistance.High {
		t.Errorf("short SL %.4f must be above resistance.High %.4f", a.Short.StopLoss, resistance.High)
	}
}

func TestAnalyze_Short_TargetIsSupportMid(t *testing.T) {
	a, _ := analysis.Analyze(midPrice, support, resistance, analyzerOpts)
	if !approxEqual(a.Short.Target, support.Mid) {
		t.Errorf("short target = %.4f, want support.Mid %.4f", a.Short.Target, support.Mid)
	}
}

func TestAnalyze_Short_RiskRewardConsistency(t *testing.T) {
	a, _ := analysis.Analyze(midPrice, support, resistance, analyzerOpts)
	s := a.Short
	wantRisk := s.StopLoss - s.Entry
	wantReward := s.Entry - s.Target

	if !approxEqual(s.Risk, wantRisk) {
		t.Errorf("short Risk = %.4f, want %.4f", s.Risk, wantRisk)
	}
	if !approxEqual(s.Reward, wantReward) {
		t.Errorf("short Reward = %.4f, want %.4f", s.Reward, wantReward)
	}
	if s.Risk > 0 && math.Abs(s.RiskReward-s.Reward/s.Risk) > 0.005 {
		t.Errorf("short RR = %.4f, want ≈%.4f", s.RiskReward, s.Reward/s.Risk)
	}
}

// ---------------------------------------------------------------------------
// Quality grading
// ---------------------------------------------------------------------------

func TestAnalyze_Quality_Excellent(t *testing.T) {
	// Wide range → high R/R for long.
	sup := analysis.Zone{Low: 99, High: 100, Mid: 99.5, Touches: 3}
	res := analysis.Zone{Low: 499, High: 501, Mid: 500, Touches: 2}
	a, err := analysis.Analyze(105, sup, res, analyzerOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Long.Quality != analysis.QualityExcellent {
		t.Errorf("expected Excellent, got %q (RR=%.2f)", a.Long.Quality, a.Long.RiskReward)
	}
}

func TestAnalyze_Quality_Poor(t *testing.T) {
	// Narrow range → low R/R for long: entry just below resistance.
	sup := analysis.Zone{Low: 95, High: 100, Mid: 97.5, Touches: 3}
	res := analysis.Zone{Low: 108, High: 110, Mid: 109, Touches: 2}
	a, err := analysis.Analyze(107, sup, res, analyzerOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Long.Quality != analysis.QualityPoor {
		t.Errorf("expected Poor, got %q (RR=%.2f)", a.Long.Quality, a.Long.RiskReward)
	}
}

func TestAnalyze_Quality_Boundaries(t *testing.T) {
	// GradeRR is tested directly to decouple quality thresholds from the
	// complexity of reverse-engineering zone fixtures for exact R/R values.
	cases := []struct {
		rr    float64
		wantQ analysis.Quality
	}{
		{0.5, analysis.QualityPoor},
		{1.0, analysis.QualityPoor},
		{1.49, analysis.QualityPoor},
		{1.5, analysis.QualityFair},
		{1.99, analysis.QualityFair},
		{2.0, analysis.QualityGood},
		{2.99, analysis.QualityGood},
		{3.0, analysis.QualityExcellent},
		{5.0, analysis.QualityExcellent},
	}
	for _, tc := range cases {
		got := analysis.GradeRR(tc.rr)
		if got != tc.wantQ {
			t.Errorf("GradeRR(%.2f) = %q, want %q", tc.rr, got, tc.wantQ)
		}
	}
}

// ---------------------------------------------------------------------------
// Input validation
// ---------------------------------------------------------------------------

func TestAnalyze_Error_ZeroPriceReturned(t *testing.T) {
	_, err := analysis.Analyze(0, support, resistance, analyzerOpts)
	if !errors.Is(err, analysis.ErrInvalidPrice) {
		t.Errorf("expected ErrInvalidPrice, got %v", err)
	}
}

func TestAnalyze_Error_NegativePrice(t *testing.T) {
	_, err := analysis.Analyze(-10, support, resistance, analyzerOpts)
	if !errors.Is(err, analysis.ErrInvalidPrice) {
		t.Errorf("expected ErrInvalidPrice, got %v", err)
	}
}

func TestAnalyze_Error_ZonesOverlap(t *testing.T) {
	sup := analysis.Zone{Low: 100, High: 120, Mid: 110, Touches: 2}
	res := analysis.Zone{Low: 90, High: 110, Mid: 100, Touches: 2} // mid ≤ support.mid
	_, err := analysis.Analyze(105, sup, res, analyzerOpts)
	if !errors.Is(err, analysis.ErrZonesOverlap) {
		t.Errorf("expected ErrZonesOverlap, got %v", err)
	}
}

func TestAnalyze_Error_PriceBelowSupport(t *testing.T) {
	_, err := analysis.Analyze(90, support, resistance, analyzerOpts)
	if !errors.Is(err, analysis.ErrPriceOutOfRange) {
		t.Errorf("expected ErrPriceOutOfRange, got %v", err)
	}
}

func TestAnalyze_Error_PriceAboveResistance(t *testing.T) {
	_, err := analysis.Analyze(200, support, resistance, analyzerOpts)
	if !errors.Is(err, analysis.ErrPriceOutOfRange) {
		t.Errorf("expected ErrPriceOutOfRange, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Default options
// ---------------------------------------------------------------------------

func TestAnalyze_ZeroOptsUsesDefaults(t *testing.T) {
	_, err := analysis.Analyze(midPrice, support, resistance, analysis.AnalyzerOptions{})
	if err != nil {
		t.Errorf("zero opts should not error, got %v", err)
	}
}
