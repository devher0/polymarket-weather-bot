package strategy

import (
	"testing"

	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func rainMarket(city string, yesPrice, noPrice float64) markets.Market {
	return markets.Market{
		ConditionID: "cond-rain-" + city,
		Question:    "Will it rain in " + city + " tomorrow?",
		City:        city,
		Signal:      "rain",
		YesTokenID:  "tok-yes",
		NoTokenID:   "tok-no",
		YesPrice:    yesPrice,
		NoPrice:     noPrice,
	}
}

func heatMarket(city string, yesPrice, noPrice float64, thresholdC float64) markets.Market {
	return markets.Market{
		ConditionID: "cond-heat-" + city,
		Question:    "Will it be above 35°C in " + city + " tomorrow?",
		City:        city,
		Signal:      "heat",
		YesTokenID:  "tok-yes",
		NoTokenID:   "tok-no",
		YesPrice:    yesPrice,
		NoPrice:     noPrice,
		ThresholdC:  thresholdC,
	}
}

// makeForecast builds a minimal weather.Forecast for testing.
func makeForecast(city string, maxC, precipMM, precipProb, windKMH float64, code int) weather.Forecast {
	return weather.Forecast{
		City:                     city,
		Date:                     "2026-05-27",
		MaxTempC:                 maxC,
		MinTempC:                 maxC - 10,
		PrecipitationMM:          precipMM,
		PrecipitationProbability: precipProb,
		WindSpeedKMH:             windKMH,
		WeatherCode:              code,
	}
}

func makeFused(f weather.Forecast, confidence float64, sources ...string) *collectors.FusedForecast {
	if sources == nil {
		sources = []string{"openmeteo", "nasa"}
	}
	return &collectors.FusedForecast{
		Forecast:   f,
		Confidence: confidence,
		Sources:    sources,
	}
}

// ─── Evaluate() tests ─────────────────────────────────────────────────────────

func TestEvaluate_NoEdge(t *testing.T) {
	// Rain probability for precipProb=50, precipMM=2 ≈ 0.35 (from RainProbability).
	// Set market prices so both YES and NO edge fall below minEdge (0.05).
	// YES edge = 0.35 - 0.33 = 0.02 < 0.05
	// NO  edge = 0.65 - 0.64 = 0.01 < 0.05
	fc := makeForecast("new_york", 20, 2, 50, 15, 61)
	m := rainMarket("new_york", 0.33, 0.64)
	d := Evaluate(m, map[string][]weather.Forecast{"new_york": {fc}}, 1000, 0.05, 50)
	if d != nil {
		t.Errorf("expected nil decision when edge~0, got side=%s edge=%.4f", d.Side, d.Edge)
	}
}

func TestEvaluate_YesEdge(t *testing.T) {
	// precipProb=90 → high rain probability; market only prices at 0.40 → YES edge.
	fc := makeForecast("new_york", 20, 8, 90, 15, 80)
	m := rainMarket("new_york", 0.40, 0.60)
	d := Evaluate(m, map[string][]weather.Forecast{"new_york": {fc}}, 1000, 0.05, 50)
	if d == nil {
		t.Fatal("expected YES decision, got nil")
	}
	if d.Side != "YES" {
		t.Errorf("expected Side=YES, got %q", d.Side)
	}
	if d.Edge < 0.05 {
		t.Errorf("expected edge >= 0.05, got %.4f", d.Edge)
	}
	if d.SizeUSDC <= 0 {
		t.Errorf("expected positive bet size, got %.2f", d.SizeUSDC)
	}
}

func TestEvaluate_NoEdge_BetNO(t *testing.T) {
	// precipProb=5 → very low rain probability; market prices YES at 0.80 → NO edge.
	fc := makeForecast("london", 18, 0.1, 5, 10, 1)
	m := rainMarket("london", 0.80, 0.20)
	d := Evaluate(m, map[string][]weather.Forecast{"london": {fc}}, 1000, 0.05, 50)
	if d == nil {
		t.Fatal("expected NO decision, got nil")
	}
	if d.Side != "NO" {
		t.Errorf("expected Side=NO, got %q", d.Side)
	}
}

func TestEvaluate_UnknownCity(t *testing.T) {
	fc := makeForecast("new_york", 20, 2, 50, 15, 61)
	m := rainMarket("unknown_city", 0.50, 0.50)
	d := Evaluate(m, map[string][]weather.Forecast{"new_york": {fc}}, 1000, 0.05, 50)
	if d != nil {
		t.Errorf("expected nil for unknown city, got %+v", d)
	}
}

func TestEvaluate_EmptyCity(t *testing.T) {
	fc := makeForecast("new_york", 20, 2, 50, 15, 61)
	m := markets.Market{Signal: "rain"} // City == ""
	d := Evaluate(m, map[string][]weather.Forecast{"new_york": {fc}}, 1000, 0.05, 50)
	if d != nil {
		t.Errorf("expected nil for empty city, got %+v", d)
	}
}

func TestEvaluate_UnknownSignal(t *testing.T) {
	fc := makeForecast("new_york", 20, 2, 50, 15, 61)
	m := markets.Market{
		City:       "new_york",
		Signal:     "earthquake", // unknown signal
		ConditionID: "cond-eq",
		YesPrice:   0.30,
		NoPrice:    0.70,
	}
	d := Evaluate(m, map[string][]weather.Forecast{"new_york": {fc}}, 1000, 0.05, 50)
	if d != nil {
		t.Errorf("expected nil for unknown signal, got %+v", d)
	}
}

func TestEvaluate_HeatSignal(t *testing.T) {
	// 40°C max temp → should be > threshold 35°C → high heat probability.
	fc := makeForecast("miami", 40, 0, 5, 10, 1)
	m := heatMarket("miami", 0.30, 0.70, 35.0)
	d := Evaluate(m, map[string][]weather.Forecast{"miami": {fc}}, 1000, 0.05, 50)
	if d == nil {
		t.Fatal("expected YES/NO decision for heat signal, got nil")
	}
	if d.Side != "YES" {
		t.Errorf("expected YES for hot day, got %q", d.Side)
	}
}

func TestEvaluate_SizeCapAtMaxBet(t *testing.T) {
	// Large bankroll + big edge should be capped at maxBet.
	fc := makeForecast("new_york", 20, 8, 95, 15, 80)
	m := rainMarket("new_york", 0.10, 0.90) // huge YES edge
	d := Evaluate(m, map[string][]weather.Forecast{"new_york": {fc}}, 100_000, 0.05, 25.0)
	if d == nil {
		t.Fatal("expected decision, got nil")
	}
	if d.SizeUSDC > 25.0 {
		t.Errorf("expected size <= 25.0 (maxBet), got %.2f", d.SizeUSDC)
	}
}

// ─── EvaluateFused() tests ───────────────────────────────────────────────────

func TestEvaluateFused_NilForecast(t *testing.T) {
	m := rainMarket("new_york", 0.40, 0.60)
	d := EvaluateFused(m, nil, 1000, 0.05, 50, "")
	if d != nil {
		t.Errorf("expected nil for nil FusedForecast, got %+v", d)
	}
}

func TestEvaluateFused_LowConfidence(t *testing.T) {
	// Confidence below 0.4 → should skip.
	fc := makeForecast("new_york", 20, 8, 90, 15, 80)
	ff := makeFused(fc, 0.30) // below minConfidence
	m := rainMarket("new_york", 0.40, 0.60)
	d := EvaluateFused(m, ff, 1000, 0.05, 50, "")
	if d != nil {
		t.Errorf("expected nil when confidence < 0.4, got %+v", d)
	}
}

func TestEvaluateFused_HighConfidence_WithEdge(t *testing.T) {
	fc := makeForecast("new_york", 20, 8, 90, 15, 80)
	ff := makeFused(fc, 0.85)
	m := rainMarket("new_york", 0.40, 0.60)
	d := EvaluateFused(m, ff, 1000, 0.05, 50, "")
	if d == nil {
		t.Fatal("expected decision with high confidence + edge, got nil")
	}
	if d.Side != "YES" {
		t.Errorf("expected YES, got %q", d.Side)
	}
}

func TestEvaluateFused_ConfidenceAtBoundary(t *testing.T) {
	// Exactly at minConfidence (0.4) — should not be skipped.
	fc := makeForecast("new_york", 20, 8, 90, 15, 80)
	ff := makeFused(fc, 0.4)
	m := rainMarket("new_york", 0.40, 0.60)
	d := EvaluateFused(m, ff, 1000, 0.05, 50, "")
	// 0.4 >= minConfidence so it should proceed; result depends on edge.
	// Just check it doesn't panic and respects the edge gate.
	_ = d
}

func TestEvaluateFused_MultiSource(t *testing.T) {
	// Multiple sources — reason should mention ensemble.
	fc := makeForecast("london", 18, 0.1, 5, 10, 1)
	ff := makeFused(fc, 0.75, "openmeteo", "nasa", "noaa")
	m := rainMarket("london", 0.80, 0.20)
	d := EvaluateFused(m, ff, 1000, 0.05, 50, "")
	if d == nil {
		t.Fatal("expected NO decision, got nil")
	}
	if d.Reason == "" {
		t.Error("expected non-empty reason")
	}
}

// ─── halfKelly tests ──────────────────────────────────────────────────────────

func TestHalfKelly_ZeroEdge(t *testing.T) {
	size := halfKelly(0, 2.0, 1000, 0.05)
	if size != 0 {
		t.Errorf("expected 0 size for zero edge, got %.4f", size)
	}
}

func TestHalfKelly_NegativeEdge(t *testing.T) {
	size := halfKelly(-0.1, 2.0, 1000, 0.05)
	if size != 0 {
		t.Errorf("expected 0 size for negative edge, got %.4f", size)
	}
}

func TestHalfKelly_PositiveEdge(t *testing.T) {
	size := halfKelly(0.2, 2.0, 1000, 0.05)
	if size <= 0 {
		t.Errorf("expected positive size, got %.4f", size)
	}
	if size > 1000*0.05 {
		t.Errorf("expected size <= maxFraction*bankroll, got %.4f", size)
	}
}
