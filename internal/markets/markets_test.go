package markets

import (
	"testing"
)

// TestClassify_TASK048 covers TASK-048 extended regex for market classification.
func TestClassify_TASK048(t *testing.T) {
	tests := []struct {
		name        string
		question    string
		wantCity    string
		wantSignal  string
		wantThreshC float64 // 0 means "not set / don't check"
	}{
		// City nicknames
		{
			name:       "Big Apple → new_york",
			question:   "Will the Big Apple see rain on Friday?",
			wantCity:   "new_york",
			wantSignal: "rain",
		},
		{
			name:       "Windy City → chicago",
			question:   "Will the Windy City temperature drop below 5 degrees?",
			wantCity:   "chicago",
			wantSignal: "cold",
		},
		{
			name:       "City of Light → paris",
			question:   "Rain in the City of Light this weekend?",
			wantCity:   "paris",
			wantSignal: "rain",
		},
		{
			name:       "Silicon Valley → san_francisco",
			question:   "Will Silicon Valley see fog tomorrow?",
			wantCity:   "san_francisco",
			wantSignal: "fog",
		},

		// New signals
		{
			name:       "fog signal",
			question:   "Will London be foggy on December 15?",
			wantCity:   "london",
			wantSignal: "fog",
		},
		{
			name:       "humid signal",
			question:   "Will Miami humidity exceed 90% this week?",
			wantCity:   "miami",
			wantSignal: "humid",
		},
		{
			name:       "dry signal",
			question:   "Will New York see a drought warning in July?",
			wantCity:   "new_york",
			wantSignal: "dry",
		},

		// Unitless temperature (> 50 → Fahrenheit)
		{
			name:       "unitless degrees > 50 → Fahrenheit",
			question:   "Will Tokyo temperature exceed 95 degrees on Tuesday?",
			wantCity:   "tokyo",
			wantSignal: "heat",
			// 95°F → 35°C
			wantThreshC: 35.0,
		},
		{
			name:       "unitless degrees ≤ 50 → Celsius",
			question:   "Will Berlin temperature stay above 30 degrees tomorrow?",
			wantCity:   "berlin",
			wantSignal: "heat",
			wantThreshC: 30.0,
		},

		// Temperature range
		{
			name:       "temperature range Celsius → upper bound",
			question:   "Will Paris have a high between 20°C and 30°C next week?",
			wantCity:   "paris",
			wantSignal: "heat",
			wantThreshC: 30.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			city, sig, threshC := classify(tc.question)

			if city != tc.wantCity {
				t.Errorf("city: got %q, want %q", city, tc.wantCity)
			}
			if sig != tc.wantSignal {
				t.Errorf("signal: got %q, want %q", sig, tc.wantSignal)
			}
			if tc.wantThreshC != 0 {
				if abs(threshC-tc.wantThreshC) > 0.6 { // 0.6°C tolerance for F→C rounding
					t.Errorf("thresholdC: got %.2f, want %.2f", threshC, tc.wantThreshC)
				}
			}
		})
	}
}

// TestParseTempThresholdC_Range ensures "between X and Y" uses the upper bound.
func TestParseTempThresholdC_Range(t *testing.T) {
	got := parseTempThresholdC("temperature between 68°F and 86°F")
	// 86°F → 30°C
	if abs(got-30.0) > 0.6 {
		t.Errorf("range: got %.2f, want ~30.0°C", got)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
