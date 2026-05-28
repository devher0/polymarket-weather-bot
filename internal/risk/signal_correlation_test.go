package risk_test

import (
	"testing"

	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/risk"
)

func TestSignalsAntiCorrelated(t *testing.T) {
	pairs := [][2]string{
		{"rain", "sunny"},
		{"sunny", "rain"},
		{"heat", "cold"},
		{"cold", "heat"},
		{"snow", "heat"},
		{"fog", "sunny"},
		{"rain", "dry"},
		{"dry", "rain"},
	}
	for _, p := range pairs {
		if !risk.SignalsAntiCorrelated(p[0], p[1]) {
			t.Errorf("expected %s↔%s to be anti-correlated", p[0], p[1])
		}
	}
	// non-anti-correlated pairs
	safe := [][2]string{
		{"rain", "wind"},
		{"heat", "wind"},
		{"snow", "fog"},
	}
	for _, p := range safe {
		if risk.SignalsAntiCorrelated(p[0], p[1]) {
			t.Errorf("expected %s↔%s NOT to be anti-correlated", p[0], p[1])
		}
	}
}

func TestIntraCitySignalConflict_NoPlaced(t *testing.T) {
	m := markets.Market{City: "new_york", Signal: "rain"}
	conflict, _ := risk.IntraCitySignalConflict(m, nil)
	if conflict {
		t.Error("expected no conflict when no placed markets")
	}
}

func TestIntraCitySignalConflict_DifferentCity(t *testing.T) {
	candidate := markets.Market{City: "new_york", Signal: "rain"}
	placed := []markets.Market{{City: "london", Signal: "sunny"}}
	conflict, _ := risk.IntraCitySignalConflict(candidate, placed)
	if conflict {
		t.Error("expected no conflict — different city")
	}
}

func TestIntraCitySignalConflict_SameSignal(t *testing.T) {
	candidate := markets.Market{City: "new_york", Signal: "rain"}
	placed := []markets.Market{{City: "new_york", Signal: "rain"}}
	conflict, _ := risk.IntraCitySignalConflict(candidate, placed)
	if conflict {
		t.Error("expected no conflict — same signal (dedup handles this)")
	}
}

func TestIntraCitySignalConflict_AntiCorrelated(t *testing.T) {
	candidate := markets.Market{City: "new_york", Signal: "sunny"}
	placed := []markets.Market{{City: "new_york", Signal: "rain"}}
	conflict, reason := risk.IntraCitySignalConflict(candidate, placed)
	if !conflict {
		t.Error("expected conflict: sunny vs rain in same city")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestIntraCitySignalConflict_CompatibleSignals(t *testing.T) {
	// rain and wind can coexist (stormy day)
	candidate := markets.Market{City: "miami", Signal: "wind"}
	placed := []markets.Market{{City: "miami", Signal: "rain"}}
	conflict, _ := risk.IntraCitySignalConflict(candidate, placed)
	if conflict {
		t.Error("expected no conflict: rain and wind can both be YES")
	}
}
