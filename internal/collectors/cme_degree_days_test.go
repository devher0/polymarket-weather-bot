// cme_degree_days_test.go — unit tests for TASK-093: CME HDD/CDD indices.
package collectors

import (
	"math"
	"testing"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

func TestComputeDegreeDays_Hot(t *testing.T) {
	// avg = (35 + 25) / 2 = 30°C — well above 18.333°C baseline
	f := weather.Forecast{MaxTempC: 35, MinTempC: 25}
	dd := ComputeDegreeDays(f)

	if dd.HDD != 0 {
		t.Errorf("HDD should be 0 on a hot day, got %.3f", dd.HDD)
	}
	want := 30.0 - CMEBaselineTempC
	if math.Abs(dd.CDD-want) > 0.001 {
		t.Errorf("CDD: want %.3f, got %.3f", want, dd.CDD)
	}
	if math.Abs(dd.AvgTempC-30.0) > 0.001 {
		t.Errorf("AvgTempC: want 30.0, got %.3f", dd.AvgTempC)
	}
}

func TestComputeDegreeDays_Cold(t *testing.T) {
	// avg = (5 + -5) / 2 = 0°C — well below 18.333°C baseline
	f := weather.Forecast{MaxTempC: 5, MinTempC: -5}
	dd := ComputeDegreeDays(f)

	if dd.CDD != 0 {
		t.Errorf("CDD should be 0 on a cold day, got %.3f", dd.CDD)
	}
	want := CMEBaselineTempC - 0.0
	if math.Abs(dd.HDD-want) > 0.001 {
		t.Errorf("HDD: want %.3f, got %.3f", want, dd.HDD)
	}
}

func TestComputeDegreeDays_AtBaseline(t *testing.T) {
	// avg = 18.333°C exactly → both HDD and CDD should be 0
	f := weather.Forecast{MaxTempC: CMEBaselineTempC + 5, MinTempC: CMEBaselineTempC - 5}
	dd := ComputeDegreeDays(f)

	if dd.HDD != 0 {
		t.Errorf("HDD should be 0 at baseline, got %.6f", dd.HDD)
	}
	if dd.CDD != 0 {
		t.Errorf("CDD should be 0 at baseline, got %.6f", dd.CDD)
	}
}

func TestComputeAccumulatedDegreeDays(t *testing.T) {
	forecasts := []weather.Forecast{
		{MaxTempC: 30, MinTempC: 20}, // avg 25°C → CDD = 25 - 18.333 = 6.667
		{MaxTempC: 10, MinTempC: 0},  // avg  5°C → HDD = 18.333 - 5    = 13.333
		{MaxTempC: 20, MinTempC: 17}, // avg 18.5°C → CDD ≈ 0.167
	}
	totalHDD, totalCDD := ComputeAccumulatedDegreeDays(forecasts)

	wantHDD := 13.333
	wantCDD := (25.0-CMEBaselineTempC) + (18.5-CMEBaselineTempC)
	if math.Abs(totalHDD-wantHDD) > 0.01 {
		t.Errorf("totalHDD: want ~%.3f, got %.3f", wantHDD, totalHDD)
	}
	if math.Abs(totalCDD-wantCDD) > 0.01 {
		t.Errorf("totalCDD: want ~%.3f, got %.3f", wantCDD, totalCDD)
	}
}

func TestHeatProbabilityFromCDD_Ranges(t *testing.T) {
	// threshold = 5 CDD (default)
	// hot day: avg ~30°C → CDD ≈ 11.67, diff ≈ 6.67 → should be ≥ 0.90
	hot := weather.Forecast{MaxTempC: 35, MinTempC: 25}
	p := HeatProbabilityFromCDD(hot, 5.0)
	if p < 0.90 {
		t.Errorf("hot day: expected p >= 0.90, got %.3f", p)
	}

	// mild day: avg ~18.5°C → CDD ≈ 0.17, diff ≈ -4.83 → should be in (0.30, 0.50)
	mild := weather.Forecast{MaxTempC: 20, MinTempC: 17}
	p2 := HeatProbabilityFromCDD(mild, 5.0)
	if p2 >= 0.50 {
		t.Errorf("mild day: expected p < 0.50, got %.3f", p2)
	}

	// cold day: avg ~0°C → CDD = 0, HDD = 18.333 → well below threshold
	cold := weather.Forecast{MaxTempC: 5, MinTempC: -5}
	p3 := HeatProbabilityFromCDD(cold, 5.0)
	if p3 > 0.20 {
		t.Errorf("cold day: expected p <= 0.20, got %.3f", p3)
	}
}

func TestColdProbabilityFromHDD_Ranges(t *testing.T) {
	// cold day: avg 0°C → HDD = 18.333, diff vs threshold 5 = 13.333 → should be ≥ 0.90
	cold := weather.Forecast{MaxTempC: 5, MinTempC: -5}
	p := ColdProbabilityFromHDD(cold, 5.0)
	if p < 0.90 {
		t.Errorf("cold day: expected p >= 0.90, got %.3f", p)
	}

	// hot day: avg 30°C → HDD = 0, diff = -5 → should be ≤ 0.20
	hot := weather.Forecast{MaxTempC: 35, MinTempC: 25}
	p2 := ColdProbabilityFromHDD(hot, 5.0)
	if p2 > 0.20 {
		t.Errorf("hot day: expected p <= 0.20, got %.3f", p2)
	}
}

func TestHeatProbabilityFromCDD_DefaultThreshold(t *testing.T) {
	// threshold <= 0 should default to 5.0 without panic
	f := weather.Forecast{MaxTempC: 25, MinTempC: 15}
	p := HeatProbabilityFromCDD(f, 0)
	if p < 0 || p > 1 {
		t.Errorf("probability out of [0,1]: %.3f", p)
	}
}

func TestColdProbabilityFromHDD_DefaultThreshold(t *testing.T) {
	f := weather.Forecast{MaxTempC: 5, MinTempC: -5}
	p := ColdProbabilityFromHDD(f, -1)
	if p < 0 || p > 1 {
		t.Errorf("probability out of [0,1]: %.3f", p)
	}
}
