// snow_test.go — unit tests for SnowProbability (TASK-142).
package weather

import "testing"

func TestSnowProbability_NoSnowfall(t *testing.T) {
	f := Forecast{SnowfallCM: 0}
	// Falls back to proxy. With no precip and warm temp the proxy should be very low.
	f.MaxTempC = 20
	f.PrecipitationProbability = 5
	p := SnowProbability(f)
	if p > 0.10 {
		t.Errorf("expected very low probability for warm/dry conditions, got %.3f", p)
	}
}

func TestSnowProbability_TraceSnow(t *testing.T) {
	// 0 < cm < 2 → 0.25
	f := Forecast{SnowfallCM: 1.0}
	p := SnowProbability(f)
	if p != 0.25 {
		t.Errorf("expected 0.25 for trace snowfall (1cm), got %.3f", p)
	}
}

func TestSnowProbability_LightSnow(t *testing.T) {
	// 0 < cm < 2, boundary
	f := Forecast{SnowfallCM: 1.9}
	p := SnowProbability(f)
	if p != 0.25 {
		t.Errorf("expected 0.25 for 1.9cm snowfall, got %.3f", p)
	}
}

func TestSnowProbability_ModerateSnow(t *testing.T) {
	// 2 ≤ cm < 5 → 0.60
	f := Forecast{SnowfallCM: 3.0}
	p := SnowProbability(f)
	if p != 0.60 {
		t.Errorf("expected 0.60 for moderate snowfall (3cm), got %.3f", p)
	}
}

func TestSnowProbability_HeavySnow(t *testing.T) {
	// 5 ≤ cm < 10 → 0.85
	f := Forecast{SnowfallCM: 7.5}
	p := SnowProbability(f)
	if p != 0.85 {
		t.Errorf("expected 0.85 for heavy snowfall (7.5cm), got %.3f", p)
	}
}

func TestSnowProbability_VeryHeavySnow(t *testing.T) {
	// cm ≥ 10 → 0.95
	f := Forecast{SnowfallCM: 15.0}
	p := SnowProbability(f)
	if p != 0.95 {
		t.Errorf("expected 0.95 for very heavy snowfall (15cm), got %.3f", p)
	}
}

func TestSnowProbability_Boundary5cm(t *testing.T) {
	// Exactly 5cm → 0.85
	f := Forecast{SnowfallCM: 5.0}
	p := SnowProbability(f)
	if p != 0.85 {
		t.Errorf("expected 0.85 for exactly 5cm, got %.3f", p)
	}
}

func TestSnowProbability_Boundary10cm(t *testing.T) {
	// Exactly 10cm → 0.95
	f := Forecast{SnowfallCM: 10.0}
	p := SnowProbability(f)
	if p != 0.95 {
		t.Errorf("expected 0.95 for exactly 10cm, got %.3f", p)
	}
}

func TestSnowProbability_FallbackColdRain(t *testing.T) {
	// SnowfallCM == 0: fall back to proxy
	// Cold + rainy conditions → non-trivial snow probability
	f := Forecast{
		SnowfallCM:               0,
		MaxTempC:                 -5, // well below 2°C → high cold factor
		PrecipitationProbability: 80,
		PrecipitationMM:          5,
	}
	p := SnowProbability(f)
	if p < 0.30 {
		t.Errorf("expected >0.30 from proxy for cold+rainy conditions, got %.3f", p)
	}
}

func TestSnowProbability_FallbackWarmNoRain(t *testing.T) {
	// SnowfallCM == 0: fallback; warm + dry → very low
	f := Forecast{
		SnowfallCM:               0,
		MaxTempC:                 25,
		PrecipitationProbability: 10,
		PrecipitationMM:          0,
	}
	p := SnowProbability(f)
	if p > 0.05 {
		t.Errorf("expected <0.05 from proxy for warm/dry conditions, got %.3f", p)
	}
}

func TestSnowProbability_InRange(t *testing.T) {
	cases := []float64{0, 0.5, 2.0, 5.0, 10.0, 20.0}
	for _, cm := range cases {
		f := Forecast{SnowfallCM: cm}
		p := SnowProbability(f)
		if p < 0 || p > 1 {
			t.Errorf("SnowProbability out of [0,1] for snowfallCM=%.1f: %.3f", cm, p)
		}
	}
}
