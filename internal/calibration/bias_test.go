package calibration

import (
	"math"
	"testing"
)

func TestComputeBias_NoData(t *testing.T) {
	dir := t.TempDir()
	bias := ComputeBias("new_york", "rain", dir)
	if bias != 0 {
		t.Errorf("expected 0 bias with no data, got %v", bias)
	}
}

func TestRecordAndComputeBias(t *testing.T) {
	dir := t.TempDir()

	// Record 5 samples: ourP=0.8, outcome=0 (lost) → bias = 0.8
	for i := 0; i < 5; i++ {
		if err := RecordBiasOutcome("new_york", "rain", 0.8, false, dir); err != nil {
			t.Fatal(err)
		}
	}

	bias := ComputeBias("new_york", "rain", dir)
	if math.Abs(bias-0.8) > 0.01 {
		t.Errorf("expected bias ~0.8, got %.4f", bias)
	}
}

func TestCorrectProbability_NoData(t *testing.T) {
	dir := t.TempDir()
	corrected, bias := CorrectProbability("london", "heat", 0.6, dir)
	if corrected != 0.6 || bias != 0 {
		t.Errorf("expected unchanged (no data), got corrected=%.3f bias=%.3f", corrected, bias)
	}
}

func TestCorrectProbability_ClampLower(t *testing.T) {
	dir := t.TempDir()
	// Record 5 samples with large overestimation: ourP=0.95, outcome=0 → bias≈0.95
	for i := 0; i < 5; i++ {
		if err := RecordBiasOutcome("miami", "heat", 0.95, false, dir); err != nil {
			t.Fatal(err)
		}
	}
	// Applying bias to small ourP: 0.10 - 0.95 = -0.85 → clamped to 0.02
	corrected, _ := CorrectProbability("miami", "heat", 0.10, dir)
	if corrected < 0.02 {
		t.Errorf("expected clamp to 0.02, got %.4f", corrected)
	}
	if corrected > 0.02+1e-6 {
		t.Errorf("expected exactly 0.02 after clamping, got %.4f", corrected)
	}
}

func TestBiasSummary_Empty(t *testing.T) {
	dir := t.TempDir()
	rows := LoadBiasSummary(dir)
	if len(rows) != 0 {
		t.Errorf("expected empty summary, got %d rows", len(rows))
	}
}

func TestBiasMaxRecordsRollingCap(t *testing.T) {
	dir := t.TempDir()
	// Record 35 entries — more than biasMaxRecords=30.
	for i := 0; i < 35; i++ {
		if err := RecordBiasOutcome("berlin", "cold", 0.5, true, dir); err != nil {
			t.Fatal(err)
		}
	}
	records, err := LoadBiasRecords("berlin", "cold", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != biasMaxRecords {
		t.Errorf("expected %d records (rolling cap), got %d", biasMaxRecords, len(records))
	}
}

func TestSplitCitySignal(t *testing.T) {
	cases := []struct {
		name, wantCity, wantSignal string
	}{
		{"new_york_rain", "new_york", "rain"},
		{"los_angeles_heat", "los_angeles", "heat"},
		{"london_snow", "london", "snow"},
		{"miami_wind", "miami", "wind"},
		{"san_francisco_sunny", "san_francisco", "sunny"},
		{"berlin_cold", "berlin", "cold"},
	}
	for _, tc := range cases {
		city, sig := splitCitySignal(tc.name)
		if city != tc.wantCity || sig != tc.wantSignal {
			t.Errorf("splitCitySignal(%q) = (%q, %q), want (%q, %q)",
				tc.name, city, sig, tc.wantCity, tc.wantSignal)
		}
	}
}
