// cme_degree_days.go — CME-style Heating/Cooling Degree Day indices.
//
// TASK-093: The Chicago Mercantile Exchange (CME) HDD/CDD indices are the
// industry standard for weather derivatives pricing. Professional weather
// traders at hedge funds and energy companies use these indices to price
// temperature-linked contracts — the same way we price Polymarket weather
// bets.
//
// Instead of scraping the CME website (fragile, rate-limited) we compute
// HDD/CDD directly from our fused forecast using the same CME definitions:
//
//	HDD = max(0, 65°F − avg_temp) = max(0, 18.33°C − avg_temp)
//	CDD = max(0, avg_temp − 65°F) = max(0, avg_temp − 18.33°C)
//
// where avg_temp = (MaxTempC + MinTempC) / 2.
//
// These values are used for Polymarket markets phrased as
// "average temperature above/below X degrees" — converting the threshold to
// a cumulative degree-day deficit gives a better edge estimate than a raw
// temperature comparison.
package collectors

import (
	"math"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

const (
	// CMEBaselineTempC is 65°F expressed in Celsius — the CME settlement basis.
	CMEBaselineTempC = 18.333
)

// DegreeDays holds HDD and CDD values for one forecast day.
type DegreeDays struct {
	// HDD is heating degree days: energy needed to heat a building.
	// Positive in winter (avg_temp < 18.33°C).
	HDD float64
	// CDD is cooling degree days: energy needed to cool a building.
	// Positive in summer (avg_temp > 18.33°C).
	CDD float64
	// AvgTempC is (MaxTempC + MinTempC) / 2 used in the calculation.
	AvgTempC float64
}

// ComputeDegreeDays computes CME HDD/CDD for a single forecast day.
// Uses the midpoint of max/min temperature as the daily average.
func ComputeDegreeDays(f weather.Forecast) DegreeDays {
	avg := (f.MaxTempC + f.MinTempC) / 2.0
	return DegreeDays{
		HDD:      math.Max(0, CMEBaselineTempC-avg),
		CDD:      math.Max(0, avg-CMEBaselineTempC),
		AvgTempC: avg,
	}
}

// ComputeAccumulatedDegreeDays sums HDD/CDD across multiple forecast days.
// Useful for weekly or monthly degree-day markets.
func ComputeAccumulatedDegreeDays(forecasts []weather.Forecast) (totalHDD, totalCDD float64) {
	for _, f := range forecasts {
		dd := ComputeDegreeDays(f)
		totalHDD += dd.HDD
		totalCDD += dd.CDD
	}
	return
}

// HeatProbabilityFromHDD returns a 0–1 probability that the day will be
// "hot" (above-normal temperature) based on CDD value.
//
// This is complementary to weather.HeatProbability which uses raw °C;
// HDD/CDD framing matches CME-style market definitions better.
//
// threshold is the CDD threshold from the market question (default 5 = ~23°C avg).
func HeatProbabilityFromCDD(f weather.Forecast, threshold float64) float64 {
	if threshold <= 0 {
		threshold = 5.0
	}
	dd := ComputeDegreeDays(f)
	diff := dd.CDD - threshold
	switch {
	case diff >= 3:
		return 0.93
	case diff >= 0:
		return 0.65 + diff/3.0*0.28
	case diff >= -3:
		return 0.35 + (diff+3)/3.0*0.30
	default:
		return 0.10
	}
}

// ColdProbabilityFromHDD returns a 0–1 probability that the day will be
// "cold" (below-normal temperature) based on HDD value.
//
// threshold is the HDD threshold from the market question (default 5 = ~13°C avg).
func ColdProbabilityFromHDD(f weather.Forecast, threshold float64) float64 {
	if threshold <= 0 {
		threshold = 5.0
	}
	dd := ComputeDegreeDays(f)
	diff := dd.HDD - threshold
	switch {
	case diff >= 3:
		return 0.93
	case diff >= 0:
		return 0.65 + diff/3.0*0.28
	case diff >= -3:
		return 0.35 + (diff+3)/3.0*0.30
	default:
		return 0.10
	}
}
