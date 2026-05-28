// cross_day.go — cross-day forecast consistency checker (TASK-185).
//
// Meteorological insight: when a weather pattern (e.g. rain, heat) persists
// across multiple forecast days, models tend to agree more reliably than for
// single-day events. This module loads cached forecasts for a configurable
// window of days and quantifies how consistently the signal fires across them,
// then returns a confidence boost that the caller can apply.
package collectors

import (
	"fmt"
	"log/slog"
	"math"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// CrossDayResult summarises the cross-day consistency check for a city+signal.
type CrossDayResult struct {
	// City and Signal identify what was checked.
	City   string
	Signal string

	// DaysChecked is how many forecast days were available (≤ lookAheadDays).
	DaysChecked int

	// DaysConsistent is how many of those days had the signal probability on
	// the same side as the target day (all above OR all below 0.5).
	DaysConsistent int

	// AgreementFraction = DaysConsistent / DaysChecked (1.0 = full agreement).
	AgreementFraction float64

	// ConfidenceBoost is the suggested additive boost to apply:
	//   AgreementFraction ≥ 1.0          → +0.08
	//   AgreementFraction ≥ 0.67 (<1.0)  → +0.04
	//   otherwise                         →  0.00
	ConfidenceBoost float64
}

// crossDayLookAhead is the number of extra forecast days to inspect beyond
// the target day. Window of 3 means: target + 2 following days (capped at d+6).
const crossDayLookAhead = 3

// signalProbFromForecast extracts the raw signal probability for the named
// signal from a weather.Forecast. threshold is only used for "heat"/"humid"
// (pass 0 to use the 35 °C default). Returns (p, true) or (0, false).
func signalProbFromForecast(f weather.Forecast, signal string, threshold float64) (float64, bool) {
	if threshold == 0 {
		threshold = 35.0
	}
	switch signal {
	case "heat":
		return weather.HeatProbability(f, threshold), true
	case "cold":
		return 1.0 - weather.HeatProbability(f, threshold), true
	case "rain":
		return weather.RainProbability(f), true
	case "sunny":
		return weather.SunnyProbability(f), true
	case "snow":
		return weather.SnowProbability(f), true
	case "wind":
		return math.Min(0.95, f.WindSpeedKMH/80.0), true
	case "fog":
		return weather.FogProbability(f), true
	case "humid":
		return weather.HumidProbability(f, threshold), true
	case "dry":
		return weather.DryProbability(f), true
	}
	return 0, false
}

// CheckCrossDay checks cross-day forecast consistency for a given city and
// signal starting from targetDayOffset. It reads only the disk cache (never
// triggers network calls) and returns a CrossDayResult with the agreement
// fraction and a suggested confidence boost.
//
// threshold is used only for "heat" and "humid" signals (0 = use 35 °C default).
// dataRoot is the bot's data directory root.
func CheckCrossDay(city, signal string, targetDayOffset int, threshold float64, dataRoot string) *CrossDayResult {
	res := &CrossDayResult{City: city, Signal: signal}

	// Determine the reference direction from the target day.
	targetFF, ok := LoadForecastCache(city, targetDayOffset, dataRoot, 0)
	if !ok || targetFF == nil {
		return res
	}
	targetP, known := signalProbFromForecast(targetFF.Forecast, signal, threshold)
	if !known {
		return res
	}
	targetBull := targetP >= 0.5 // true = "signal fires"

	res.DaysChecked = 1
	res.DaysConsistent = 1 // target day always agrees with itself

	for offset := targetDayOffset + 1; offset < targetDayOffset+crossDayLookAhead && offset <= 6; offset++ {
		ff, ok := LoadForecastCache(city, offset, dataRoot, 0)
		if !ok || ff == nil {
			continue
		}
		p, known := signalProbFromForecast(ff.Forecast, signal, threshold)
		if !known {
			continue
		}
		res.DaysChecked++
		if (p >= 0.5) == targetBull {
			res.DaysConsistent++
		}
	}

	if res.DaysChecked <= 1 {
		return res // no neighbours available — no information
	}

	res.AgreementFraction = float64(res.DaysConsistent) / float64(res.DaysChecked)
	// Thresholds: 1.0 = all days agree; 2/3 ≈ 0.6666 = majority agree.
	// Use slightly relaxed lower bound to avoid float64 precision issues.
	switch {
	case res.AgreementFraction >= 1.0:
		res.ConfidenceBoost = 0.08
	case res.AgreementFraction >= 2.0/3.0-1e-9:
		res.ConfidenceBoost = 0.04
	default:
		res.ConfidenceBoost = 0.00
	}

	slog.Debug("cross-day consistency checked",
		"city", city,
		"signal", signal,
		"target_day", targetDayOffset,
		"days_checked", res.DaysChecked,
		"days_consistent", res.DaysConsistent,
		"agreement", fmt.Sprintf("%.2f", res.AgreementFraction),
		"boost", res.ConfidenceBoost,
	)

	return res
}

// ApplyCrossDay applies the confidence boost from res to ff in-place,
// capping Confidence at 0.97 and recording CrossDayScore + "cross_day" source.
// It is a no-op when res is nil or the boost is zero.
func ApplyCrossDay(ff *FusedForecast, res *CrossDayResult) {
	if res == nil || res.ConfidenceBoost <= 0 || ff == nil {
		return
	}
	before := ff.Confidence
	ff.Confidence = math.Min(0.97, ff.Confidence+res.ConfidenceBoost)
	ff.CrossDayScore = res.AgreementFraction
	ff.Sources = append(ff.Sources, "cross_day")
	slog.Info("cross-day confidence boost applied",
		"city", res.City,
		"signal", res.Signal,
		"agreement", fmt.Sprintf("%.2f", res.AgreementFraction),
		"confidence_before", fmt.Sprintf("%.3f", before),
		"confidence_after", fmt.Sprintf("%.3f", ff.Confidence),
	)
}
