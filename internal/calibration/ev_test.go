package calibration

import (
	"math"
	"testing"
	"time"
)

func makeBetRecord(ourP, mktP, size float64, outcome *bool, signal string) BetRecord {
	b := boolPtr(true)
	_ = b // suppress unused warning; boolPtr defined in timing_test.go
	return BetRecord{
		Timestamp:      time.Now(),
		OurProbability: ourP,
		MarketPrice:    mktP,
		SizeUSDC:       size,
		Outcome:        outcome,
		Signal:         signal,
	}
}

func bp(v bool) *bool { return &v }

func TestRollingEV_Empty(t *testing.T) {
	res := RollingEV(nil, 0)
	if res.Count != 0 || res.ExpectedEV != 0 || res.RealizedPnL != 0 {
		t.Errorf("empty: got %+v", res)
	}
}

func TestRollingEV_AllWins(t *testing.T) {
	// ourP=0.7, mktP=0.5, size=10 → edge=0.2, EV=2.0, pnl=10*(1/0.5-1)=10
	records := []BetRecord{
		makeBetRecord(0.7, 0.5, 10, bp(true), "rain"),
		makeBetRecord(0.7, 0.5, 10, bp(true), "rain"),
	}
	res := RollingEV(records, 0)
	if res.Count != 2 {
		t.Errorf("count want 2 got %d", res.Count)
	}
	if math.Abs(res.ExpectedEV-4.0) > 0.001 {
		t.Errorf("expectedEV want 4.0 got %.4f", res.ExpectedEV)
	}
	if math.Abs(res.RealizedPnL-20.0) > 0.001 {
		t.Errorf("realizedPnL want 20.0 got %.4f", res.RealizedPnL)
	}
	if res.CaptureRatio < 1.0 {
		t.Errorf("captureRatio want ≥1 got %.4f", res.CaptureRatio)
	}
}

func TestRollingEV_AllLosses(t *testing.T) {
	// ourP=0.7, mktP=0.5, size=10 → EV=2.0, pnl=-10
	records := []BetRecord{
		makeBetRecord(0.7, 0.5, 10, bp(false), "heat"),
		makeBetRecord(0.7, 0.5, 10, bp(false), "heat"),
	}
	res := RollingEV(records, 0)
	if math.Abs(res.ExpectedEV-4.0) > 0.001 {
		t.Errorf("expectedEV want 4.0 got %.4f", res.ExpectedEV)
	}
	if math.Abs(res.RealizedPnL+20.0) > 0.001 {
		t.Errorf("realizedPnL want -20.0 got %.4f", res.RealizedPnL)
	}
	if res.CaptureRatio >= 0 {
		t.Errorf("captureRatio should be negative got %.4f", res.CaptureRatio)
	}
}

func TestRollingEV_Mixed(t *testing.T) {
	// 1 win + 1 loss: ourP=0.6, mktP=0.5, size=10
	// edge=0.1, EV_total=2.0; win pnl=10, loss pnl=-10 → net=0
	records := []BetRecord{
		makeBetRecord(0.6, 0.5, 10, bp(true), "rain"),
		makeBetRecord(0.6, 0.5, 10, bp(false), "rain"),
	}
	res := RollingEV(records, 0)
	if res.Count != 2 {
		t.Errorf("count want 2 got %d", res.Count)
	}
	if math.Abs(res.ExpectedEV-2.0) > 0.001 {
		t.Errorf("expectedEV want 2.0 got %.4f", res.ExpectedEV)
	}
	if math.Abs(res.RealizedPnL) > 0.001 {
		t.Errorf("realizedPnL want ≈0 got %.4f", res.RealizedPnL)
	}
}

func TestWeightedBrierScore_Empty(t *testing.T) {
	score, count, err := WeightedBrierScore(nil)
	if err != nil || count != 0 || score != 0 {
		t.Errorf("empty: got score=%.4f count=%d err=%v", score, count, err)
	}
}

func TestWeightedBrierScore_EqualSizes(t *testing.T) {
	// Equal sizes → weighted Brier should equal unweighted Brier
	records := []BetRecord{
		makeBetRecord(0.8, 0.5, 10, bp(true), "rain"),  // diff=0.2, brier=0.04
		makeBetRecord(0.3, 0.5, 10, bp(false), "rain"), // diff=0.3, brier=0.09
	}
	wScore, _, _ := WeightedBrierScore(records)
	score, _, _ := BrierScore(records)
	if math.Abs(wScore-score) > 0.001 {
		t.Errorf("equal sizes: weighted %.4f vs unweighted %.4f should match", wScore, score)
	}
}

func TestWeightedBrierScore_LargerBetsWeighMore(t *testing.T) {
	// Large accurate bet + small inaccurate bet → weighted Brier lower than unweighted
	accurate := makeBetRecord(0.9, 0.5, 100, bp(true), "heat") // brier=0.01, big bet
	inaccurate := makeBetRecord(0.1, 0.5, 1, bp(true), "heat") // brier=0.81, tiny bet

	records := []BetRecord{accurate, inaccurate}
	wScore, _, _ := WeightedBrierScore(records)
	score, _, _ := BrierScore(records)

	// Weighted should be lower (better) because the large accurate bet dominates.
	if wScore >= score {
		t.Errorf("large accurate bet should pull weighted Brier lower: weighted=%.4f unweighted=%.4f", wScore, score)
	}
}
