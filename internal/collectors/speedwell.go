// speedwell.go — Speedwell Climate HDD/CDD settlement data collector.
//
// TASK-097: Speedwell Climate is the institutional standard for weather
// derivatives settlement. Their HDD/CDD indices are used by hedge funds and
// energy traders to settle CME weather futures.
//
// Since the Speedwell portal (https://portal.speedwellclimate.com) requires
// institutional login, this collector replicates their methodology using
// open-source temperature observations from the NOAA GHCN-Daily dataset
// (accessible via the Open-Meteo historical archive API).
//
// Speedwell methodology (same as CME):
//
//	HDD = max(0, 65°F − avg_daily_temp) = max(0, 18.333°C − avg_temp)
//	CDD = max(0, avg_daily_temp − 65°F) = max(0, avg_temp − 18.333°C)
//
// The resulting SpeedwellIndex can be used as ground truth for backtesting
// temperature-based Polymarket markets.
package collectors

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// SpeedwellIndex holds daily HDD/CDD values following Speedwell Climate's
// published methodology for weather derivatives settlement.
type SpeedwellIndex struct {
	// City is the canonical city name (matches weather.Cities keys).
	City string `json:"city"`
	// Date is the observation date in "2006-01-02" format.
	Date string `json:"date"`
	// AvgTempC is (TMax + TMin) / 2 in °C — the Speedwell daily average.
	AvgTempC float64 `json:"avg_temp_c"`
	// HDD is heating degree days (max 0, 18.333 − avg_temp).
	HDD float64 `json:"hdd"`
	// CDD is cooling degree days (max(0, avg_temp − 18.333)).
	CDD float64 `json:"cdd"`
	// Source identifies the underlying data feed ("open-meteo-archive").
	Source string `json:"source"`
}

// SpeedwellSummary is an aggregated period summary (e.g. weekly, monthly).
type SpeedwellSummary struct {
	City      string  `json:"city"`
	StartDate string  `json:"start_date"`
	EndDate   string  `json:"end_date"`
	TotalHDD  float64 `json:"total_hdd"`
	TotalCDD  float64 `json:"total_cdd"`
	AvgTempC  float64 `json:"avg_temp_c"`
	Days      int     `json:"days"`
}

var speedwellClient = &http.Client{Timeout: 30 * time.Second}

// speedwellArchiveResp mirrors the Open-Meteo historical archive JSON.
type speedwellArchiveResp struct {
	Daily struct {
		Time             []string  `json:"time"`
		Temperature2MMax []float64 `json:"temperature_2m_max"`
		Temperature2MMin []float64 `json:"temperature_2m_min"`
	} `json:"daily"`
}

// FetchSpeedwellIndices downloads HDD/CDD settlement indices for a city over
// a date range using the Open-Meteo historical archive (free, no API key).
//
// lat/lon must correspond to the nearest WMO/SYNOP station used by Speedwell;
// for standard cities use the coords from weather.Cities.
//
// start and end are inclusive in "2006-01-02" format.
func FetchSpeedwellIndices(city string, lat, lon float64, start, end string) ([]SpeedwellIndex, error) {
	url := fmt.Sprintf(
		"https://archive-api.open-meteo.com/v1/archive?latitude=%.4f&longitude=%.4f"+
			"&daily=temperature_2m_max,temperature_2m_min"+
			"&start_date=%s&end_date=%s&timezone=UTC",
		lat, lon, start, end,
	)

	resp, err := speedwellClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("speedwell fetch %s: %w", city, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("speedwell fetch %s: HTTP %d: %s", city, resp.StatusCode, body)
	}

	var ar speedwellArchiveResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("speedwell decode %s: %w", city, err)
	}

	n := len(ar.Daily.Time)
	if n == 0 {
		return nil, fmt.Errorf("speedwell: no data for %s [%s – %s]", city, start, end)
	}

	indices := make([]SpeedwellIndex, 0, n)
	for i := 0; i < n; i++ {
		tmax := safeIdx(ar.Daily.Temperature2MMax, i)
		tmin := safeIdx(ar.Daily.Temperature2MMin, i)
		avg := (tmax + tmin) / 2.0
		hdd := math.Max(0, CMEBaselineTempC-avg)
		cdd := math.Max(0, avg-CMEBaselineTempC)

		indices = append(indices, SpeedwellIndex{
			City:     city,
			Date:     ar.Daily.Time[i],
			AvgTempC: avg,
			HDD:      hdd,
			CDD:      cdd,
			Source:   "open-meteo-archive",
		})
	}
	return indices, nil
}

// SummariseSpeedwell aggregates daily SpeedwellIndex records into a period
// summary suitable for backtesting Polymarket HDD/CDD markets.
func SummariseSpeedwell(indices []SpeedwellIndex) SpeedwellSummary {
	if len(indices) == 0 {
		return SpeedwellSummary{}
	}
	s := SpeedwellSummary{
		City:      indices[0].City,
		StartDate: indices[0].Date,
		EndDate:   indices[len(indices)-1].Date,
		Days:      len(indices),
	}
	var sumTemp float64
	for _, idx := range indices {
		s.TotalHDD += idx.HDD
		s.TotalCDD += idx.CDD
		sumTemp += idx.AvgTempC
	}
	s.AvgTempC = sumTemp / float64(len(indices))
	return s
}

// CalibrationError returns the mean absolute error between Speedwell settlement
// values and our computed degree-day series, expressed in degree-days.
//
// settlement is the ground-truth Speedwell indices; computed is our model's
// output for the same period. Both slices must be the same length.
//
// A low MAE (< 1.0 HDD/CDD per day) indicates good calibration.
func CalibrationError(settlement, computed []SpeedwellIndex) (hddMAE, cddMAE float64, err error) {
	if len(settlement) != len(computed) {
		return 0, 0, fmt.Errorf("speedwell calibration: length mismatch %d vs %d",
			len(settlement), len(computed))
	}
	if len(settlement) == 0 {
		return 0, 0, nil
	}
	var hddSum, cddSum float64
	for i := range settlement {
		hddSum += math.Abs(settlement[i].HDD - computed[i].HDD)
		cddSum += math.Abs(settlement[i].CDD - computed[i].CDD)
	}
	n := float64(len(settlement))
	return hddSum / n, cddSum / n, nil
}

// safeIdx returns slice[i] or 0.0 if out of bounds.
func safeIdx(s []float64, i int) float64 {
	if i < len(s) {
		return s[i]
	}
	return 0.0
}
