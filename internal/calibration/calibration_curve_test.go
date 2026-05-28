package calibration

import (
	"math"
	"testing"
	"time"
)

// Test 1: empty records → all buckets have Count=0, ECE=0.
func TestBuildCalibrationCurve_Empty(t *testing.T) {
	curve := BuildCalibrationCurve(nil, "")
	if len(curve) != 5 {
		t.Fatalf("expected 5 buckets, got %d", len(curve))
	}
	for i, b := range curve {
		if b.Count != 0 {
			t.Errorf("bucket %d: want Count=0, got %d", i, b.Count)
		}
	}
	ece := CalibrationError(curve)
	if ece != 0 {
		t.Errorf("ECE for empty curve should be 0, got %f", ece)
	}
}

// Test 2: single signal with a few bets, cross-signal filter works.
func TestBuildCalibrationCurve_OneSignal(t *testing.T) {
	ts := time.Now()
	records := []BetRecord{
		{Signal: "rain", OurProbability: 0.75, Outcome: boolPtr(true), Timestamp: ts},
		{Signal: "rain", OurProbability: 0.80, Outcome: boolPtr(false), Timestamp: ts},
		{Signal: "heat", OurProbability: 0.25, Outcome: boolPtr(true), Timestamp: ts},
	}

	rainCurve := BuildCalibrationCurve(records, "rain")
	heatCurve := BuildCalibrationCurve(records, "heat")
	allCurve := BuildCalibrationCurve(records, "")

	// 0.75 → bucket 3 ([0.6,0.8)), 0.80 → bucket 4 ([0.8,1.0]).
	if rainCurve[3].Count != 1 {
		t.Errorf("rain bucket 3: want 1, got %d", rainCurve[3].Count)
	}
	if rainCurve[4].Count != 1 {
		t.Errorf("rain bucket 4: want 1, got %d", rainCurve[4].Count)
	}
	// Heat bet sits in bucket 1 ([0.2,0.4)).
	if heatCurve[1].Count != 1 {
		t.Errorf("heat bucket 1: want 1, got %d", heatCurve[1].Count)
	}
	// All-signals curve includes all 3 bets.
	total := 0
	for _, b := range allCurve {
		total += b.Count
	}
	if total != 3 {
		t.Errorf("all-curve total: want 3, got %d", total)
	}
}

// Test 3: perfect calibration → ECE ≈ 0.
// Place bets so that within each bucket the win rate matches the predicted probability.
func TestCalibrationError_PerfectCalibration(t *testing.T) {
	ts := time.Now()
	// Bucket 0 ([0,0.2)): predict 0.1 → want ~10% wins. 1 win out of 10 bets.
	records := make([]BetRecord, 0, 20)
	for i := 0; i < 10; i++ {
		outcome := i == 0 // 1 win
		records = append(records, BetRecord{
			Signal: "test", OurProbability: 0.10, Outcome: boolPtr(outcome), Timestamp: ts,
		})
	}
	// Bucket 4 ([0.8,1.0]): predict 0.9 → want ~90% wins. 9 wins out of 10 bets.
	for i := 0; i < 10; i++ {
		outcome := i < 9 // 9 wins
		records = append(records, BetRecord{
			Signal: "test", OurProbability: 0.90, Outcome: boolPtr(outcome), Timestamp: ts,
		})
	}

	curve := BuildCalibrationCurve(records, "test")
	ece := CalibrationError(curve)
	if ece > 0.05 {
		t.Errorf("expected near-zero ECE for perfect calibration, got %.4f", ece)
	}
}

// Test 4: systematic overestimation → ECE > 0, overconfident diagnosis.
// All bets predict 0.9 but only 50% win → ECE ≈ 0.4.
func TestCalibrationError_SystematicOverestimation(t *testing.T) {
	ts := time.Now()
	records := make([]BetRecord, 10)
	for i := range records {
		outcome := i%2 == 0 // 50% wins
		records[i] = BetRecord{
			Signal: "hot", OurProbability: 0.90, Outcome: boolPtr(outcome), Timestamp: ts,
		}
	}

	curve := BuildCalibrationCurve(records, "hot")
	ece := CalibrationError(curve)
	// Predicted ≈ 0.9, actual ≈ 0.5, so ECE ≈ 0.4.
	if math.Abs(ece-0.40) > 0.05 {
		t.Errorf("expected ECE ≈ 0.40 for systematic overestimation, got %.4f", ece)
	}

	diag := CalibrationDiagnosis(curve, ece)
	if diag != "severely overconfident" {
		t.Errorf("expected 'severely overconfident', got %q", diag)
	}
}
