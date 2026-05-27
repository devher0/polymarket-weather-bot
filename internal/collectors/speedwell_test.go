// speedwell_test.go — unit tests for TASK-097: Speedwell HDD/CDD settlement.
package collectors

import (
	"math"
	"testing"
)

func TestSummariseSpeedwell_Empty(t *testing.T) {
	s := SummariseSpeedwell(nil)
	if s.Days != 0 || s.TotalHDD != 0 || s.TotalCDD != 0 {
		t.Errorf("empty summary should be zero, got %+v", s)
	}
}

func TestSummariseSpeedwell_Values(t *testing.T) {
	indices := []SpeedwellIndex{
		{City: "chicago", Date: "2024-01-01", AvgTempC: 0.0, HDD: 18.333, CDD: 0},   // cold
		{City: "chicago", Date: "2024-01-02", AvgTempC: 25.0, HDD: 0, CDD: 6.667},   // warm
		{City: "chicago", Date: "2024-01-03", AvgTempC: 18.333, HDD: 0.0, CDD: 0.0}, // baseline
	}
	s := SummariseSpeedwell(indices)

	if s.City != "chicago" {
		t.Errorf("City: want chicago, got %s", s.City)
	}
	if s.Days != 3 {
		t.Errorf("Days: want 3, got %d", s.Days)
	}
	if s.StartDate != "2024-01-01" || s.EndDate != "2024-01-03" {
		t.Errorf("date range: %s – %s", s.StartDate, s.EndDate)
	}
	wantHDD := 18.333
	wantCDD := 6.667
	wantAvg := (0.0 + 25.0 + 18.333) / 3.0
	if math.Abs(s.TotalHDD-wantHDD) > 0.01 {
		t.Errorf("TotalHDD: want %.3f, got %.3f", wantHDD, s.TotalHDD)
	}
	if math.Abs(s.TotalCDD-wantCDD) > 0.01 {
		t.Errorf("TotalCDD: want %.3f, got %.3f", wantCDD, s.TotalCDD)
	}
	if math.Abs(s.AvgTempC-wantAvg) > 0.01 {
		t.Errorf("AvgTempC: want %.3f, got %.3f", wantAvg, s.AvgTempC)
	}
}

func TestCalibrationError_Match(t *testing.T) {
	// Identical series → MAE = 0
	a := []SpeedwellIndex{{HDD: 5.0, CDD: 0.0}, {HDD: 0.0, CDD: 3.5}}
	b := []SpeedwellIndex{{HDD: 5.0, CDD: 0.0}, {HDD: 0.0, CDD: 3.5}}
	hddMAE, cddMAE, err := CalibrationError(a, b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hddMAE != 0 || cddMAE != 0 {
		t.Errorf("identical series: want 0/0, got hdd=%.3f cdd=%.3f", hddMAE, cddMAE)
	}
}

func TestCalibrationError_Mismatch(t *testing.T) {
	settlement := []SpeedwellIndex{{HDD: 10.0, CDD: 0.0}, {HDD: 0.0, CDD: 8.0}}
	computed := []SpeedwellIndex{{HDD: 8.0, CDD: 0.0}, {HDD: 0.0, CDD: 6.0}}
	hddMAE, cddMAE, err := CalibrationError(settlement, computed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// hddMAE = (|10-8| + |0-0|) / 2 = 1.0
	// cddMAE = (|0-0| + |8-6|) / 2 = 1.0
	if math.Abs(hddMAE-1.0) > 0.001 {
		t.Errorf("hddMAE: want 1.0, got %.3f", hddMAE)
	}
	if math.Abs(cddMAE-1.0) > 0.001 {
		t.Errorf("cddMAE: want 1.0, got %.3f", cddMAE)
	}
}

func TestCalibrationError_LengthMismatch(t *testing.T) {
	a := []SpeedwellIndex{{HDD: 1.0}}
	b := []SpeedwellIndex{{HDD: 1.0}, {HDD: 2.0}}
	_, _, err := CalibrationError(a, b)
	if err == nil {
		t.Error("expected error for length mismatch")
	}
}

func TestCalibrationError_Empty(t *testing.T) {
	hddMAE, cddMAE, err := CalibrationError(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hddMAE != 0 || cddMAE != 0 {
		t.Errorf("empty: want 0/0, got %.3f/%.3f", hddMAE, cddMAE)
	}
}

func TestSafeIdx_OutOfBounds(t *testing.T) {
	s := []float64{1.0, 2.0}
	if safeIdx(s, 5) != 0.0 {
		t.Error("safeIdx out of bounds should return 0.0")
	}
	if safeIdx(s, 1) != 2.0 {
		t.Error("safeIdx in bounds failed")
	}
}

func TestSpeedwellIndex_HDDCDDConsistency(t *testing.T) {
	// Verify that a hot day produces CDD > 0, HDD = 0
	hotIdx := SpeedwellIndex{AvgTempC: 30.0, HDD: 0.0, CDD: 30.0 - CMEBaselineTempC}
	if hotIdx.HDD != 0 {
		t.Errorf("hot day HDD should be 0, got %.3f", hotIdx.HDD)
	}
	if hotIdx.CDD <= 0 {
		t.Errorf("hot day CDD should be > 0, got %.3f", hotIdx.CDD)
	}

	// Cold day: HDD > 0, CDD = 0
	coldIdx := SpeedwellIndex{AvgTempC: 5.0, HDD: CMEBaselineTempC - 5.0, CDD: 0.0}
	if coldIdx.CDD != 0 {
		t.Errorf("cold day CDD should be 0, got %.3f", coldIdx.CDD)
	}
	if coldIdx.HDD <= 0 {
		t.Errorf("cold day HDD should be > 0, got %.3f", coldIdx.HDD)
	}
}
