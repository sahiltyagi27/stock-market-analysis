package backtest

import (
	"math"
	"testing"
)

func TestCompute_Empty(t *testing.T) {
	s := Compute(nil)
	if s.Total != 0 || s.Wins != 0 || s.Losses != 0 {
		t.Fatalf("expected zero Summary for empty input, got %+v", s)
	}
}

func TestCompute_AllWins(t *testing.T) {
	results := []TradeResult{
		{Outcome: OutcomeWin, ActualRR: 3.0, HoldDays: 5},
		{Outcome: OutcomeWin, ActualRR: 2.0, HoldDays: 3},
		{Outcome: OutcomeWin, ActualRR: 4.0, HoldDays: 7},
	}
	s := Compute(results)

	if s.Total != 3 {
		t.Fatalf("expected Total=3, got %d", s.Total)
	}
	if s.Wins != 3 {
		t.Fatalf("expected Wins=3, got %d", s.Wins)
	}
	if s.Losses != 0 {
		t.Fatalf("expected Losses=0, got %d", s.Losses)
	}
	if s.WinRate != 100.0 {
		t.Fatalf("expected WinRate=100, got %.2f", s.WinRate)
	}
	if !math.IsInf(s.ProfitFactor, 1) {
		t.Fatalf("expected ProfitFactor=+Inf (no losses), got %.4f", s.ProfitFactor)
	}
	wantAvgWin := (3.0 + 2.0 + 4.0) / 3.0
	if math.Abs(s.AvgWinRR-wantAvgWin) > 1e-9 {
		t.Fatalf("expected AvgWinRR=%.4f, got %.4f", wantAvgWin, s.AvgWinRR)
	}
}

func TestCompute_AllLosses(t *testing.T) {
	results := []TradeResult{
		{Outcome: OutcomeLoss, ActualRR: -1.0, HoldDays: 2},
		{Outcome: OutcomeLoss, ActualRR: -1.0, HoldDays: 3},
	}
	s := Compute(results)

	if s.WinRate != 0 {
		t.Fatalf("expected WinRate=0, got %.2f", s.WinRate)
	}
	if s.Losses != 2 {
		t.Fatalf("expected Losses=2, got %d", s.Losses)
	}
	if s.MaxConsecLoss != 2 {
		t.Fatalf("expected MaxConsecLoss=2, got %d", s.MaxConsecLoss)
	}
	if s.ProfitFactor != 0 {
		t.Fatalf("expected ProfitFactor=0 (no winners), got %.4f", s.ProfitFactor)
	}
}

func TestCompute_Mixed(t *testing.T) {
	// 2 wins (+3R, +2R) and 1 loss (−1R).
	results := []TradeResult{
		{Outcome: OutcomeWin, ActualRR: 3.0, HoldDays: 5},
		{Outcome: OutcomeLoss, ActualRR: -1.0, HoldDays: 2},
		{Outcome: OutcomeWin, ActualRR: 2.0, HoldDays: 4},
	}
	s := Compute(results)

	if s.Wins != 2 || s.Losses != 1 {
		t.Fatalf("expected 2W/1L, got %dW/%dL", s.Wins, s.Losses)
	}
	wantWinRate := 2.0 / 3.0 * 100
	if math.Abs(s.WinRate-wantWinRate) > 1e-6 {
		t.Fatalf("expected WinRate=%.4f, got %.4f", wantWinRate, s.WinRate)
	}
	// ProfitFactor = sumWin / |sumLoss| = 5.0 / 1.0 = 5.0
	wantPF := 5.0
	if math.Abs(s.ProfitFactor-wantPF) > 1e-9 {
		t.Fatalf("expected ProfitFactor=%.2f, got %.4f", wantPF, s.ProfitFactor)
	}
	// Expectancy = (2/3)*2.5 + (1/3)*(-1.0) = 1.6667 - 0.3333 = 1.3333
	wantE := (2.0/3.0)*2.5 + (1.0/3.0)*(-1.0)
	if math.Abs(s.Expectancy-wantE) > 1e-6 {
		t.Fatalf("expected Expectancy=%.4f, got %.4f", wantE, s.Expectancy)
	}
}

func TestCompute_TimeoutsExcludedFromWinRate(t *testing.T) {
	results := []TradeResult{
		{Outcome: OutcomeWin, ActualRR: 2.0, HoldDays: 5},
		{Outcome: OutcomeTimeout, ActualRR: 0.3, HoldDays: 20},
		{Outcome: OutcomeTimeout, ActualRR: -0.2, HoldDays: 20},
	}
	s := Compute(results)

	// WinRate should be based on Wins/(Wins+Losses) = 1/(1+0) = 100%.
	if s.WinRate != 100.0 {
		t.Fatalf("expected WinRate=100 (timeouts excluded), got %.2f", s.WinRate)
	}
	if s.Timeouts != 2 {
		t.Fatalf("expected Timeouts=2, got %d", s.Timeouts)
	}
}

func TestCompute_MaxConsecLoss(t *testing.T) {
	results := []TradeResult{
		{Outcome: OutcomeLoss, ActualRR: -1},
		{Outcome: OutcomeLoss, ActualRR: -1},
		{Outcome: OutcomeLoss, ActualRR: -1},
		{Outcome: OutcomeWin, ActualRR: 3},
		{Outcome: OutcomeLoss, ActualRR: -1},
		{Outcome: OutcomeLoss, ActualRR: -1},
	}
	s := Compute(results)
	if s.MaxConsecLoss != 3 {
		t.Fatalf("expected MaxConsecLoss=3, got %d", s.MaxConsecLoss)
	}
}

func TestCompute_AvgHoldDays(t *testing.T) {
	results := []TradeResult{
		{Outcome: OutcomeWin, ActualRR: 2, HoldDays: 4},
		{Outcome: OutcomeLoss, ActualRR: -1, HoldDays: 2},
		{Outcome: OutcomeTimeout, ActualRR: 0, HoldDays: 20},
	}
	s := Compute(results)
	wantAvg := (4.0 + 2.0 + 20.0) / 3.0
	if math.Abs(s.AvgHoldDays-wantAvg) > 1e-9 {
		t.Fatalf("expected AvgHoldDays=%.4f, got %.4f", wantAvg, s.AvgHoldDays)
	}
}
