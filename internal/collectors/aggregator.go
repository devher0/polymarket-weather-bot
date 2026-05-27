// aggregator.go — fuses weather data from all sources into a single FusedForecast.
// Sources and weights: OpenMeteo=0.35, NASA=0.30, NOAA=0.25, GOES=0.10
//
// TASK-031: all source fetches run in parallel goroutines with a shared 8-second
// context deadline, cutting the per-city fetch time from ~12 s to ~5 s.
// AggregateAll also fetches all cities concurrently.
package collectors

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// FusedForecast combines forecasts from multiple sources with confidence score.
type FusedForecast struct {
	weather.Forecast
	Confidence          float64                    // 0-1: how much sources agree (1 = full agreement)
	Sources             []string                   // which sources contributed
	EnsembleUncertainty float64                    // stddev of temperature across ensemble members (°C); 0 if unavailable
	FetchedAt           time.Time                  // when this forecast was assembled (for staleness checks)
	PerSourceForecasts  map[string]weather.Forecast // raw per-source forecasts (for accuracy tracking)
}

// staticSourceWeights defines the base weight for each data source.
// At runtime these may be overridden by DynamicWeights() once enough
// resolved bets have accumulated (TASK-032).
var staticSourceWeights = map[string]float64{
	"openmeteo": 0.35,
	"nasa":      0.30,
	"noaa":      0.25,
	"goes":      0.10,
}

// currentWeights returns the active source weights: dynamic if enough data
// exists, otherwise the static baseline.
// dataRoot may be empty (uses ".").
func currentWeights(dataRoot string) map[string]float64 {
	accuracy := LoadSourceAccuracy(dataRoot)
	if len(accuracy) == 0 {
		return staticSourceWeights
	}
	w := DynamicWeights(accuracy)
	LogDynamicWeights(w)
	return w
}

// sourceResult holds a forecast from one source along with its weight.
type sourceResult struct {
	name     string
	forecast weather.Forecast
	weight   float64
	// cloud cover override for GOES (no standard Forecast fields)
	cloudCover *float64
}

// sourceFetchTimeout is the maximum time we wait for all sources to respond.
// Individual sources that exceed this deadline are gracefully dropped.
const sourceFetchTimeout = 8 * time.Second

// collectSources fetches from all weather sources concurrently and returns
// the results that succeeded within sourceFetchTimeout.
//
// dayOffset selects which forecast day index to use (0=today … 6).
// includeGOES enables the GOES-19 satellite cloud-cover source (today only).
// weights overrides staticSourceWeights when non-nil.
func collectSources(ctx context.Context, city string, days int, dayOffset int, dataRoot string, includeGOES bool) []sourceResult {
	weights := currentWeights(dataRoot)
	type item struct {
		r  sourceResult
		ok bool
	}

	numSources := 3
	if includeGOES {
		numSources = 4
	}

	ch := make(chan item, numSources)

	// --- OpenMeteo ---
	go func() {
		fc, err := weather.GetForecast(city, days)
		if err != nil || len(fc) == 0 {
			ch <- item{}
			return
		}
		idx := dayOffset
		if idx >= len(fc) {
			idx = len(fc) - 1
		}
		ch <- item{r: sourceResult{name: "openmeteo", forecast: fc[idx], weight: weights["openmeteo"]}, ok: true}
	}()

	// --- NASA POWER ---
	go func() {
		fc, err := NASAGetForecast(city, days)
		if err != nil || len(fc) == 0 {
			ch <- item{}
			return
		}
		idx := dayOffset
		if idx >= len(fc) {
			idx = len(fc) - 1
		}
		ch <- item{r: sourceResult{name: "nasa", forecast: fc[idx], weight: weights["nasa"]}, ok: true}
	}()

	// --- NOAA NWS (US only) ---
	go func() {
		fc, err := NOAAGetForecast(city, days)
		if err != nil || len(fc) == 0 {
			ch <- item{}
			return
		}
		idx := dayOffset
		if idx >= len(fc) {
			idx = len(fc) - 1
		}
		ch <- item{r: sourceResult{name: "noaa", forecast: fc[idx], weight: weights["noaa"]}, ok: true}
	}()

	// --- GOES-19 (cloud cover supplement, today only) ---
	if includeGOES {
		go func() {
			cover, err := GOESGetCloudCover(city, dataRoot)
			if err != nil {
				ch <- item{}
				return
			}
			ch <- item{r: sourceResult{name: "goes", weight: weights["goes"], cloudCover: &cover}, ok: true}
		}()
	}

	results := make([]sourceResult, 0, numSources)
	for i := 0; i < numSources; i++ {
		select {
		case it := <-ch:
			if it.ok {
				results = append(results, it.r)
			}
		case <-ctx.Done():
			// Deadline exceeded — return whatever succeeded so far.
			return results
		}
	}
	return results
}

// Aggregate fetches forecasts from all available sources and fuses them.
// dataRoot is used for GOES cache and the forecast disk cache. Pass "" to use current directory.
//
// TASK-041: checks the on-disk forecast cache first; returns a cached result
// when it is < 2 hours old, skipping all upstream API calls.
func Aggregate(city string, dataRoot string) (*FusedForecast, error) {
	// TASK-041: try disk cache before making any network calls.
	if cached, ok := LoadForecastCache(city, 0, dataRoot, 0); ok {
		return cached, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), sourceFetchTimeout)
	defer cancel()

	results := collectSources(ctx, city, 3, 0, dataRoot, true)

	if len(results) == 0 {
		return nil, fmt.Errorf("aggregator: no data sources available for city %q", city)
	}

	ff := fuse(city, results)

	// TASK-027: replace inter-model confidence with ensemble-based uncertainty
	// when the ICON seamless ensemble is reachable. Lower temperature spread
	// across members → higher confidence.
	if er, err := GetEnsembleForecast(city, 0); err == nil {
		ff.Confidence = EnsembleToConfidence(er.TempStdDev)
		ff.EnsembleUncertainty = er.TempStdDev
		ff.Sources = append(ff.Sources, "ensemble")
	}

	// TASK-042: detect and log significant forecast changes before overwriting cache.
	// Telegram notifications for shifts are sent from cmd/bot/main.go to avoid
	// an import cycle (collectors → notifier → calibration → collectors).
	if shift := DetectForecastShift(city, 0, ff, dataRoot); shift != nil && shift.Significant {
		slog.Warn("significant forecast shift detected",
			"city", city,
			"delta_max_temp_c", fmt.Sprintf("%+.1f", shift.DeltaMaxTempC),
			"delta_precip_p", fmt.Sprintf("%+.1f", shift.DeltaPrecipP),
		)
	}

	// TASK-041: persist to disk cache for future cycles.
	if err := SaveForecastCache(city, 0, ff, dataRoot); err != nil {
		slog.Warn("forecast cache save failed", "city", city, "err", err)
	}

	return ff, nil
}

// fuse computes weighted averages and confidence from source results.
func fuse(city string, results []sourceResult) *FusedForecast {
	// Normalise weights to available sources.
	totalWeight := 0.0
	for _, r := range results {
		totalWeight += r.weight
	}
	if totalWeight == 0 {
		totalWeight = 1
	}

	// Collect source names and weighted sums for each parameter.
	var (
		wMaxTemp float64
		wMinTemp float64
		wPrecip  float64
		wPrecipP float64
		wWind    float64
		wCode    float64

		sourceNames []string
		precipProbs []float64 // for confidence calc
	)

	// Filter to sources that have actual forecast data (not just cloud cover).
	forecastSources := make([]sourceResult, 0, len(results))
	for _, r := range results {
		sourceNames = append(sourceNames, r.name)
		if r.cloudCover == nil {
			forecastSources = append(forecastSources, r)
		}
	}

	// Recalculate total weight for forecast sources only.
	fcWeight := 0.0
	for _, r := range forecastSources {
		fcWeight += r.weight
	}
	if fcWeight == 0 {
		fcWeight = 1
	}

	for _, r := range forecastSources {
		w := r.weight / fcWeight
		wMaxTemp += r.forecast.MaxTempC * w
		wMinTemp += r.forecast.MinTempC * w
		wPrecip += r.forecast.PrecipitationMM * w
		wPrecipP += r.forecast.PrecipitationProbability * w
		wWind += r.forecast.WindSpeedKMH * w
		wCode += float64(r.forecast.WeatherCode) * w
		precipProbs = append(precipProbs, r.forecast.PrecipitationProbability/100.0)
	}

	// Pick representative date from highest-weight source.
	bestDate := ""
	bestW := -1.0
	for _, r := range forecastSources {
		if r.weight > bestW {
			bestW = r.weight
			bestDate = r.forecast.Date
		}
	}
	if bestDate == "" {
		bestDate = "unknown"
	}

	// Confidence = 1 - stddev of precipitation probabilities across sources.
	confidence := 1.0
	if len(precipProbs) > 1 {
		sd := stddev(precipProbs)
		confidence = math.Max(0, 1-sd)
	}

	// Build per-source forecast map (excludes cloud-cover-only sources).
	perSourceForecasts := make(map[string]weather.Forecast, len(forecastSources))
	for _, r := range forecastSources {
		perSourceForecasts[r.name] = r.forecast
	}

	fused := &FusedForecast{
		Forecast: weather.Forecast{
			City:                     city,
			Date:                     bestDate,
			MaxTempC:                 wMaxTemp,
			MinTempC:                 wMinTemp,
			PrecipitationMM:          wPrecip,
			PrecipitationProbability: wPrecipP,
			WindSpeedKMH:             wWind,
			WeatherCode:              int(math.Round(wCode)),
		},
		Confidence:         confidence,
		Sources:            sourceNames,
		FetchedAt:          time.Now(),
		PerSourceForecasts: perSourceForecasts,
	}

	// Extreme-event confidence boost: when the fused forecast shows an obvious
	// extreme (heat wave / heavy rain / storm), all NWP models tend to agree
	// even with limited sources — raise confidence to at least the floor.
	if extreme, tag := weather.IsExtreme(fused.Forecast); extreme {
		if fused.Confidence < weather.ExtremeConfidenceFloor {
			fused.Confidence = weather.ExtremeConfidenceFloor
		}
		fused.Sources = append(fused.Sources, tag)
	}

	return fused
}

// AggregateForDay fetches a fused forecast for a specific day offset:
// dayOffset=0 → today, dayOffset=1 → tomorrow, up to dayOffset=6.
// GOES satellite data (cloud cover) is only available for today (dayOffset=0).
//
// TASK-041: checks the on-disk forecast cache first (TTL 2h) before hitting APIs.
func AggregateForDay(city string, dayOffset int, dataRoot string) (*FusedForecast, error) {
	if dayOffset < 0 {
		dayOffset = 0
	}
	if dayOffset > 6 {
		dayOffset = 6
	}

	// TASK-041: try disk cache before making any network calls.
	if cached, ok := LoadForecastCache(city, dayOffset, dataRoot, 0); ok {
		return cached, nil
	}

	days := dayOffset + 1 // need at least dayOffset+1 forecast days

	ctx, cancel := context.WithTimeout(context.Background(), sourceFetchTimeout)
	defer cancel()

	includeGOES := dayOffset == 0
	results := collectSources(ctx, city, days, dayOffset, dataRoot, includeGOES)

	if len(results) == 0 {
		return nil, fmt.Errorf("aggregator: no data sources available for city %q day+%d", city, dayOffset)
	}

	ff := fuse(city, results)
	// Embed the target date in the fused result so callers can inspect it.
	if ff.Forecast.Date == "" || ff.Forecast.Date == "unknown" {
		ff.Forecast.Date = time.Now().UTC().AddDate(0, 0, dayOffset).Format("2006-01-02")
	}

	// TASK-027: replace inter-model confidence with ensemble-based uncertainty.
	// For dayOffset > 0 the ensemble may have lower accuracy; that's fine — the
	// stddev naturally widens for distant days, reducing confidence as expected.
	if er, err := GetEnsembleForecast(city, dayOffset); err == nil {
		ff.Confidence = EnsembleToConfidence(er.TempStdDev)
		ff.EnsembleUncertainty = er.TempStdDev
		ff.Sources = append(ff.Sources, "ensemble")
	}

	// TASK-042: detect and log significant forecast changes before overwriting cache.
	if shift := DetectForecastShift(city, dayOffset, ff, dataRoot); shift != nil && shift.Significant {
		slog.Warn("significant forecast shift detected",
			"city", city,
			"day_offset", dayOffset,
			"delta_max_temp_c", fmt.Sprintf("%+.1f", shift.DeltaMaxTempC),
			"delta_precip_p", fmt.Sprintf("%+.1f", shift.DeltaPrecipP),
		)
	}

	// TASK-041: persist to disk cache for future cycles.
	if err := SaveForecastCache(city, dayOffset, ff, dataRoot); err != nil {
		slog.Warn("forecast cache save failed", "city", city, "day_offset", dayOffset, "err", err)
	}

	return ff, nil
}

// AggregateAll fetches fused forecasts for all known cities concurrently.
// Each city is fetched in its own goroutine; errors are collected and returned
// as a combined error only when ALL cities fail.
func AggregateAll(dataRoot string) (map[string]*FusedForecast, error) {
	cities := make([]string, 0, len(weather.Cities))
	for city := range weather.Cities {
		cities = append(cities, city)
	}

	type cityResult struct {
		city string
		ff   *FusedForecast
		err  error
	}

	ch := make(chan cityResult, len(cities))
	var wg sync.WaitGroup

	for _, city := range cities {
		city := city // capture
		wg.Add(1)
		go func() {
			defer wg.Done()
			ff, err := Aggregate(city, dataRoot)
			ch <- cityResult{city: city, ff: ff, err: err}
		}()
	}

	// Close channel after all goroutines finish.
	go func() {
		wg.Wait()
		close(ch)
	}()

	out := make(map[string]*FusedForecast, len(cities))
	var errs []string

	for res := range ch {
		if res.err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", res.city, res.err))
			continue
		}
		out[res.city] = res.ff
	}

	if len(errs) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("aggregator: all cities failed: %v", errs)
	}
	return out, nil
}

// AggregateForCities fetches fresh forecasts only for cities that appear in
// activeCities (i.e. have at least one active Polymarket weather market).
//
// Cities not in activeCities are served from the on-disk cache when available;
// if no cache exists for an inactive city it is simply omitted from the result
// and a log line is emitted. This reduces unnecessary API calls on quiet days
// when only 3–4 cities have live markets (TASK-043).
func AggregateForCities(activeCities []string, dataRoot string) (map[string]*FusedForecast, error) {
	activeSet := make(map[string]bool, len(activeCities))
	for _, c := range activeCities {
		activeSet[c] = true
	}

	type cityResult struct {
		city string
		ff   *FusedForecast
		err  error
	}

	allCities := make([]string, 0, len(weather.Cities))
	for city := range weather.Cities {
		allCities = append(allCities, city)
	}

	ch := make(chan cityResult, len(allCities))
	var wg sync.WaitGroup

	for _, city := range allCities {
		city := city // capture
		wg.Add(1)
		go func() {
			defer wg.Done()
			if activeSet[city] {
				// Active city: fetch fresh data (Aggregate checks cache internally first).
				ff, err := Aggregate(city, dataRoot)
				ch <- cityResult{city: city, ff: ff, err: err}
				return
			}
			// Inactive city: only serve from cache; skip expensive API calls.
			if cached, ok := LoadForecastCache(city, 0, dataRoot, 0); ok {
				ch <- cityResult{city: city, ff: cached}
			} else {
				slog.Info("skipping forecast: no active markets", "city", city)
				ch <- cityResult{city: city} // no data, no error
			}
		}()
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	out := make(map[string]*FusedForecast, len(allCities))
	var errs []string

	for res := range ch {
		if res.err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", res.city, res.err))
			continue
		}
		if res.ff != nil {
			out[res.city] = res.ff
		}
	}

	// Only error when ALL active cities failed; inactive-city cache misses are non-fatal.
	if len(errs) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("aggregator: all cities failed: %v", errs)
	}
	return out, nil
}

// stddev computes population standard deviation of a slice.
func stddev(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	mean := 0.0
	for _, v := range vals {
		mean += v
	}
	mean /= float64(len(vals))

	variance := 0.0
	for _, v := range vals {
		d := v - mean
		variance += d * d
	}
	variance /= float64(len(vals))
	return math.Sqrt(variance)
}
