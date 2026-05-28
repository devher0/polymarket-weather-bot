// confidence_intervals.go — Wilson score confidence intervals for win rates.
//
// The Wilson score interval (Newcombe 1998) outperforms the naive
// Wald interval (p ± z*sqrt(p(1-p)/n)) when n is small or p is
// near 0/1.  We use it throughout the dashboard to flag whether a
// signal's win-rate edge is statistically distinguishable from 50%.
package calibration

import (
	"fmt"
	"math"
)

// WilsonCI computes the Wilson score confidence interval for a proportion.
//
//	wins — number of successes
//	n    — total observations
//	z    — z-score for the desired confidence level (e.g. 1.96 for 95%)
//
// Returns (lo, hi) in [0, 1].  If n == 0 both are 0.5 (maximum uncertainty).
func WilsonCI(wins, n int, z float64) (lo, hi float64) {
	if n <= 0 {
		return 0.0, 1.0
	}
	p := float64(wins) / float64(n)
	nf := float64(n)
	z2 := z * z
	denom := 1.0 + z2/nf
	center := (p + z2/(2*nf)) / denom
	margin := z * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf)) / denom
	lo = math.Max(0.0, center-margin)
	hi = math.Min(1.0, center+margin)
	return lo, hi
}

// WilsonCI95 returns the 95% Wilson score confidence interval (z=1.96).
func WilsonCI95(wins, n int) (lo, hi float64) {
	return WilsonCI(wins, n, 1.96)
}

// FormatCI formats the half-width of a Wilson CI as a percentage string.
//
//	n < 5  → "~" (not enough data)
//	n >= 5 → "±8%" (rounded to nearest integer percentage point)
func FormatCI(lo, hi float64, n int) string {
	if n < 5 {
		return "~"
	}
	halfWidth := (hi - lo) / 2.0 * 100.0
	return fmt.Sprintf("±%d%%", int(math.Round(halfWidth)))
}

// IsSignificantlyAbove50 returns true when the lower bound of the 95% CI
// is strictly above 0.50 — i.e., there is statistically significant evidence
// that the true win rate exceeds the 50% break-even threshold.
func IsSignificantlyAbove50(wins, n int) bool {
	if n < 5 {
		return false
	}
	lo, _ := WilsonCI95(wins, n)
	return lo > 0.50
}

// WinRateWithCI returns a formatted string combining the win rate percentage
// and its 95% CI half-width, e.g. "62% ±8%".
//
// When n < 5 the CI is suppressed to "62%  ~".
func WinRateWithCI(wins, n int) string {
	if n <= 0 {
		return "  — "
	}
	pct := float64(wins) / float64(n) * 100.0
	ci := FormatCI(0, 0, 0) // default: "~"
	if n >= 5 {
		lo, hi := WilsonCI95(wins, n)
		ci = FormatCI(lo, hi, n)
	}
	return fmt.Sprintf("%4.0f%% %s", pct, ci)
}

// SignificanceBadge returns ⚡ when the win rate is statistically significantly
// above 50%, or ❓ otherwise.  Used inline with win-rate figures in Telegram
// to give operators a quick visual signal.
func SignificanceBadge(wins, n int) string {
	if IsSignificantlyAbove50(wins, n) {
		return "⚡"
	}
	return "❓"
}
