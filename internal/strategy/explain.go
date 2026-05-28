// explain.go — ExplainEvaluate runs the full strategy pipeline and captures
// every intermediate value, so the caller can display a human-readable audit
// trail of why each market was bet or skipped.
package strategy

import (
	"fmt"
	"math"
	"strings"

	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// ExplainResult holds a complete audit trail of EvaluateFused for one market.
// All numeric fields are zero-valued when evaluation stopped at an earlier gate.
type ExplainResult struct {
	Market markets.Market

	// Forecast metadata
	Confidence     float64
	ConsensusScore float64  // TASK-130: multi-dim inter-source agreement [0,1]
	Sources        []string // from ff.Sources
	EnsUnc         float64  // ensemble temperature stddev (°C); 0 = unavailable

	// Probability pipeline
	RawP    float64 // raw signal probability before seasonal adjustment
	SeasonP float64 // after seasonal Bayesian correction
	FinalP  float64 // = SeasonP (canonical probability used for edge calc)

	// Edge components
	YesEdge float64 // FinalP - YesPrice
	NoEdge  float64 // (1-FinalP) - NoPrice

	// Bet sizing
	BestSide  string  // "YES", "NO", or "" when skipped
	BestEdge  float64 // edge of the chosen side
	KellyRaw  float64 // half-Kelly size before ensemble scaling
	EnsScale  float64 // ensemble multiplier [0.30, 1.0]; 1.0 when no ensemble
	FinalSize float64 // final bet size in USDC

	// Outcome
	SkipReason string // human-readable reason when Action starts with "SKIP"
	Action     string // "BET YES $8.42" or "SKIP: <reason>"
}

// IsBet returns true when the evaluation ended in a bet recommendation.
func (r *ExplainResult) IsBet() bool {
	return strings.HasPrefix(r.Action, "BET")
}

// ExplainEvaluate mirrors EvaluateFused step-by-step and records every
// decision point into an ExplainResult.  It does NOT record source
// predictions (side-effect of EvaluateFused); call EvaluateFused separately
// when you need that.
//
// Parameters match EvaluateFused:
//   - ff: fused forecast (nil → instant skip with "no forecast")
//   - bankroll, minEdge, maxBet: same semantics as EvaluateFused
func ExplainEvaluate(
	m markets.Market,
	ff *collectors.FusedForecast,
	bankroll, minEdge, maxBet float64,
) *ExplainResult {
	r := &ExplainResult{Market: m, EnsScale: 1.0}

	// ── Gate 0: forecast availability ──────────────────────────────────────
	if ff == nil {
		r.skip("no forecast available")
		return r
	}

	r.Confidence = ff.Confidence
	r.ConsensusScore = ff.ConsensusScore
	r.Sources = ff.Sources
	r.EnsUnc = ff.EnsembleUncertainty

	// ── Gate 1: confidence ─────────────────────────────────────────────────
	if ff.Confidence < minConfidence {
		r.skip(fmt.Sprintf("low confidence (%.2f < %.2f)", ff.Confidence, minConfidence))
		return r
	}

	// ── Gate 2: signal → rawP ──────────────────────────────────────────────
	heatThreshold := 35.0
	if m.ThresholdC != 0 {
		heatThreshold = m.ThresholdC
	}

	var rawP float64
	switch m.Signal {
	case "rain":
		rawP = weather.RainProbability(ff.Forecast)
	case "heat":
		rawP = weather.HeatProbability(ff.Forecast, heatThreshold)
	case "cold":
		rawP = 1 - weather.HeatProbability(ff.Forecast, heatThreshold)
	case "snow":
		rawP = (1 - weather.HeatProbability(ff.Forecast, 2.0)) *
			weather.RainProbability(ff.Forecast) * 0.8
	case "wind":
		rawP = math.Min(0.95, ff.Forecast.WindSpeedKMH/80.0)
	case "sunny":
		rawP = weather.SunnyProbability(ff.Forecast)
	default:
		r.skip(fmt.Sprintf("unknown signal %q", m.Signal))
		return r
	}
	r.RawP = rawP

	// ── Seasonal Bayesian adjustment ───────────────────────────────────────
	seasonP := weather.AdjustForSeason(m.City, ff.Forecast.Date, rawP, m.Signal, m.ThresholdC)
	r.SeasonP = seasonP
	r.FinalP = seasonP

	// ── Gate 3: edge ───────────────────────────────────────────────────────
	yesEdge := seasonP - m.YesPrice
	noEdge := (1 - seasonP) - m.NoPrice
	r.YesEdge = yesEdge
	r.NoEdge = noEdge

	var bestSide string
	var bestEdge float64
	var bestPrice float64
	if yesEdge >= noEdge && yesEdge >= minEdge {
		bestSide, bestEdge, bestPrice = "YES", yesEdge, m.YesPrice
	} else if noEdge > yesEdge && noEdge >= minEdge {
		bestSide, bestEdge, bestPrice = "NO", noEdge, m.NoPrice
	} else {
		top := math.Max(yesEdge, noEdge)
		r.skip(fmt.Sprintf("no edge (best=%+.3f < %.2f)", top, minEdge))
		return r
	}
	r.BestSide = bestSide
	r.BestEdge = bestEdge

	// ── Kelly sizing ───────────────────────────────────────────────────────
	odds := 1.0 / bestPrice
	kelly := halfKelly(bestEdge, odds, bankroll, 0.05)
	kelly = math.Min(kelly, maxBet)
	r.KellyRaw = kelly

	// ── Ensemble uncertainty scaling ───────────────────────────────────────
	scale := ensembleUncertaintyScale(ff.EnsembleUncertainty)
	r.EnsScale = scale
	finalSize := math.Round(kelly*scale*100) / 100
	r.FinalSize = finalSize

	// ── Gate 4: minimum size ───────────────────────────────────────────────
	if finalSize < 0.5 {
		r.skip(fmt.Sprintf("size too small after ensemble scaling ($%.2f)", finalSize))
		return r
	}

	r.Action = fmt.Sprintf("BET %s $%.2f", bestSide, finalSize)
	return r
}

func (r *ExplainResult) skip(reason string) {
	r.SkipReason = reason
	r.Action = "SKIP: " + reason
}
