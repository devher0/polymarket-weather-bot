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

// TestClassifyUV tests TASK-083: UV index signal classification.
func TestClassifyUV(t *testing.T) {
	tests := []struct {
		name        string
		question    string
		wantCity    string
		wantSignal  string
		wantThreshC float64
	}{
		{
			name:        "uv index above 8 miami",
			question:    "Will the UV index exceed 8 in Miami today?",
			wantCity:    "miami",
			wantSignal:  "uv",
			wantThreshC: 8,
		},
		{
			name:        "uv index 10 nyc",
			question:    "Will UV index reach 10 in New York this week?",
			wantCity:    "new_york",
			wantSignal:  "uv",
			wantThreshC: 10,
		},
		{
			name:        "uv index no threshold defaults to 8",
			question:    "Is the UV index high in London today?",
			wantCity:    "london",
			wantSignal:  "uv",
			wantThreshC: 8, // default
		},
		{
			name:        "ultraviolet index signal",
			question:    "Will ultraviolet index be above 9 in Tokyo?",
			wantCity:    "tokyo",
			wantSignal:  "uv",
			wantThreshC: 9,
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
			if abs(threshC-tc.wantThreshC) > 0.5 {
				t.Errorf("thresholdC: got %.1f, want %.1f", threshC, tc.wantThreshC)
			}
		})
	}
}

// TestParseUVThreshold tests the UV threshold parser.
func TestParseUVThreshold(t *testing.T) {
	tests := []struct {
		question string
		want     float64
	}{
		{"UV index above 8 in Miami", 8},
		{"Will UV level exceed 11 today?", 11},
		{"UV index of 6 or higher", 6},
		{"Is the UV index high?", 0}, // no number → 0 (caller uses default)
		{"ultraviolet index above 10", 10},
	}

	for _, tc := range tests {
		got := parseUVThreshold(tc.question)
		if abs(got-tc.want) > 0.1 {
			t.Errorf("parseUVThreshold(%q): got %.1f, want %.1f", tc.question, got, tc.want)
		}
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
