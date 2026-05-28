// volume_tracker.go — per-market volume tracking and snapshot persistence.
// TASK-221
package markets

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// VolumeSnapshot records a volume reading for one market at a point in time.
type VolumeSnapshot struct {
	ConditionID string    `json:"condition_id"`
	Question    string    `json:"question"`
	Volume24h   float64   `json:"volume_24h"`
	TotalVolume float64   `json:"total_volume"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// FetchVolume calls the Gamma API and returns the total traded volume in USDC
// for the given conditionID. Returns 0 and an error when the market is not found.
func FetchVolume(conditionID string) (float64, error) {
	url := fmt.Sprintf("https://gamma-api.polymarket.com/markets?condition_id=%s", conditionID)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("gamma volume fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("gamma volume fetch: status %d", resp.StatusCode)
	}

	var result []struct {
		Volume string `json:"volume"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("gamma volume decode: %w", err)
	}
	if len(result) == 0 {
		return 0, nil
	}
	v, _ := strconv.ParseFloat(result[0].Volume, 64)
	return v, nil
}

// volumeFile returns path to today's volume snapshot file.
func volumeFile(dataRoot string) string {
	date := time.Now().UTC().Format("2006-01-02")
	dir := filepath.Join(dataRoot, "volume")
	return filepath.Join(dir, date+".json")
}

// SaveVolumeSnapshots persists a slice of snapshots to data/volume/{date}.json.
// Existing file is overwritten with the combined deduplicated set.
func SaveVolumeSnapshots(snapshots []VolumeSnapshot, dataRoot string) error {
	path := volumeFile(dataRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Merge with existing snapshots (newer wins by conditionID).
	existing, _ := LoadTodayVolumeSnapshots(dataRoot)
	byID := make(map[string]VolumeSnapshot, len(existing)+len(snapshots))
	for _, s := range existing {
		byID[s.ConditionID] = s
	}
	for _, s := range snapshots {
		byID[s.ConditionID] = s
	}
	merged := make([]VolumeSnapshot, 0, len(byID))
	for _, s := range byID {
		merged = append(merged, s)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].TotalVolume > merged[j].TotalVolume
	})

	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadTodayVolumeSnapshots reads today's volume snapshot file, returning an
// empty slice (not an error) when the file is missing.
func LoadTodayVolumeSnapshots(dataRoot string) ([]VolumeSnapshot, error) {
	data, err := os.ReadFile(volumeFile(dataRoot))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snaps []VolumeSnapshot
	if err := json.Unmarshal(data, &snaps); err != nil {
		return nil, err
	}
	return snaps, nil
}

// SnapshotsFromMarkets converts a []Market slice into VolumeSnapshots using
// the VolumeUSDC already fetched during GetWeatherMarkets. It also sets the
// HighVolume flag (>10 000 USDC) on the market in-place.
func SnapshotsFromMarkets(mks []Market) []VolumeSnapshot {
	snaps := make([]VolumeSnapshot, 0, len(mks))
	now := time.Now().UTC()
	for i := range mks {
		if mks[i].VolumeUSDC >= 10_000 {
			mks[i].HighVolume = true
		}
		snaps = append(snaps, VolumeSnapshot{
			ConditionID: mks[i].ConditionID,
			Question:    mks[i].Question,
			TotalVolume: mks[i].VolumeUSDC,
			UpdatedAt:   now,
		})
	}
	return snaps
}
