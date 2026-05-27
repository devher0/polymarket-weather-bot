// gfs_test.go — unit tests for TASK-092: NOAA GFS collector.
package collectors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

// gfsTestResponse builds a minimal Open-Meteo GFS JSON response for n days.
func gfsTestResponse(n int) []byte {
	type daily struct {
		Time          []string  `json:"time"`
		TempMax       []float64 `json:"temperature_2m_max"`
		TempMin       []float64 `json:"temperature_2m_min"`
		PrecipSum     []float64 `json:"precipitation_sum"`
		PrecipProbMax []float64 `json:"precipitation_probability_max"`
		WindSpeedMax  []float64 `json:"wind_speed_10m_max"`
		WeatherCode   []int     `json:"weather_code"`
	}
	type resp struct {
		Daily daily `json:"daily"`
	}

	d := daily{}
	for i := 0; i < n; i++ {
		d.Time = append(d.Time, "2026-06-01")
		d.TempMax = append(d.TempMax, float64(28+i))
		d.TempMin = append(d.TempMin, float64(18+i))
		d.PrecipSum = append(d.PrecipSum, 1.5)
		d.PrecipProbMax = append(d.PrecipProbMax, 40)
		d.WindSpeedMax = append(d.WindSpeedMax, 25)
		d.WeatherCode = append(d.WeatherCode, 1)
	}
	b, _ := json.Marshal(resp{Daily: d})
	return b
}

// patchGFSClient replaces the package-level gfsClient with a client pointed
// at srv and returns a cleanup function.
func patchGFSClient(srv *httptest.Server) func() {
	orig := gfsClient
	gfsClient = srv.Client()
	return func() { gfsClient = orig }
}

func TestGFSGetForecast_Basic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(gfsTestResponse(7))
	}))
	defer srv.Close()
	defer patchGFSClient(srv)()

	// Temporarily override the Open-Meteo URL by patching gfsClient transport.
	// We also need to intercept the real URL — easiest via a custom transport.
	saved := gfsClient
	gfsClient = &http.Client{
		Transport: &roundTripFunc{fn: func(req *http.Request) (*http.Response, error) {
			// Redirect all requests to the test server.
			req2, _ := http.NewRequest(req.Method, srv.URL+req.URL.Path+"?"+req.URL.RawQuery, req.Body)
			req2.Header = req.Header
			return saved.Do(req2)
		}},
	}
	defer func() { gfsClient = saved }()

	// Clear cache before test.
	gfsCache.Delete("new_york_7")

	fc, err := GFSGetForecast("new_york", 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc) != 7 {
		t.Fatalf("expected 7 days, got %d", len(fc))
	}
	if fc[0].MaxTempC != 28 {
		t.Errorf("MaxTempC[0]: expected 28, got %.1f", fc[0].MaxTempC)
	}
	if fc[0].City != "new_york" {
		t.Errorf("City: expected new_york, got %s", fc[0].City)
	}
	if fc[0].PrecipitationProbability != 40 {
		t.Errorf("PrecipitationProbability: expected 40, got %.1f", fc[0].PrecipitationProbability)
	}
}

func TestGFSGetForecast_DaysClamped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify forecast_days param is clamped to 16.
		fd := r.URL.Query().Get("forecast_days")
		if fd != "16" {
			t.Errorf("expected forecast_days=16, got %q", fd)
		}
		w.Write(gfsTestResponse(16))
	}))
	defer srv.Close()

	saved := gfsClient
	gfsClient = &http.Client{
		Transport: &roundTripFunc{fn: func(req *http.Request) (*http.Response, error) {
			req2, _ := http.NewRequest(req.Method, srv.URL+req.URL.Path+"?"+req.URL.RawQuery, req.Body)
			req2.Header = req.Header
			return saved.Do(req2)
		}},
	}
	defer func() { gfsClient = saved }()

	gfsCache.Delete("new_york_16")

	fc, err := GFSGetForecast("new_york", 99) // should be clamped to 16
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc) != 16 {
		t.Fatalf("expected 16 days, got %d", len(fc))
	}
}

func TestGFSGetForecast_UnknownCity(t *testing.T) {
	_, err := GFSGetForecast("atlantis", 7)
	if err == nil {
		t.Fatal("expected error for unknown city, got nil")
	}
}

func TestGFSGet16DayForecast_ReturnsUpTo16(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(gfsTestResponse(16))
	}))
	defer srv.Close()

	saved := gfsClient
	gfsClient = &http.Client{
		Transport: &roundTripFunc{fn: func(req *http.Request) (*http.Response, error) {
			req2, _ := http.NewRequest(req.Method, srv.URL+req.URL.Path+"?"+req.URL.RawQuery, req.Body)
			req2.Header = req.Header
			return saved.Do(req2)
		}},
	}
	defer func() { gfsClient = saved }()

	gfsCache.Delete("london_16")

	fc, err := GFSGet16DayForecast("london")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc) != 16 {
		t.Fatalf("expected 16 forecast days, got %d", len(fc))
	}
}

func TestGFSGetForecast_Cache(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Write(gfsTestResponse(3))
	}))
	defer srv.Close()

	saved := gfsClient
	gfsClient = &http.Client{
		Transport: &roundTripFunc{fn: func(req *http.Request) (*http.Response, error) {
			req2, _ := http.NewRequest(req.Method, srv.URL+req.URL.Path+"?"+req.URL.RawQuery, req.Body)
			req2.Header = req.Header
			return saved.Do(req2)
		}},
	}
	defer func() { gfsClient = saved }()

	gfsCache.Delete("tokyo_3")

	if _, err := GFSGetForecast("tokyo", 3); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if _, err := GFSGetForecast("tokyo", 3); err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 HTTP call (second hit cache), got %d", callCount)
	}
}

// TestFusedForecastHas16DayField verifies that FusedForecast has a Forecast16Days
// field of the correct type (TASK-092 requirement).
func TestFusedForecastHas16DayField(t *testing.T) {
	ff := &FusedForecast{}
	ff.Forecast16Days = []weather.Forecast{
		{City: "new_york", MaxTempC: 30},
	}
	if len(ff.Forecast16Days) != 1 {
		t.Errorf("expected 1 entry in Forecast16Days, got %d", len(ff.Forecast16Days))
	}
	if ff.Forecast16Days[0].MaxTempC != 30 {
		t.Errorf("expected MaxTempC=30, got %.1f", ff.Forecast16Days[0].MaxTempC)
	}
}

// roundTripFunc is a helper to build a custom http.RoundTripper from a func.
type roundTripFunc struct {
	fn func(*http.Request) (*http.Response, error)
}

func (rt *roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return rt.fn(r)
}
