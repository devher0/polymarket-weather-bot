package collectors

import (
	"math"
	"testing"
	"time"
)

func TestHorizonDecayLinear(t *testing.T) {
	tests := []struct {
		name     string
		hours    float64
		wantMin  float64
		wantMax  float64
	}{
		{
			name:    "same-day (0 h)",
			hours:   0,
			wantMin: 1.0,
			wantMax: 1.0,
		},
		{
			name:    "24 h out",
			hours:   24,
			wantMin: 0.93,
			wantMax: 0.95,
		},
		{
			name:    "48 h out",
			hours:   48,
			wantMin: 0.87,
			wantMax: 0.89,
		},
		{
			name:    "96 h out",
			hours:   96,
			wantMin: 0.75,
			wantMax: 0.77,
		},
		{
			name:    "120+ h — floor 0.65",
			hours:   200,
			wantMin: 0.65,
			wantMax: 0.65,
		},
		{
			name:    "boundary: exactly 140 h → floor",
			hours:   140,
			wantMin: 0.65,
			wantMax: 0.65,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := HorizonDecayLinear(tc.hours)
			if got < tc.wantMin-1e-9 || got > tc.wantMax+1e-9 {
				t.Errorf("HorizonDecayLinear(%.0f) = %.4f, want [%.2f, %.2f]",
					tc.hours, got, tc.wantMin, tc.wantMax)
			}
		})
	}

	// Monotonic: decay must never increase as horizonHours grows.
	t.Run("monotonic decrease", func(t *testing.T) {
		prev := HorizonDecayLinear(0)
		for h := 12.0; h <= 200; h += 12 {
			cur := HorizonDecayLinear(h)
			if cur > prev+1e-9 {
				t.Errorf("non-monotonic: f(%.0f)=%.4f > f(%.0f)=%.4f", h, cur, h-12, prev)
			}
			prev = cur
		}
	})
}

func TestHorizonDecay(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		targetHours float64 // hours after base to set as target
		fetchedAt   time.Time
		wantDecay   float64
	}{
		{
			name:        "same moment (0 h)",
			targetHours: 0,
			fetchedAt:   base,
			wantDecay:   1.0,
		},
		{
			name:        "24 h",
			targetHours: 24,
			fetchedAt:   base,
			wantDecay:   HorizonDecayLinear(24),
		},
		{
			name:        "48 h",
			targetHours: 48,
			fetchedAt:   base,
			wantDecay:   HorizonDecayLinear(48),
		},
		{
			name:        "past target (negative) → 1.0",
			targetHours: -6,
			fetchedAt:   base,
			wantDecay:   1.0, // negative horizon → same-day treatment
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := base.Add(time.Duration(tc.targetHours * float64(time.Hour)))
			got := HorizonDecay(target, tc.fetchedAt)
			if math.Abs(got-tc.wantDecay) > 1e-9 {
				t.Errorf("HorizonDecay: got %.4f, want %.4f", got, tc.wantDecay)
			}
		})
	}
}
