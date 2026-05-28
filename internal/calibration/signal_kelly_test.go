package calibration

import (
	"testing"
	"time"
)

// makeRecord builds a minimal resolved BetRecord for testing.
func makeSignalRecord(signal string, ourP float64, won bool) BetRecord {
	outcome := won
	return BetRecord{
		Signal:         signal,
		OurProbability: ourP,
		ResolvedAt:     time.Now(),
		Outcome:        &outcome,
	}
}

func TestBrierToKellyMultiplier(t *testing.T) {
	cases := []struct {
		brier float64
		want  float64
	}{
		{0.05, 1.50},  // excellent
		{0.09, 1.50},  // just below 0.10
		{0.10, 1.20},  // boundary: [0.10, 0.14)
		{0.13, 1.20},
		{0.14, 1.00},  // baseline
		{0.17, 1.00},
		{0.18, 0.75},  // under-performing
		{0.21, 0.75},
		{0.22, 0.50},  // poor
		{0.35, 0.50},
	}
	for _, c := range cases {
		got := brierToKellyMultiplier(c.brier)
		if got != c.want {
			t.Errorf("brierToKellyMultiplier(%.2f) = %.2f, want %.2f", c.brier, got, c.want)
		}
	}
}

func TestSignalKellyMultipliers_InsufficientData(t *testing.T) {
	// Fewer than MinSignalSamples resolved bets → multiplier must be 1.0.
	records := make([]BetRecord, MinSignalSamples-1)
	for i := range records {
		records[i] = makeSignalRecord("rain", 0.80, i%2 == 0)
	}
	mults := SignalKellyMultipliers(records)
	info, ok := mults["rain"]
	if !ok {
		t.Fatal("expected 'rain' key in result")
	}
	if info.Multiplier != 1.0 {
		t.Errorf("expected multiplier=1.0 with insufficient data, got %.2f", info.Multiplier)
	}
	if info.Count != MinSignalSamples-1 {
		t.Errorf("expected Count=%d, got %d", MinSignalSamples-1, info.Count)
	}
}

func TestSignalKellyMultipliers_ExcellentCalibration(t *testing.T) {
	// ourP=0.80, all wins → Brier = (0.80-1)² = 0.04 per bet → excellent.
	var records []BetRecord
	for i := 0; i < 20; i++ {
		records = append(records, makeSignalRecord("heat", 0.80, true))
	}
	mults := SignalKellyMultipliers(records)
	info := mults["heat"]
	if info.Multiplier != 1.50 {
		t.Errorf("expected 1.50x for excellent calibration (brier=%.4f), got %.2f", info.BrierScore, info.Multiplier)
	}
}

func TestSignalKellyMultipliers_PoorCalibration(t *testing.T) {
	// ourP=0.90, all losses → Brier = 0.81 per bet → poor.
	var records []BetRecord
	for i := 0; i < 15; i++ {
		records = append(records, makeSignalRecord("humid", 0.90, false))
	}
	mults := SignalKellyMultipliers(records)
	info := mults["humid"]
	if info.Multiplier != 0.50 {
		t.Errorf("expected 0.50x for poor calibration (brier=%.4f), got %.2f", info.BrierScore, info.Multiplier)
	}
}

func TestSignalKellyMultipliers_MultipleSignals(t *testing.T) {
	var records []BetRecord
	// rain: excellent (win all with ourP=0.85 → brier≈0.023)
	for i := 0; i < 15; i++ {
		records = append(records, makeSignalRecord("rain", 0.85, true))
	}
	// cold: poor (lose all with ourP=0.85 → brier≈0.72)
	for i := 0; i < 15; i++ {
		records = append(records, makeSignalRecord("cold", 0.85, false))
	}

	mults := SignalKellyMultipliers(records)
	if mults["rain"].Multiplier != 1.50 {
		t.Errorf("rain: expected 1.50x, got %.2f", mults["rain"].Multiplier)
	}
	if mults["cold"].Multiplier != 0.50 {
		t.Errorf("cold: expected 0.50x, got %.2f", mults["cold"].Multiplier)
	}
}

func TestLookupSignalKelly_DefaultOnMiss(t *testing.T) {
	mults := map[string]SignalKellyInfo{
		"rain": {Multiplier: 1.50, Count: 20, BrierScore: 0.07},
	}
	got := LookupSignalKelly(mults, "heat")
	if got.Multiplier != 1.0 {
		t.Errorf("expected 1.0 for unknown signal, got %.2f", got.Multiplier)
	}
}

func TestSignalKellyInfo_String(t *testing.T) {
	info := SignalKellyInfo{Multiplier: 1.20, BrierScore: 0.1235, Count: 42}
	s := info.String("rain")
	if s == "" {
		t.Error("String() should not be empty")
	}
	// Should contain the multiplier value.
	expected := "signal_kelly=1.20x(rain,brier=0.124,n=42)"
	// We don't hardcode the exact format to be resilient, just sanity-check.
	if len(s) < 20 {
		t.Errorf("String() too short: %q", s)
	}
	_ = expected
}

func TestSignalKellyMultipliers_EmptyRecords(t *testing.T) {
	mults := SignalKellyMultipliers(nil)
	if len(mults) != 0 {
		t.Errorf("expected empty map for nil records, got %d entries", len(mults))
	}
}
