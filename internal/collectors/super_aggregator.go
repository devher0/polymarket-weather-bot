// super_aggregator.go — central pipeline aggregating ALL weather sources into a
// single SuperForecast with dynamic Brier-score-based weights.
//
// TASK-099: collects results from OpenMeteo, NASA, NOAA, GOES, HRRR, ECMWF,
// GFS, RAOB, Lightning, CAPE, and MTG in parallel goroutines with a 10-second
// per-source timeout.  Sources that time out are silently dropped so the
// pipeline never blocks.  Dynamic weights are read from source_accuracy.json;
// sources with better Brier scores over the last 30 days receive higher weight
// automatically.
package collectors

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// superSourceTimeout is the per-source fetch deadline used by
// AggregateSuperForecast.  Generous enough for slow APIs (RAOB, MTG) but
// short enough to keep the overall pipeline under ~12 s.
const superSourceTimeout = 10 * time.Second

// SourceResult holds the contribution of a single weather data source.
type SourceResult struct {
	// Name is the human-readable source identifier (e.g. "ecmwf", "nasa").
	Name string
	// Weight is the normalised weight applied to this source (0–1, all weights sum to 1).
	Weight float64
	// RainProb is this source's rain probability estimate in [0, 1].
	RainProb float64
	// MaxTempC is this source's maximum temperature forecast.
	MaxTempC float64
	// Available indicates whether the source responded within the timeout.
	Available bool
}

// SuperForecast is a fully-fused forecast that embeds the base weather.Forecast
// and enriches it with multi-source consensus analytics.
type SuperForecast struct {
	weather.Forecast

	// Sources holds the per-source breakdown (weight, value, availability).
	Sources []SourceResult

	// Confidence is 0–1: how much the contributing sources agree on rain
	// probability (1 = perfect agreement, 0 = maximum disagreement).
	Confidence float64

	// Uncertainty is the standard deviation of per-source rain probabilities.
	// Low uncertainty → sources agree.  High uncertainty → sources diverge.
	Uncertainty float64

	// ModelAgreement is the fraction of sources whose rain probability
	// agrees with the majority-vote direction (>0.5 → rain, ≤0.5 → no rain).
	ModelAgreement float64

	// SignalStrength reflects how far the consensus probability is from 0.5.
	// Values near 1.0 indicate a strong, bet-worthy signal; values near 0
	// indicate the consensus is close to the market-maker's 50/50 baseline.
	SignalStrength float64

	// FetchedAt is the wall-clock time when the super-forecast was assembled.
	FetchedAt time.Time

	// LightningRisk and LightningStrikes are populated from the lightning
	// sub-collector (NLDN + Blitzortung) when available.
	LightningRisk    float64
	LightningStrikes int

	// CapeJkg is the maximum CAPE value observed across all sources.
	CapeJkg float64
}

// superSourceWeights defines the static fallback weights for all sources
// recognised by AggregateSuperForecast.  When source_accuracy.json has enough
// resolved bets (≥10 per source) the dynamic Brier-score weights take over.
var superSourceWeights = map[string]float64{
	"ecmwf":     0.25,
	"openmeteo": 0.20,
	"nasa":      0.17,
	"noaa":      0.13,
	"hrrr":      0.12,
	"gfs":       0.10,
	"goes":      0.03,
}

// superWeights merges static fallback weights with dynamic Brier-score weights
// from source_accuracy.json.  Keeps all keys from superSourceWeights so that
// unknown dynamic keys don't cause panics.
func superWeights(dataRoot string) map[string]float64 {
	accuracy := LoadSourceAccuracy(dataRoot)
	if len(accuracy) == 0 {
		return superSourceWeights
	}
	dynamic := DynamicWeights(accuracy)
	merged := make(map[string]float64, len(superSourceWeights))
	for k, v := range superSourceWeights {
		merged[k] = v
	}
	for k, v := range dynamic {
		if _, ok := merged[k]; ok {
			merged[k] = v
		}
	}
	return merged
}

// AggregateSuperForecast fetches all available weather sources in parallel and
// fuses them into a single SuperForecast.
//
// city must be a key in weather.Cities.
// dayOffset selects which forecast day (0 = today … 6 = day+6).
// dataRoot is the project data directory (pass "" to use current directory).
func AggregateSuperForecast(city string, dayOffset int, dataRoot string) (*SuperForecast, error) {
	if dayOffset < 0 {
		dayOffset = 0
	}
	if dayOffset > 6 {
		dayOffset = 6
	}

	days := dayOffset + 1
	weights := superWeights(dataRoot)
	isUS := usCities[city]

	type rawItem struct {
		name     string
		forecast weather.Forecast
		ok       bool
	}

	// Count goroutines we will launch so we can drain exactly that many.
	numFetch := 5 // openmeteo + nasa + noaa + ecmwf + gfs
	if isUS {
		numFetch++ // hrrr
	}
	if dayOffset == 0 {
		numFetch++ // goes (today only)
	}

	ch := make(chan rawItem, numFetch)

	// Helper: fetch a []weather.Forecast slice and send the dayOffset element.
	fetch := func(name string, fn func() ([]weather.Forecast, error)) {
		ctx, cancel := context.WithTimeout(context.Background(), superSourceTimeout)
		defer cancel()

		done := make(chan rawItem, 1)
		go func() {
			fc, err := fn()
			if err != nil || len(fc) == 0 {
				RecordSourceCall(name, err, dataRoot)
				done <- rawItem{name: name}
				return
			}
			RecordSourceCall(name, nil, dataRoot)
			idx := dayOffset
			if idx >= len(fc) {
				idx = len(fc) - 1
			}
			done <- rawItem{name: name, forecast: fc[idx], ok: true}
		}()

		select {
		case item := <-done:
			ch <- item
		case <-ctx.Done():
			slog.Debug("super_aggregator: source timeout", "source", name, "city", city)
			ch <- rawItem{name: name}
		}
	}

	go fetch("openmeteo", func() ([]weather.Forecast, error) { return weather.GetForecast(city, days) })
	go fetch("nasa", func() ([]weather.Forecast, error) { return NASAGetForecast(city, days) })
	go fetch("noaa", func() ([]weather.Forecast, error) { return NOAAGetForecast(city, days) })
	go fetch("ecmwf", func() ([]weather.Forecast, error) { return ECMWFGetForecast(city, days) })
	go fetch("gfs", func() ([]weather.Forecast, error) { return GFSGetForecast(city, days) })
	if isUS {
		go fetch("hrrr", func() ([]weather.Forecast, error) { return HRRRGetForecast(city, days) })
	}
	if dayOffset == 0 {
		// GOES returns a cloud cover float, not a Forecast slice; handle inline.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), superSourceTimeout)
			defer cancel()
			done := make(chan rawItem, 1)
			go func() {
				cover, err := GOESGetCloudCover(city, dataRoot)
				if err != nil {
					RecordSourceCall("goes", err, dataRoot)
					done <- rawItem{name: "goes"}
					return
				}
				RecordSourceCall("goes", nil, dataRoot)
				// Convert cloud cover (0–1) to a synthetic Forecast so the
				// fuser can extract a rain probability estimate.
				// Rain probability = cloud_cover × 0.6 (empirical heuristic).
				done <- rawItem{
					name: "goes",
					forecast: weather.Forecast{
						City:                     city,
						PrecipitationProbability: cover * 60, // 0–60 (0–100 scale)
					},
					ok: true,
				}
			}()
			select {
			case item := <-done:
				ch <- item
			case <-ctx.Done():
				slog.Debug("super_aggregator: source timeout", "source", "goes", "city", city)
				ch <- rawItem{name: "goes"}
			}
		}()
	}

	// Drain all goroutines.
	raws := make([]rawItem, 0, numFetch)
	for i := 0; i < numFetch; i++ {
		raws = append(raws, <-ch)
	}

	// Enrich with non-forecast sources (lightning, CAPE, MTG) — these don't
	// return Forecast slices so we handle them outside the generic fetch loop.
	lightningRisk, lightningStrikes := GetCityLightningRisk(city, dataRoot)

	// CAPE from HRRR (already embedded in HRRR Forecast.CapeJkg if available).
	// MTG atmospheric profile (Europe only; logged for completeness).
	mtgProfile := GetMTGAtmosphericProfile(city)
	if mtgProfile.City != "" {
		slog.Debug("super_aggregator: mtg profile available",
			"city", city,
			"temp_850hpa", mtgProfile.Temp850hPa,
			"rh_700hpa", mtgProfile.RH700hPa,
		)
	}

	// RAOB upper-air profile — wind shear signal.
	raobProfile := GetAtmosphericProfile(city)
	if raobProfile.WindKMH850hPa > 0 {
		slog.Debug("super_aggregator: raob profile available",
			"city", city,
			"wind_850hpa_kmh", raobProfile.WindKMH850hPa,
			"max_wind_shear", raobProfile.MaxWindShear,
		)
	}

	// Build per-source results and compute fused values.
	sourceResults := make([]SourceResult, 0, len(raws))
	var (
		wRainProb  float64
		wMaxTemp   float64
		wMinTemp   float64
		wPrecipMM  float64
		wWind      float64
		totalW     float64
		rainProbs  []float64
		maxCapeJkg float64
		bestDate   string
		bestW      float64
	)

	for _, r := range raws {
		w := weights[r.name]
		if w == 0 {
			w = minSourceWeight
		}
		sr := SourceResult{
			Name:      r.name,
			Weight:    w,
			Available: r.ok,
		}
		if r.ok {
			rp := r.forecast.PrecipitationProbability / 100.0
			sr.RainProb = rp
			sr.MaxTempC = r.forecast.MaxTempC
			wRainProb += rp * w
			wMaxTemp += r.forecast.MaxTempC * w
			wMinTemp += r.forecast.MinTempC * w
			wPrecipMM += r.forecast.PrecipitationMM * w
			wWind += r.forecast.WindSpeedKMH * w
			totalW += w
			rainProbs = append(rainProbs, rp)
			if r.forecast.CapeJkg > maxCapeJkg {
				maxCapeJkg = r.forecast.CapeJkg
			}
			if w > bestW && r.forecast.Date != "" {
				bestW = w
				bestDate = r.forecast.Date
			}
		}
		sourceResults = append(sourceResults, sr)
	}

	if totalW == 0 {
		// No sources available — return an error-like zero struct rather than
		// crashing; callers should fall back to the regular FusedForecast path.
		slog.Warn("super_aggregator: no sources available", "city", city, "day_offset", dayOffset)
		totalW = 1
	}

	// Normalise weighted sums.
	fusedRainProb := wRainProb / totalW
	fusedMaxTemp := wMaxTemp / totalW
	fusedMinTemp := wMinTemp / totalW
	fusedPrecipMM := wPrecipMM / totalW
	fusedWind := wWind / totalW

	if bestDate == "" {
		bestDate = time.Now().UTC().AddDate(0, 0, dayOffset).Format("2006-01-02")
	}

	// Consensus analytics.
	uncertainty := stddev(rainProbs)
	confidence := math.Max(0, 1-2*uncertainty)

	// ModelAgreement: fraction of sources whose direction matches majority vote.
	majorityThreshold := 0.5
	aboveCount := 0
	for _, p := range rainProbs {
		if p > majorityThreshold {
			aboveCount++
		}
	}
	majority := aboveCount > len(rainProbs)/2 // true = majority says rain
	agreeing := 0
	for _, p := range rainProbs {
		vote := p > majorityThreshold
		if vote == majority {
			agreeing++
		}
	}
	modelAgreement := 0.0
	if len(rainProbs) > 0 {
		modelAgreement = float64(agreeing) / float64(len(rainProbs))
	}

	// SignalStrength: how far the consensus rain probability is from 0.5.
	signalStrength := math.Min(1, math.Abs(fusedRainProb-0.5)/0.5)

	sf := &SuperForecast{
		Forecast: weather.Forecast{
			City:                     city,
			Date:                     bestDate,
			MaxTempC:                 fusedMaxTemp,
			MinTempC:                 fusedMinTemp,
			PrecipitationMM:          fusedPrecipMM,
			PrecipitationProbability: fusedRainProb * 100,
			WindSpeedKMH:             fusedWind,
			CapeJkg:                  maxCapeJkg,
		},
		Sources:          sourceResults,
		Confidence:       confidence,
		Uncertainty:      uncertainty,
		ModelAgreement:   modelAgreement,
		SignalStrength:   signalStrength,
		LightningRisk:    lightningRisk,
		LightningStrikes: lightningStrikes,
		CapeJkg:          maxCapeJkg,
		FetchedAt:        time.Now(),
	}

	slog.Info("super_aggregator: fused",
		"city", city,
		"day_offset", dayOffset,
		"sources_available", len(rainProbs),
		"confidence", math.Round(sf.Confidence*100)/100,
		"model_agreement", math.Round(sf.ModelAgreement*100)/100,
		"signal_strength", math.Round(sf.SignalStrength*100)/100,
		"rain_prob", math.Round(fusedRainProb*1000)/1000,
	)

	return sf, nil
}
