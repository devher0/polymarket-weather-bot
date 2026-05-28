// calibration_curve.go — per-signal calibration error tracker (TASK-218).
//
// Splits predicted probabilities into 5 equal-width buckets and compares
// the average predicted probability in each bucket to the actual win rate.
// The Expected Calibration Error (ECE) summarises the mean |pred − actual|
// weighted by bucket count.
package calibration

import "math"

// CalibrationBucket holds statistics for one probability bin.
type CalibrationBucket struct {
	// PredMid is the midpoint of the bucket interval (0.1, 0.3, 0.5, 0.7, 0.9).
	PredMid float64
	// AvgPred is the actual mean predicted probability of bets in this bucket.
	AvgPred float64
	// ActualRate is the empirical win rate for bets in this bucket (resolved only).
	ActualRate float64
	// Count is the number of resolved bets in this bucket.
	Count int
}

// bucketIndex returns the 0–4 bucket index for probability p.
// Buckets: [0,0.2), [0.2,0.4), [0.4,0.6), [0.6,0.8), [0.8,1.0]
// p==1.0 is clamped into bucket 4.
func bucketIndex(p float64) int {
	idx := int(p * 5)
	if idx > 4 {
		idx = 4
	}
	return idx
}

var bucketMids = [5]float64{0.1, 0.3, 0.5, 0.7, 0.9}

// BuildCalibrationCurve computes a 5-bucket calibration curve for the given
// signal. Pass an empty string to include all signals. Only resolved bets are
// counted; unresolved records are ignored.
func BuildCalibrationCurve(records []BetRecord, signal string) []CalibrationBucket {
	type acc struct {
		predSum float64
		wins    int
		count   int
	}
	var buckets [5]acc

	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		if signal != "" && r.Signal != signal {
			continue
		}
		idx := bucketIndex(r.OurProbability)
		buckets[idx].predSum += r.OurProbability
		buckets[idx].count++
		if *r.Outcome {
			buckets[idx].wins++
		}
	}

	out := make([]CalibrationBucket, 5)
	for i, b := range buckets {
		cb := CalibrationBucket{PredMid: bucketMids[i]}
		if b.count > 0 {
			cb.AvgPred = b.predSum / float64(b.count)
			cb.ActualRate = float64(b.wins) / float64(b.count)
		} else {
			cb.AvgPred = bucketMids[i]
			cb.ActualRate = 0
		}
		cb.Count = b.count
		out[i] = cb
	}
	return out
}

// CalibrationError returns the Expected Calibration Error (ECE): the mean
// |predicted − actual| across all non-empty buckets, weighted by count.
// Returns 0 when no resolved bets exist.
func CalibrationError(curve []CalibrationBucket) float64 {
	var totalWeight, weightedErr float64
	for _, b := range curve {
		if b.Count == 0 {
			continue
		}
		w := float64(b.Count)
		weightedErr += w * math.Abs(b.AvgPred-b.ActualRate)
		totalWeight += w
	}
	if totalWeight == 0 {
		return 0
	}
	return weightedErr / totalWeight
}

// CalibrationDiagnosis returns a short human-readable diagnosis string based
// on ECE and the direction of the most populated bucket's error.
func CalibrationDiagnosis(curve []CalibrationBucket, ece float64) string {
	if ece < 0.03 {
		return "well-calibrated"
	}

	// Check dominant direction: positive = predicted > actual (overconfident).
	var biasSum, biasW float64
	for _, b := range curve {
		if b.Count == 0 {
			continue
		}
		biasSum += float64(b.Count) * (b.AvgPred - b.ActualRate)
		biasW += float64(b.Count)
	}
	if biasW == 0 {
		return "no data"
	}
	bias := biasSum / biasW

	switch {
	case ece >= 0.10 && bias > 0:
		return "severely overconfident"
	case ece >= 0.10 && bias < 0:
		return "severely underconfident"
	case bias > 0.02:
		return "overconfident"
	case bias < -0.02:
		return "underconfident"
	default:
		return "slightly miscalibrated"
	}
}

// AllSignalCalibrations returns calibration curves and ECE for every unique
// signal present in resolved records, plus a synthetic "" key for "all".
// Signals with fewer than minBets resolved bets are excluded.
func AllSignalCalibrations(records []BetRecord, minBets int) map[string][]CalibrationBucket {
	// Collect unique signal names.
	sigSet := make(map[string]struct{})
	for _, r := range records {
		if r.Outcome == nil {
			continue
		}
		if r.Signal != "" {
			sigSet[r.Signal] = struct{}{}
		}
	}

	out := make(map[string][]CalibrationBucket)
	// Overall curve.
	allCurve := BuildCalibrationCurve(records, "")
	total := 0
	for _, b := range allCurve {
		total += b.Count
	}
	if total >= minBets {
		out[""] = allCurve
	}
	// Per-signal curves.
	for sig := range sigSet {
		curve := BuildCalibrationCurve(records, sig)
		cnt := 0
		for _, b := range curve {
			cnt += b.Count
		}
		if cnt >= minBets {
			out[sig] = curve
		}
	}
	return out
}
