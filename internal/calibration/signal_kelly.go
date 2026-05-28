// signal_kelly.go — per-signal adaptive Kelly multiplier (TASK-156).
//
// Signals with a strong historical Brier score get a higher Kelly cap;
// poorly-calibrated signals are penalised. The multiplier is applied on top
// of the base MaxKellyFraction so over-performing signals can bet more while
// under-performing ones are scaled back automatically.
//
// Brier → multiplier table (requires ≥MinSignalSamples resolved bets):
//
//	< 0.10  → 1.50x  (excellent calibration)
//	< 0.14  → 1.20x  (good)
//	< 0.18  → 1.00x  (baseline)
//	< 0.22  → 0.75x  (under-performing)
//	≥ 0.22  → 0.50x  (poor calibration — scale back hard)
package calibration

import (
	"fmt"
	"math"
)

// MinSignalSamples is the minimum number of resolved bets required before the
// per-signal Kelly multiplier deviates from 1.0. Below this threshold we
// return the neutral 1.0 to avoid over-fitting sparse data.
const MinSignalSamples = 10

// SignalKellyInfo holds the computed Kelly multiplier together with the
// underlying statistics that drove it — useful for logging.
type SignalKellyInfo struct {
	// Multiplier to apply to MaxKellyFraction (1.0 = no change).
	Multiplier float64
	// BrierScore is the per-signal Brier score over all resolved bets.
	// 0 when Count < MinSignalSamples.
	BrierScore float64
	// Count is the number of resolved bets for this signal.
	Count int
}

// String returns a compact human-readable summary for log annotation.
// Example: "signal_kelly=1.20x(rain,brier=0.09,n=42)"
func (s SignalKellyInfo) String(signal string) string {
	if s.Count < MinSignalSamples {
		return fmt.Sprintf("signal_kelly=1.00x(%s,n=%d<min)", signal, s.Count)
	}
	return fmt.Sprintf("signal_kelly=%.2fx(%s,brier=%.3f,n=%d)",
		s.Multiplier, signal, s.BrierScore, s.Count)
}

// brierToKellyMultiplier converts a Brier score to a Kelly multiplier.
// The mapping is deliberately conservative on the upper side (max 1.50x)
// and aggressive on the lower side (min 0.50x) to prevent runaway sizing.
func brierToKellyMultiplier(brier float64) float64 {
	switch {
	case brier < 0.10:
		return 1.50
	case brier < 0.14:
		return 1.20
	case brier < 0.18:
		return 1.00
	case brier < 0.22:
		return 0.75
	default:
		return 0.50
	}
}

// SignalKellyMultipliers computes a SignalKellyInfo for every signal that
// appears in records. Signals with fewer than MinSignalSamples resolved bets
// receive Multiplier=1.0.
//
// The returned map is safe to use concurrently for reads.
func SignalKellyMultipliers(records []BetRecord) map[string]SignalKellyInfo {
	breakdown := SignalBreakdown(records)
	result := make(map[string]SignalKellyInfo, len(breakdown))
	for sig, stats := range breakdown {
		if sig == "(unknown)" {
			continue
		}
		info := SignalKellyInfo{
			Multiplier: 1.0,
			Count:      stats.Count,
		}
		if stats.Count >= MinSignalSamples {
			brier := stats.BrierAvg()
			info.BrierScore = math.Round(brier*10000) / 10000
			info.Multiplier = brierToKellyMultiplier(brier)
		}
		result[sig] = info
	}
	return result
}

// LookupSignalKelly returns the SignalKellyInfo for the given signal from a
// pre-computed map, defaulting to {Multiplier:1.0} when absent.
func LookupSignalKelly(mults map[string]SignalKellyInfo, signal string) SignalKellyInfo {
	if info, ok := mults[signal]; ok {
		return info
	}
	return SignalKellyInfo{Multiplier: 1.0}
}
