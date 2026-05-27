package collectors

import (
	"math"
	"os"
	"testing"
	"time"
)

func TestComputeDriftFactor_Empty(t *testing.T) {
	f := ComputeDriftFactor(nil)
	if f != 1.0 {
		t.Errorf("expected 1.0 for empty records, got %.3f", f)
	}
}

func TestComputeDriftFactor_AllStable(t *testing.T) {
	// Near-zero shifts → factor should be close to 1.0
	records := []DriftRecord{
		{Timestamp: time.Now(), AbsDeltaTempC: 0.1, AbsDeltaPrecipPt: 0.5},
		{Timestamp: time.Now(), AbsDeltaTempC: 0.2, AbsDeltaPrecipPt: 1.0},
		{Timestamp: time.Now(), AbsDeltaTempC: 0.0, AbsDeltaPrecipPt: 0.0},
	}
	f := ComputeDriftFactor(records)
	if f < 0.96 {
		t.Errorf("expected stable factor ≥ 0.96, got %.3f", f)
	}
}

func TestComputeDriftFactor_HighDrift(t *testing.T) {
	// Max-instability shifts (10°C temp + 40pp precip each) → factor approaches floor 0.70
	records := []DriftRecord{
		{Timestamp: time.Now(), AbsDeltaTempC: 12.0, AbsDeltaPrecipPt: 50.0},
		{Timestamp: time.Now(), AbsDeltaTempC: 11.0, AbsDeltaPrecipPt: 45.0},
		{Timestamp: time.Now(), AbsDeltaTempC: 10.0, AbsDeltaPrecipPt: 40.0},
	}
	f := ComputeDriftFactor(records)
	if f > 0.72 {
		t.Errorf("expected high-drift factor ≤ 0.72, got %.3f", f)
	}
	if f < 0.70 {
		t.Errorf("factor should not drop below 0.70 floor, got %.3f", f)
	}
}

func TestComputeDriftFactor_FloorRespected(t *testing.T) {
	records := make([]DriftRecord, 10)
	for i := range records {
		records[i] = DriftRecord{
			Timestamp:        time.Now(),
			AbsDeltaTempC:    100.0,
			AbsDeltaPrecipPt: 100.0,
		}
	}
	f := ComputeDriftFactor(records)
	if f < 0.70 {
		t.Errorf("factor must be ≥ 0.70 (floor), got %.3f", f)
	}
}

func TestComputeDriftFactor_SingleRecord(t *testing.T) {
	records := []DriftRecord{
		{Timestamp: time.Now(), AbsDeltaTempC: 5.0, AbsDeltaPrecipPt: 20.0},
	}
	// instability = 5/10 + 20/40 = 0.5 + 0.5 = 1.0 (clamped)
	// factor = 1 - 0.30*1.0 = 0.70
	f := ComputeDriftFactor(records)
	want := 0.70
	if math.Abs(f-want) > 0.005 {
		t.Errorf("expected %.3f, got %.3f", want, f)
	}
}

func TestComputeDriftFactor_RecentWeightedMore(t *testing.T) {
	// Two records: (old=stable, new=high) vs (old=high, new=stable).
	// Because the newest record has weight 1.0 vs older 0.8, "new=high" case
	// should yield a lower factor than "new=stable" case.
	recentHigh := []DriftRecord{
		{Timestamp: time.Now().Add(-2 * time.Hour), AbsDeltaTempC: 0.0, AbsDeltaPrecipPt: 0.0},  // old, stable
		{Timestamp: time.Now(), AbsDeltaTempC: 10.0, AbsDeltaPrecipPt: 40.0},                     // newest, high
	}
	recentStable := []DriftRecord{
		{Timestamp: time.Now().Add(-2 * time.Hour), AbsDeltaTempC: 10.0, AbsDeltaPrecipPt: 40.0}, // old, high
		{Timestamp: time.Now(), AbsDeltaTempC: 0.0, AbsDeltaPrecipPt: 0.0},                       // newest, stable
	}
	fHigh := ComputeDriftFactor(recentHigh)
	fStable := ComputeDriftFactor(recentStable)
	// Recent high → lower factor than recent stable
	if fHigh >= fStable {
		t.Errorf("recent-high factor (%.3f) should be < recent-stable factor (%.3f)",
			fHigh, fStable)
	}
}

func TestRecordDrift_PersistsAndCaps(t *testing.T) {
	dir := t.TempDir()
	shift := &ForecastShift{City: "test_city", DeltaMaxTempC: 3.5, DeltaPrecipP: 12.0}

	// Write more than driftHistoryMax records and confirm cap.
	for i := 0; i < driftHistoryMax+5; i++ {
		RecordDrift("test_city", 0, shift, dir)
	}

	records := loadDriftHistory("test_city", 0, dir)
	if len(records) != driftHistoryMax {
		t.Errorf("expected %d records (capped), got %d", driftHistoryMax, len(records))
	}
}

func TestRecordDrift_NilShiftNoOp(t *testing.T) {
	dir := t.TempDir()
	RecordDrift("city", 0, nil, dir)
	// File should not exist.
	_, err := os.Stat(dir + "/data/drift/city_d0.json")
	if !os.IsNotExist(err) {
		t.Error("expected no drift file created for nil shift")
	}
}

func TestLoadDriftSummary_Empty(t *testing.T) {
	dir := t.TempDir()
	rec, factor := LoadDriftSummary("nowhere", 0, dir)
	if factor != 1.0 {
		t.Errorf("expected factor 1.0 when no history, got %.3f", factor)
	}
	if !rec.Timestamp.IsZero() {
		t.Error("expected zero DriftRecord when no history")
	}
}
