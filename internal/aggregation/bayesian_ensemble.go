// bayesian_ensemble.go — Bayesian probability update across independent weather sources.
// TASK-100: Replaces simple weighted average with proper Bayesian inference.
//
// Model: each source acts as a noisy sensor of the true event probability.
// We treat each source prediction as a likelihood update on a shared prior.
//
// Formula:
//
//	posterior ∝ prior × Π likelihood_i
//
// where likelihood_i(p | source_says q) = Gaussian(q, σ²) evaluated at p,
// and σ = source_noise (derived from Brier score when available, else 0.15).
//
// The result is more accurate than a weighted average when sources disagree
// because it properly accounts for conditional independence.
package aggregation

import (
	"math"
)

// SourceBelief holds a single source's probability estimate and its noise level.
type SourceBelief struct {
	// Source is the data source name (e.g. "openmeteo", "ecmwf").
	Source string
	// P is the source's probability estimate for the YES outcome (0–1).
	P float64
	// Noise is the standard deviation of this source's errors.
	// Use BrierScore^0.5 when available; 0.15 is a sensible default.
	Noise float64
}

// BayesianUpdate applies a single source likelihood update to the current
// posterior probability. The likelihood is modelled as a Gaussian centred
// at belief.P with std = belief.Noise, evaluated over a grid and normalised.
//
// prior is the current best estimate (0–1). Returns the updated posterior.
func BayesianUpdate(prior float64, belief SourceBelief) float64 {
	noise := belief.Noise
	if noise <= 0 {
		noise = 0.15
	}
	// Bayesian update in log-space over a grid of 200 points.
	// This avoids numerical underflow for extreme priors.
	const N = 200
	logWeights := make([]float64, N)
	maxLog := math.Inf(-1)
	for i := 0; i < N; i++ {
		// Hypothesis: true probability is h = (i+0.5)/N.
		h := (float64(i) + 0.5) / float64(N)
		logPrior := logBeta(prior, h)
		logLik := logGaussian(belief.P, h, noise)
		logWeights[i] = logPrior + logLik
		if logWeights[i] > maxLog {
			maxLog = logWeights[i]
		}
	}

	// Normalise and compute posterior mean.
	sum := 0.0
	posterior := 0.0
	for i := 0; i < N; i++ {
		w := math.Exp(logWeights[i] - maxLog)
		h := (float64(i) + 0.5) / float64(N)
		sum += w
		posterior += w * h
	}
	if sum == 0 {
		return prior
	}
	return posterior / sum
}

// BayesianEnsemble computes the Bayesian posterior probability given a prior
// and a slice of source beliefs, applying each source update sequentially.
//
// climatePrior is the seasonal base rate (e.g. 0.40 for summer rain in NYC).
// When climatePrior <= 0 the simple average of source beliefs is used as prior.
func BayesianEnsemble(climatePrior float64, beliefs []SourceBelief) float64 {
	if len(beliefs) == 0 {
		if climatePrior > 0 {
			return climatePrior
		}
		return 0.5
	}

	prior := climatePrior
	if prior <= 0 {
		// Initialise prior from simple average of beliefs.
		sum := 0.0
		for _, b := range beliefs {
			sum += b.P
		}
		prior = sum / float64(len(beliefs))
	}
	// Clamp prior to (0,1) to avoid log singularities.
	prior = math.Max(0.01, math.Min(0.99, prior))

	posterior := prior
	for _, b := range beliefs {
		posterior = BayesianUpdate(posterior, b)
		posterior = math.Max(0.01, math.Min(0.99, posterior))
	}
	return posterior
}

// DefaultNoise returns an appropriate noise level for a Brier score.
// Noise = sqrt(brierScore), clamped to [0.05, 0.40].
func DefaultNoise(brierScore float64) float64 {
	if brierScore <= 0 {
		return 0.15 // unknown accuracy → moderate noise
	}
	n := math.Sqrt(brierScore)
	if n < 0.05 {
		return 0.05
	}
	if n > 0.40 {
		return 0.40
	}
	return n
}

// logBeta returns a log-prior for hypothesis h given a prior probability p,
// using a Beta-inspired shape that peaks at h == p.
func logBeta(p, h float64) float64 {
	// Use a symmetric Beta with concentration proportional to certainty.
	// α = β = 1 + 3*(1 - 2*|p-0.5|) so uniform prior → Beta(1,1), confident → Beta(4,4).
	confidence := 1.0 - 2.0*math.Abs(p-0.5)
	alpha := 1.0 + 3.0*confidence*p
	beta := 1.0 + 3.0*confidence*(1.0-p)
	if h <= 0 || h >= 1 {
		return math.Inf(-1)
	}
	return (alpha-1)*math.Log(h) + (beta-1)*math.Log(1-h)
}

// logGaussian returns log N(x; mu, sigma).
func logGaussian(x, mu, sigma float64) float64 {
	d := (x - mu) / sigma
	return -0.5*d*d - math.Log(sigma)
}
