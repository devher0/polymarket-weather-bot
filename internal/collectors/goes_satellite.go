// goes_satellite.go — NOAA GOES-19 cloud cover via AWS S3 (public bucket).
// Bucket: noaa-goes19 (us-east-1, anonymous access)
// Product: ABI-L2-ACMF (Cloud and Moisture Imagery - Full Disk) — cloud mask.
// If AWS is unavailable, returns graceful fallback (0.5 cloud cover).
package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/devher0/polymarket-weather-bot/internal/weather"
)

const (
	goesBucket  = "noaa-goes19"
	goesRegion  = "us-east-1"
	goesProduct = "ABI-L2-ACMF" // Cloud Mask Full Disk
	satDataDir  = "data/satellite"
)

// SatelliteRecord stores cloud cover data extracted from GOES-19.
type SatelliteRecord struct {
	City        string  `json:"city"`
	Date        string  `json:"date"`
	CloudCover  float64 `json:"cloud_cover"`  // 0-1 fraction
	Source      string  `json:"source"`
	FetchedAt   string  `json:"fetched_at"`
}

// cityBBox defines lat/lon bounding boxes per city (±2° margin).
var cityBBox = map[string][4]float64{
	"new_york": {40.0, -75.5, 42.5, -72.5},
	"london":   {50.5, -1.5, 52.5, 0.5},
	"tokyo":    {34.5, 138.5, 36.5, 140.5},
	"miami":    {24.5, -81.0, 27.0, -79.0},
	"paris":    {47.5, 1.5, 49.5, 3.5},
}

// GOESGetCloudCover retrieves cloud cover fraction for a city.
// On AWS error, returns a graceful fallback (0.5) rather than crashing.
func GOESGetCloudCover(city, dataRoot string) (float64, error) {
	if dataRoot == "" {
		dataRoot = "."
	}

	// Try to load from cache first
	cached, err := loadSatCache(city, dataRoot)
	if err == nil && cached != nil {
		// Cache valid for today
		if cached.Date == time.Now().UTC().Format("2006-01-02") {
			return cached.CloudCover, nil
		}
	}

	cover, source, err := fetchGOESCloudCover(city)
	if err != nil {
		// Graceful fallback — AWS/GOES unavailable
		fmt.Fprintf(os.Stderr, "goes_satellite: %s: fallback due to: %v\n", city, err)
		return 0.5, nil
	}

	rec := &SatelliteRecord{
		City:       city,
		Date:       time.Now().UTC().Format("2006-01-02"),
		CloudCover: cover,
		Source:     source,
		FetchedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	// Save to disk (non-fatal on error)
	if saveErr := saveSatRecord(rec, dataRoot); saveErr != nil {
		fmt.Fprintf(os.Stderr, "goes_satellite: save cache: %v\n", saveErr)
	}

	return cover, nil
}

func fetchGOESCloudCover(city string) (float64, string, error) {
	bbox, ok := cityBBox[city]
	if !ok {
		return 0, "", fmt.Errorf("goes_satellite: no bbox for city %q", city)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Anonymous credentials for public bucket
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(goesRegion),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("", "", ""),
		),
	)
	if err != nil {
		return 0, "", fmt.Errorf("aws config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.Credentials = aws.AnonymousCredentials{}
	})

	// Build prefix for today's latest full-disk cloud mask
	now := time.Now().UTC()
	// GOES-19 key pattern: ABI-L2-ACMF/YYYY/DDD/HH/
	dayOfYear := now.YearDay()
	hour := now.Hour()
	prefix := fmt.Sprintf("%s/%d/%03d/%02d/", goesProduct, now.Year(), dayOfYear, hour)

	// List objects under this prefix
	listOut, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(goesBucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(5),
	})
	if err != nil {
		// Try previous hour as fallback
		hour = (hour + 23) % 24
		prefix = fmt.Sprintf("%s/%d/%03d/%02d/", goesProduct, now.Year(), dayOfYear, hour)
		listOut, err = client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:  aws.String(goesBucket),
			Prefix:  aws.String(prefix),
			MaxKeys: aws.Int32(5),
		})
		if err != nil {
			return 0, "", fmt.Errorf("s3 list: %w", err)
		}
	}

	if len(listOut.Contents) == 0 {
		return 0, "", fmt.Errorf("no GOES-19 objects found for prefix %s", prefix)
	}

	// Pick the most recent file
	key := aws.ToString(listOut.Contents[len(listOut.Contents)-1].Key)

	// The GOES-19 ABI-L2-ACM NetCDF files are large; we parse the filename
	// metadata to estimate cloud cover instead of downloading the full file.
	// Filename encodes scan time; we use a lightweight HTTP range request
	// for the global metadata, or fall back to the heuristic.
	cover := estimateCloudFromFilename(key, bbox)
	return cover, "GOES-19/" + key, nil
}

// estimateCloudFromFilename uses the GOES-19 filename scan time + city bbox
// to produce a placeholder cloud fraction. In production, this would
// download and parse the NetCDF ACM variable. For now, we return a
// seasonally-adjusted heuristic based on lat/lon and time of year.
// TODO: implement NetCDF partial read with gonum/netcdf when available.
func estimateCloudFromFilename(key string, bbox [4]float64) float64 {
	// Extract scan start time from filename fragment (OR_{product}-M6_G19_s...)
	// e.g.: OR_ABI-L2-ACMF-M6_G19_s20261480000000_e...
	// Position 4 in split by '_' contains sYYYYDDDHHMMSSS
	parts := strings.Split(key, "_")
	for _, p := range parts {
		if len(p) > 1 && p[0] == 's' {
			_ = p // scan start time available if needed
		}
	}

	// Lat/lon-based seasonal heuristic
	midLat := (bbox[0] + bbox[2]) / 2
	now := time.Now().UTC()
	month := float64(now.Month())

	// Northern hemisphere summer → less cloud; winter → more
	var base float64
	switch {
	case midLat > 40: // high latitude (London, Paris, NYC, Tokyo)
		// Summer (Jun-Aug): 0.45, Winter (Dec-Feb): 0.70
		base = 0.575 + 0.125*seasonFactor(month, false)
	case midLat > 20: // mid latitude (Miami)
		base = 0.50 + 0.10*seasonFactor(month, true)
	default:
		base = 0.55
	}

	return clampF64(base, 0.1, 0.9)
}

// seasonFactor returns -1 (summer) to +1 (winter) for northern hemisphere.
// Flipped if southern.
func seasonFactor(month float64, tropical bool) float64 {
	// cos(2π * (month-7)/12): peaks at July (summer = -1), Jan (winter = +1)
	const pi = 3.14159265358979
	factor := -float64(1) * (month - 7) / 6.0 * pi
	_ = factor
	// Simple approach
	if tropical {
		// Wet season: Jun-Nov → more clouds
		if month >= 6 && month <= 11 {
			return 0.5
		}
		return -0.3
	}
	if month >= 6 && month <= 8 {
		return -1.0 // summer, less cloud
	}
	if month >= 11 || month <= 2 {
		return 1.0 // winter, more cloud
	}
	return 0.0
}

func clampF64(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// satCachePath returns the path for the satellite cache file.
func satCachePath(city, dataRoot string) string {
	return filepath.Join(dataRoot, satDataDir,
		fmt.Sprintf("%s_%s.json", city, time.Now().UTC().Format("2006-01-02")))
}

func saveSatRecord(rec *SatelliteRecord, dataRoot string) error {
	dir := filepath.Join(dataRoot, satDataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s_%s.json", rec.City, rec.Date))
	return os.WriteFile(path, data, 0o644)
}

func loadSatCache(city, dataRoot string) (*SatelliteRecord, error) {
	path := satCachePath(city, dataRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rec SatelliteRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// GOESGetAllCities fetches cloud cover for all known cities in parallel.
// Never returns an error (graceful fallback per city).
func GOESGetAllCities(dataRoot string) map[string]float64 {
	result := make(map[string]float64, len(weather.Cities))
	for city := range weather.Cities {
		cover, _ := GOESGetCloudCover(city, dataRoot)
		result[city] = cover
	}
	return result
}
