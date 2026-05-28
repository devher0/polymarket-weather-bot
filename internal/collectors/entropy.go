// entropy.go — TASK-202: Signal entropy tracker.
//
// Measures how much weather data sources disagree with each other.
// High entropy means sources give conflicting forecasts → lower confidence.
// Low entropy means sources agree → higher confidence in our signal.
package collectors

import (
	"fmt"
	"strings"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// DisagreementReport summarises forecast disagreement across sources for one city.
type DisagreementReport struct {
	City         string
	TempEntropy  float64 // normalised stdev of max temps across sources (0..1)
	RainEntropy  float64 // normalised stdev of precip probabilities across sources (0..1)
	OverallScore float64 // mean of TempEntropy and RainEntropy
	Agreement    float64 // 1 - OverallScore (0=chaos, 1=perfect consensus)
	Label        string  // "consensus" | "moderate" | "disputed"
	SourceCount  int     // how many sources contributed
}

// ForecastDisagreement computes the disagreement report for a FusedForecast.
// Returns a zeroed report (Label="no data") when fewer than 2 sources are present.
func ForecastDisagreement(ff *FusedForecast) DisagreementReport {
	if ff == nil {
		return DisagreementReport{Label: "no data"}
	}

	rep := DisagreementReport{
		City:        ff.Forecast.City,
		SourceCount: len(ff.PerSourceForecasts),
	}

	if rep.SourceCount < 2 {
		rep.Label = "no data"
		rep.Agreement = 1.0
		return rep
	}

	// Collect per-source temperature and precipitation probability values.
	var temps, rainProbs []float64
	for _, f := range ff.PerSourceForecasts {
		temps = append(temps, f.MaxTempC)
		rainProbs = append(rainProbs, f.PrecipitationProbability)
	}

	// Normalise temperature stdev: divide by 10°C (a spread of 10°C = score 1.0).
	tempStd := stddev(temps)
	rep.TempEntropy = clamp01(tempStd / 10.0)

	// Normalise rain stdev: divide by 50% (a spread of 50pp = score 1.0).
	rainStd := stddev(rainProbs)
	rep.RainEntropy = clamp01(rainStd / 50.0)

	rep.OverallScore = (rep.TempEntropy + rep.RainEntropy) / 2.0
	rep.Agreement = 1.0 - rep.OverallScore

	switch {
	case rep.OverallScore < 0.15:
		rep.Label = "consensus"
	case rep.OverallScore < 0.35:
		rep.Label = "moderate"
	default:
		rep.Label = "disputed"
	}

	return rep
}

// LoadDisagreementReports computes DisagreementReport for all known cities
// using their day-0 cached forecasts. Cities with no cached data are skipped.
func LoadDisagreementReports(dataRoot string) []DisagreementReport {
	var reports []DisagreementReport
	for city := range weather.Cities {
		ff, ok := LoadForecastCache(city, 0, dataRoot, 0)
		if !ok || ff == nil {
			continue
		}
		reports = append(reports, ForecastDisagreement(ff))
	}

	// Sort by OverallScore descending (most disputed first).
	sortDisagreements(reports)
	return reports
}

// FormatDisagreementTable returns an ASCII table of disagreement reports
// suitable for terminal output.
func FormatDisagreementTable(reports []DisagreementReport) string {
	if len(reports) == 0 {
		return "No forecast data available for entropy analysis.\n"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-16s %7s %7s %7s %7s  %-10s  %s\n",
		"City", "TmpEnt", "RainEnt", "Overall", "Agree%", "Label", "Srcs"))
	sb.WriteString(strings.Repeat("─", 72) + "\n")

	for _, r := range reports {
		badge := agreeBadge(r.Agreement)
		sb.WriteString(fmt.Sprintf("%-16s %7.3f %7.3f %7.3f %7.1f  %-10s  %d  %s\n",
			r.City,
			r.TempEntropy,
			r.RainEntropy,
			r.OverallScore,
			r.Agreement*100,
			r.Label,
			r.SourceCount,
			badge,
		))
	}
	return sb.String()
}

// clamp01 clamps v to [0, 1].
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// agreeBadge returns a short visual indicator of agreement level.
func agreeBadge(agreement float64) string {
	switch {
	case agreement >= 0.85:
		return "✅ high"
	case agreement >= 0.65:
		return "🟡 ok"
	default:
		return "🔴 low"
	}
}

// sortDisagreements sorts reports by OverallScore descending (most disputed first).
func sortDisagreements(reports []DisagreementReport) {
	for i := 1; i < len(reports); i++ {
		for j := i; j > 0 && reports[j].OverallScore > reports[j-1].OverallScore; j-- {
			reports[j], reports[j-1] = reports[j-1], reports[j]
		}
	}
}
