package calibration

import (
	"math"
	"testing"
)

func TestBankrollKellyScale(t *testing.T) {
	cases := []struct {
		name            string
		current, initial float64
		wantMin, wantMax float64
	}{
		{"zero initial returns 1.0", 150, 0, 1.0, 1.0},
		{"zero current returns 1.0", 0, 100, 1.0, 1.0},
		{"deep drawdown below 0.70", 60, 100, 0.70, 0.70},
		{"severe drawdown exactly 0.70", 70, 100, 0.70, 0.70},
		{"slight drawdown 0.85 → between floor and neutral", 85, 100, 0.70, 1.00},
		{"at initial value exact", 100, 100, 1.00, 1.00},
		{"modest growth 1.5x → between neutral and ceiling", 150, 100, 1.00, 1.20},
		{"exactly 2x → ceiling", 200, 100, 1.20, 1.20},
		{"beyond 2x → capped at ceiling", 500, 100, 1.20, 1.20},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BankrollKellyScale(tc.current, tc.initial)
			if got < tc.wantMin-1e-9 || got > tc.wantMax+1e-9 {
				t.Errorf("BankrollKellyScale(%.0f, %.0f) = %.4f, want [%.2f, %.2f]",
					tc.current, tc.initial, got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestBankrollKellyScaleMonotonic(t *testing.T) {
	// Scale must be non-decreasing as the bankroll grows.
	initial := 100.0
	prev := BankrollKellyScale(0.01, initial)
	for _, pct := range []float64{0.5, 0.7, 0.85, 1.0, 1.2, 1.5, 2.0, 3.0} {
		got := BankrollKellyScale(pct*initial, initial)
		if got < prev-1e-9 {
			t.Errorf("scale not monotonic: at ratio %.2f got %.4f, prev=%.4f", pct, got, prev)
		}
		prev = got
	}
}

func TestBankrollKellyScaleBounds(t *testing.T) {
	for _, ratio := range []float64{0.01, 0.3, 0.5, 0.69, 0.7, 1.0, 1.5, 2.0, 5.0, 10.0} {
		got := BankrollKellyScale(ratio*100, 100)
		if got < 0.70-1e-9 || got > 1.20+1e-9 {
			t.Errorf("scale %.4f out of [0.70, 1.20] at ratio %.2f", got, ratio)
		}
		// Round-trip: scale should be deterministic.
		got2 := BankrollKellyScale(ratio*100, 100)
		if math.Abs(got-got2) > 1e-9 {
			t.Errorf("BankrollKellyScale not deterministic at ratio %.2f", ratio)
		}
	}
}
