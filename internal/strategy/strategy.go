// Package strategy evaluates edge and computes bet sizing.
package strategy

import (
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/collectors"
	"github.com/devher0/polymarket-weather-bot/internal/markets"
	"github.com/devher0/polymarket-weather-bot/internal/ratelimit"
	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// KellyFraction controls bet aggressiveness.
//
//	0.25 = quarter-Kelly (conservative)
//	0.50 = half-Kelly (default, industry standard)
//	1.00 = full-Kelly (aggressive)
//
// Set by cmd/bot at startup from config. Tests use the default 0.5.
var KellyFraction = 0.5

// MaxKellyFraction is a hard cap on the fraction of bankroll risked per bet
// regardless of the Kelly formula output (default: 5%).
// Set by cmd/bot at startup from config.
var MaxKellyFraction = 0.05

// ScoredMarket pairs a market with its pre-computed priority score
// and the fused forecast used to compute it.
type ScoredMarket struct {
	Market markets.Market
	FF     *collectors.FusedForecast
	Score  float64
}

// ScoreMarket returns a priority score for a market:
//
//	score = rough_edge × confidence × urgency_factor
//
// Higher score → evaluate and bet this market first.
// Markets with no forecast (ff == nil) score 0.
func ScoreMarket(m markets.Market, ff *collectors.FusedForecast) float64 {
	if ff == nil || m.City == "" || m.Signal == "" {
		return 0
	}

	// Rough probability estimate (before seasonal correction) to approximate edge.
	heatThreshold := 35.0
	if m.ThresholdC != 0 {
		heatThreshold = m.ThresholdC
	}
	var roughP float64
	switch m.Signal {
	case "heat":
		roughP = weather.HeatProbability(ff.Forecast, heatThreshold)
	case "cold":
		roughP = 1 - weather.HeatProbability(ff.Forecast, heatThreshold)
	case "rain":
		roughP = weather.RainProbability(ff.Forecast)
	case "sunny":
		roughP = weather.SunnyProbability(ff.Forecast)
	case "wind":
		roughP = math.Min(0.95, ff.Forecast.WindSpeedKMH/80.0)
	case "snow":
		roughP = (1 - weather.HeatProbability(ff.Forecast, 2.0)) * weather.RainProbability(ff.Forecast) * 0.8
	case "uv": // TASK-083
		uvThreshold := 8.0
		if m.ThresholdC != 0 {
			uvThreshold = m.ThresholdC
		}
		roughP = weather.UVProbability(ff.Forecast, uvThreshold)
	default:
		roughP = 0.5
	}

	yesEdge := math.Abs(roughP - m.YesPrice)
	noEdge := math.Abs((1 - roughP) - m.NoPrice)
	roughEdge := math.Max(yesEdge, noEdge)

	// Urgency factor: markets expiring sooner need to be evaluated first.
	days := m.DaysUntilExpiry()
	var urgency float64
	switch days {
	case 0, 1:
		urgency = 1.5
	case 2:
		urgency = 1.2
	case 3:
		urgency = 1.0
	case 4:
		urgency = 0.8
	default:
		urgency = 0.6
	}

	return roughEdge * ff.Confidence * urgency
}

// Decision is a bet recommendation.
type Decision struct {
	Market         markets.Market
	Side           string  // "YES" or "NO"
	TokenID        string
	OurProbability float64
	MarketPrice    float64
	Edge           float64
	SizeUSDC       float64
	Reason         string
}

// halfKelly returns the bet size using the fractional-Kelly formula.
// The actual fraction applied is min(maxFraction, k*KellyFraction).
// maxFraction is a hard cap; KellyFraction (package-level var) scales Kelly.
func halfKelly(edge, odds, bankroll, maxFraction float64) float64 {
	if edge <= 0 {
		return 0
	}
	b := odds - 1
	p := edge + 1/odds
	q := 1 - p
	k := (b*p - q) / b
	frac := math.Min(maxFraction, math.Max(0, k*KellyFraction))
	return frac * bankroll
}

// minConfidence is the threshold below which we skip the market.
const minConfidence = 0.4

// confidenceEdgeFactor returns a multiplier for minEdge based on forecast confidence.
// TASK-055: high-confidence forecasts tolerate a smaller edge (we trust the signal more);
// low-confidence forecasts require a bigger cushion (model disagreement = more risk).
//
//	confidence > 0.80 → factor 0.80  (accept 20% smaller edge)
//	confidence 0.50–0.80 → factor 1.00  (baseline)
//	confidence < 0.50 → factor 1.50  (require 50% larger edge)
func confidenceEdgeFactor(confidence float64) float64 {
	switch {
	case confidence > 0.80:
		return 0.80
	case confidence >= 0.50:
		return 1.00
	default:
		return 1.50
	}
}

// ensembleUncertaintyScale converts an ensemble temperature stddev (°C) into a
// bet-size multiplier. High uncertainty → smaller position.
//
//	0°C stddev → 1.00 (no scaling)
//	3°C stddev → 0.50
//	6°C+ stddev → 0.30 (floor)
//
// Returns 1.0 when uncertainty is 0 (ensemble unavailable).
func ensembleUncertaintyScale(uncertaintyC float64) float64 {
	if uncertaintyC <= 0 {
		return 1.0
	}
	scale := 1.0 - uncertaintyC/6.0
	if scale < 0.30 {
		return 0.30
	}
	if scale > 1.0 {
		return 1.0
	}
	return scale
}

// ComputeOurP returns the raw (pre-seasonal) probability estimate for a market
// signal given a weather forecast. Exported so prediction logging can call it
// even for markets that are ultimately skipped (confidence gate / no edge).
func ComputeOurP(m markets.Market, f weather.Forecast) float64 {
	heatThreshold := 35.0
	if m.ThresholdC != 0 {
		heatThreshold = m.ThresholdC
	}
	coldThreshold := 10.0
	if m.ThresholdC != 0 {
		coldThreshold = m.ThresholdC
	}
	var p float64
	switch m.Signal {
	case "rain":
		p = weather.RainProbability(f)
	case "heat":
		// TASK-084: use apparent temperature (heat index) when hot+humid.
		heatF := f
		if f.HumidityPct > 50 && f.ApparentMaxTempC > 0 {
			heatF.MaxTempC = f.ApparentMaxTempC
		}
		p = weather.HeatProbability(heatF, heatThreshold)
	case "cold":
		// TASK-084: apparent temperature may be colder than dry-bulb (wind chill).
		coldF := f
		if f.HumidityPct > 50 && f.ApparentMaxTempC > 0 {
			coldF.MaxTempC = f.ApparentMaxTempC
		}
		p = 1 - weather.HeatProbability(coldF, coldThreshold)
	case "snow":
		p = (1 - weather.HeatProbability(f, 2.0)) * weather.RainProbability(f) * 0.8
	case "wind":
		p = math.Min(0.95, f.WindSpeedKMH/80.0)
	case "sunny":
		p = weather.SunnyProbability(f)
	case "uv": // TASK-083
		uvThreshold := 8.0
		if m.ThresholdC != 0 {
			uvThreshold = m.ThresholdC
		}
		p = weather.UVProbability(f, uvThreshold)
	default:
		p = 0.5
	}
	// Apply seasonal Bayesian correction.
	return weather.AdjustForSeason(m.City, f.Date, p, m.Signal, m.ThresholdC)
}

// EvaluateFused evaluates a market using a FusedForecast from the aggregator.
// Returns nil when confidence is too low or there is no sufficient edge.
// dataRoot is the project data directory (used to record per-source predictions
// for accuracy tracking). Pass "" to skip recording.
func EvaluateFused(
	m markets.Market,
	ff *collectors.FusedForecast,
	bankroll float64,
	minEdge float64,
	maxBet float64,
	dataRoot string,
) *Decision {
	if ff == nil {
		return nil
	}

	// Pre-compute ourP for prediction logging (used even if we skip below).
	ourP := ComputeOurP(m, ff.Forecast)
	yesEdge := ourP - m.YesPrice
	noEdge := (1 - ourP) - m.NoPrice

	// Helper to build and save a prediction record.
	logPrediction := func(decision string, sizeUSDC float64, reason string) {
		SavePrediction(PredictionRecord{
			Timestamp:           time.Now().UTC().Format(time.RFC3339),
			ConditionID:         m.ConditionID,
			City:                m.City,
			Signal:              m.Signal,
			YesPrice:            m.YesPrice,
			NoPrice:             m.NoPrice,
			OurP:                ourP,
			YesEdge:             yesEdge,
			NoEdge:              noEdge,
			Confidence:          ff.Confidence,
			EnsembleUncertainty: ff.EnsembleUncertainty,
			AlertLevel:          ff.AlertLevel,
			Sources:             ff.Sources,
			MaxTempC:            ff.Forecast.MaxTempC,
			MinTempC:            ff.Forecast.MinTempC,
			PrecipMM:            ff.Forecast.PrecipitationMM,
			PrecipProb:          ff.Forecast.PrecipitationProbability,
			WindKPH:             ff.Forecast.WindSpeedKMH,
			Decision:            decision,
			SizeUSDC:            sizeUSDC,
			Reason:              reason,
		}, dataRoot)
	}

	// TASK-063: skip stale markets (no trades >24h + wide spread).
	if m.Stale {
		q := m.Question
		if len(q) > 60 {
			q = q[:60] + "…"
		}
		slog.Info("skipped: stale market (no trades >24h)",
			"conditionID", m.ConditionID,
			"question", q,
		)
		logPrediction("SKIP:stale", 0, "stale market: no trades >24h")
		return nil
	}

	// Confidence gate: skip if sources disagree too much
	if ff.Confidence < minConfidence {
		logPrediction("SKIP:confidence", 0, fmt.Sprintf("confidence=%.2f < %.2f", ff.Confidence, minConfidence))
		return nil
	}

	// Build per-source contribution note for the Decision reason.
	// Weights are normalised to available sources; show each as percentage.
	staticWeights := map[string]float64{
		"openmeteo": 0.35, "nasa": 0.30, "noaa": 0.25, "goes": 0.10,
	}
	totalW := 0.0
	for _, s := range ff.Sources {
		totalW += staticWeights[s]
	}
	if totalW == 0 {
		totalW = 1
	}
	parts := make([]string, 0, len(ff.Sources))
	for _, s := range ff.Sources {
		pct := staticWeights[s] / totalW * 100
		parts = append(parts, fmt.Sprintf("%s(%.0f%%)", s, pct))
	}
	sourceNote := fmt.Sprintf("ensemble=[%s] confidence=%.2f",
		strings.Join(parts, "+"), ff.Confidence)

	// TASK-050: apply NWS alert probability boost before sizing.
	// When an active weather warning/watch is relevant to the market signal,
	// we boost OurProbability before the edge check so the alert acts as
	// additional evidence alongside the weather model forecasts.
	alertForecast := ff.Forecast
	if ff.AlertLevel > collectors.AlertLevelNone {
		probBoost, confBoost := collectors.AlertBoost(
			collectors.AlertSummary{Level: ff.AlertLevel, Events: ff.AlertEvents},
			m.Signal,
		)
		if probBoost > 0 {
			boostedP := math.Min(0.97, alertForecast.PrecipitationProbability/100+probBoost) * 100
			switch m.Signal {
			case "rain", "flood":
				alertForecast.PrecipitationProbability = boostedP
				if alertForecast.PrecipitationMM < 2 {
					alertForecast.PrecipitationMM = 2.1 // ensure RainProbability uses mid path
				}
			case "heat":
				alertForecast.MaxTempC += probBoost * 15 // ~+2.25°C for warning (+15%)
			case "cold", "snow":
				alertForecast.MaxTempC -= probBoost * 15
				alertForecast.MinTempC -= probBoost * 15
			case "wind":
				alertForecast.WindSpeedKMH += probBoost * 40 // ~+6 km/h for warning
			}
			_ = confBoost // confidence boost applied below after decision is made
			slog.Info("alert boost applied",
				"city", m.City,
				"signal", m.Signal,
				"alert_level", ff.AlertLevel,
				"events", strings.Join(ff.AlertEvents, "; "),
				"prob_boost", fmt.Sprintf("+%.0f%%", probBoost*100),
			)
			sourceNote += fmt.Sprintf(" alert_boost=+%.0f%%(%s)",
				probBoost*100, levelName(ff.AlertLevel))
		}
	}

	// TASK-087: apply RAOB upper-air wind boost for wind markets.
	// When 850 hPa wind exceeds 50 km/h the surface wind probability is boosted
	// proportionally (up to +0.20) — upper-level momentum often translates to
	// surface gusts, especially in frontal and cyclonic situations.
	if m.Signal == "wind" {
		raobProfile := collectors.GetAtmosphericProfile(m.City)
		boost := raobProfile.WindBoost()
		if boost > 0 {
			alertForecast.WindSpeedKMH += boost * 80.0 // scale to km/h space used by wind probability
			slog.Info("raob wind boost applied",
				"city", m.City,
				"wind_850hpa_kmh", fmt.Sprintf("%.1f", raobProfile.WindKMH850hPa),
				"wind_700hpa_kmh", fmt.Sprintf("%.1f", raobProfile.WindKMH700hPa),
				"max_wind_shear_kmh", fmt.Sprintf("%.1f", raobProfile.MaxWindShear),
				"boost_fraction", fmt.Sprintf("+%.3f", boost),
			)
			sourceNote += fmt.Sprintf(" raob_boost=+%.0f%%", boost*100)
		}
	}

	// TASK-055: adjust minEdge based on forecast confidence.
	// High-confidence forecasts (sources agree) can enter with smaller edge;
	// low-confidence forecasts require a larger edge as safety margin.
	edgeFactor := confidenceEdgeFactor(ff.Confidence)
	adjustedMinEdge := minEdge * edgeFactor
	if edgeFactor != 1.0 {
		slog.Debug("min_edge adjusted",
			"base", fmt.Sprintf("%.3f", minEdge),
			"factor", fmt.Sprintf("%.2f", edgeFactor),
			"adjusted", fmt.Sprintf("%.3f", adjustedMinEdge),
			"confidence", fmt.Sprintf("%.2f", ff.Confidence),
		)
	}

	d := evaluate(m, alertForecast, bankroll, adjustedMinEdge, maxBet, sourceNote)
	if d == nil {
		logPrediction("SKIP:no_edge", 0, fmt.Sprintf("yes_edge=%+.3f no_edge=%+.3f adj_min=%.3f", yesEdge, noEdge, adjustedMinEdge))
		return nil
	}

	// Apply confidence boost from alert (capped at 0.97).
	if ff.AlertLevel > collectors.AlertLevelNone {
		_, confBoost := collectors.AlertBoost(
			collectors.AlertSummary{Level: ff.AlertLevel, Events: ff.AlertEvents},
			m.Signal,
		)
		if confBoost > 0 {
			ff.Confidence = math.Min(0.97, ff.Confidence+confBoost)
		}
	}

	// TASK-034: scale bet size down when ensemble spread is high.
	// High temperature stddev across 16 ensemble members signals low predictability.
	if ff.EnsembleUncertainty > 0 {
		scale := ensembleUncertaintyScale(ff.EnsembleUncertainty)
		if scale < 1.0 {
			original := d.SizeUSDC
			d.SizeUSDC = math.Round(d.SizeUSDC*scale*100) / 100
			d.Reason += fmt.Sprintf(" ensemble_scale=%.2f(unc=%.1f°C,%.2f→%.2f)",
				scale, ff.EnsembleUncertainty, original, d.SizeUSDC)
			slog.Info("ensemble scaling applied",
				"conditionID", m.ConditionID,
				"uncertainty_c", fmt.Sprintf("%.1f", ff.EnsembleUncertainty),
				"scale", fmt.Sprintf("%.2f", scale),
				"size_before", fmt.Sprintf("%.2f", original),
				"size_after", fmt.Sprintf("%.2f", d.SizeUSDC),
			)
			// Re-check minimum size after scaling.
			if d.SizeUSDC < 0.5 {
				slog.Info("skipped: size below minimum after ensemble scaling",
					"conditionID", m.ConditionID)
				logPrediction("SKIP:min_size", 0, fmt.Sprintf("size_after_scale=%.2f unc=%.1f°C", d.SizeUSDC, ff.EnsembleUncertainty))
				return nil
			}
		}
	}

	// TASK-032: record per-source probability predictions so accuracy can
	// be computed after the market resolves. We compute each source's estimate
	// of the market signal using the same logic as evaluate().
	if len(ff.PerSourceForecasts) > 0 {
		perSourceProbs := computePerSourceProbs(m, ff.PerSourceForecasts)
		if len(perSourceProbs) > 0 {
			if err := collectors.RecordSourcePredictions(m.ConditionID, perSourceProbs, dataRoot); err != nil {
				slog.Warn("record source predictions failed", "err", err)
			}
		}
	}

	// TASK-057: log successful bet to prediction journal.
	logPrediction("BET_"+d.Side, d.SizeUSDC, d.Reason)

	return d
}

// Evaluate compares our weather forecast to a Polymarket price.
// Returns a Decision when edge ≥ minEdge, otherwise nil.
// Deprecated: prefer EvaluateFused when aggregator data is available.
func Evaluate(
	m markets.Market,
	forecasts map[string][]weather.Forecast,
	bankroll float64,
	minEdge float64,
	maxBet float64,
) *Decision {
	if m.City == "" {
		return nil
	}
	flist, ok := forecasts[m.City]
	if !ok || len(flist) == 0 {
		return nil
	}
	return evaluate(m, flist[0], bankroll, minEdge, maxBet, "source=openmeteo")
}

// evaluate is the core logic shared by Evaluate and EvaluateFused.
func evaluate(
	m markets.Market,
	f weather.Forecast,
	bankroll float64,
	minEdge float64,
	maxBet float64,
	sourceNote string,
) *Decision {
	if m.City == "" {
		return nil
	}

	// Use parsed temperature threshold from market question when available;
	// fall back to sensible defaults.
	heatThreshold := 35.0
	if m.ThresholdC != 0 {
		heatThreshold = m.ThresholdC
	}
	coldThreshold := 10.0
	if m.ThresholdC != 0 {
		coldThreshold = m.ThresholdC
	}

	var ourP float64
	switch m.Signal {
	case "rain":
		ourP = weather.RainProbability(f)
	case "heat":
		// TASK-084: use apparent temperature (heat index) when hot+humid.
		heatF := f
		if f.HumidityPct > 50 && f.ApparentMaxTempC > 0 {
			heatF.MaxTempC = f.ApparentMaxTempC
		}
		ourP = weather.HeatProbability(heatF, heatThreshold)
	case "cold":
		// TASK-084: apparent temperature may be colder than dry-bulb (wind chill).
		coldF := f
		if f.HumidityPct > 50 && f.ApparentMaxTempC > 0 {
			coldF.MaxTempC = f.ApparentMaxTempC
		}
		ourP = 1 - weather.HeatProbability(coldF, coldThreshold)
	case "snow":
		coldP := 1 - weather.HeatProbability(f, 2.0)
		ourP = coldP * weather.RainProbability(f) * 0.8
	case "wind":
		ourP = math.Min(0.95, f.WindSpeedKMH/80.0)
	case "sunny":
		ourP = weather.SunnyProbability(f)
	default:
		return nil
	}

	// Apply seasonal Bayesian correction: blend raw model probability with
	// monthly climatological prior. Improves calibration, especially for
	// distant forecasts (day 4-6) where NWP skill degrades.
	rawP := ourP
	ourP = weather.AdjustForSeason(m.City, f.Date, rawP, m.Signal, m.ThresholdC)
	if ourP != rawP {
		sourceNote += fmt.Sprintf(" seasonal(raw=%.2f→%.2f)", rawP, ourP)
	}

	yesEdge := ourP - m.YesPrice
	noEdge := (1 - ourP) - m.NoPrice

	// Determine winning side and compute half-Kelly size.
	type candidate struct {
		side        string
		tokenID     string
		marketPrice float64
		edge        float64
		odds        float64
	}
	var best candidate
	if yesEdge >= noEdge && math.Abs(yesEdge) >= minEdge {
		best = candidate{"YES", m.YesTokenID, m.YesPrice, yesEdge, 1 / m.YesPrice}
	} else if noEdge > yesEdge && math.Abs(noEdge) >= minEdge {
		best = candidate{"NO", m.NoTokenID, m.NoPrice, noEdge, 1 / m.NoPrice}
	} else {
		return nil
	}

	size := halfKelly(best.edge, best.odds, bankroll, MaxKellyFraction)
	size = math.Min(size, maxBet)
	// Apply size fuzzing (±3–7%) to avoid mechanical round-number detection.
	size = ratelimit.FuzzSize(size)

	// Liquidity gate: skip thin markets when expected position size < $50 USDC
	// to avoid moving the price on illiquid books.
	if m.ThinLiquidity && size < 50 {
		slog.Info("skipped: thin liquidity",
			"conditionID", m.ConditionID,
			"spread", fmt.Sprintf("%.3f", m.Spread),
			"est_size", fmt.Sprintf("%.2f", size),
		)
		return nil
	}

	if size < 0.5 {
		return nil
	}

	return &Decision{
		Market:         m,
		Side:           best.side,
		TokenID:        best.tokenID,
		OurProbability: ourP,
		MarketPrice:    best.marketPrice,
		Edge:           best.edge,
		SizeUSDC:       size,
		Reason: fmt.Sprintf("%s/%s: our=%.2f mkt=%.2f edge=%+.2f [%s]",
			m.City, m.Signal, ourP, best.marketPrice, best.edge, sourceNote),
	}
}

// levelName returns a human-readable name for an AlertLevel constant.
func levelName(level int) string {
	switch level {
	case collectors.AlertLevelAdvisory:
		return "advisory"
	case collectors.AlertLevelWatch:
		return "watch"
	case collectors.AlertLevelWarning:
		return "warning"
	default:
		return "none"
	}
}

// computePerSourceProbs computes each source's raw probability estimate for
// the market signal. This is called after EvaluateFused decides to record a bet
// so we can later compare against the actual outcome per source.
func computePerSourceProbs(m markets.Market, perSource map[string]weather.Forecast) map[string]float64 {
	if len(perSource) == 0 {
		return nil
	}

	heatThreshold := 35.0
	if m.ThresholdC != 0 {
		heatThreshold = m.ThresholdC
	}

	result := make(map[string]float64, len(perSource))
	for src, f := range perSource {
		var p float64
		switch m.Signal {
		case "rain":
			p = weather.RainProbability(f)
		case "heat":
			p = weather.HeatProbability(f, heatThreshold)
		case "cold":
			p = 1 - weather.HeatProbability(f, heatThreshold)
		case "snow":
			p = (1 - weather.HeatProbability(f, 2.0)) * weather.RainProbability(f) * 0.8
		case "wind":
			p = math.Min(0.95, f.WindSpeedKMH/80.0)
		case "sunny":
			p = weather.SunnyProbability(f)
		default:
			continue
		}
		result[src] = p
	}
	return result
}
