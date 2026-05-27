// Package ratelimit provides anti-detection and rate-limiting measures for
// the Polymarket Weather Bot when running in live mode.
//
// Features:
//   - BetJitter: random human-like delay before each bet placement (30s–3min)
//   - FuzzSize: randomly varies bet size by ±3–7% so amounts don't look mechanical
//   - RateLimiter: token-bucket rate limiter (configurable requests/minute)
//   - MarketCooldown: per-conditionID cooldown — prevents betting the same market
//     more than once per configurable interval (default 4 hours)
package ratelimit

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// BetJitter sleeps a random duration between 30 seconds and 3 minutes before
// a bet placement to mimic human-like timing and avoid mechanical patterns.
func BetJitter() {
	const (
		minDelay = 30 * time.Second
		maxDelay = 3 * time.Minute
	)
	jitter := minDelay + time.Duration(rand.Int63n(int64(maxDelay-minDelay)))
	time.Sleep(jitter)
}

// FuzzSize randomly varies amount by ±3–7% and rounds to 2 decimal places.
// This ensures bet sizes don't look mechanical to market surveillance systems.
//
// Example: $10.00 → $9.67 or $10.43
func FuzzSize(amount float64) float64 {
	if amount <= 0 {
		return amount
	}
	// Pick a random perturbation magnitude in [0.03, 0.07].
	magnitude := 0.03 + rand.Float64()*0.04 // [0.03, 0.07)
	// Randomly add or subtract.
	if rand.Intn(2) == 0 {
		magnitude = -magnitude
	}
	fuzzed := amount * (1 + magnitude)
	// Round to 2 decimal places.
	return math.Round(fuzzed*100) / 100
}

// RateLimiter is a simple token-bucket rate limiter using a channel as the
// bucket. It allows at most requestsPerMinute calls per minute.
type RateLimiter struct {
	mu      sync.Mutex
	ticker  *time.Ticker
	tokens  chan struct{}
	stopped bool
}

// NewRateLimiter creates a RateLimiter that allows at most requestsPerMinute
// calls per minute. It starts a background goroutine that refills one token
// at each tick interval (60s / requestsPerMinute).
//
// Typical defaults:
//   - Market fetching: 20 req/min
//   - Order placement: 6 req/min
func NewRateLimiter(requestsPerMinute int) *RateLimiter {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 1
	}
	interval := time.Minute / time.Duration(requestsPerMinute)
	rl := &RateLimiter{
		// Pre-fill the bucket so the first batch of calls isn't blocked.
		tokens: make(chan struct{}, requestsPerMinute),
		ticker: time.NewTicker(interval),
	}
	// Pre-fill up to half capacity so the bot can start immediately.
	prefill := requestsPerMinute / 2
	if prefill < 1 {
		prefill = 1
	}
	for i := 0; i < prefill; i++ {
		rl.tokens <- struct{}{}
	}
	// Background goroutine refills one token per tick.
	go func() {
		for range rl.ticker.C {
			rl.mu.Lock()
			stopped := rl.stopped
			rl.mu.Unlock()
			if stopped {
				return
			}
			select {
			case rl.tokens <- struct{}{}:
			default:
				// Bucket is full — drop the token (don't block).
			}
		}
	}()
	return rl
}

// Wait blocks until a token is available, consuming one from the bucket.
// Call this before each API request to enforce the rate limit.
func (rl *RateLimiter) Wait() {
	<-rl.tokens
}

// Stop shuts down the background ticker goroutine. Call when the limiter is
// no longer needed to avoid goroutine leaks.
func (rl *RateLimiter) Stop() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if !rl.stopped {
		rl.stopped = true
		rl.ticker.Stop()
	}
}

// MarketCooldown tracks the last time a bet was placed on each conditionID.
// It prevents the bot from betting the same market more than once per
// minIntervalHours (default 4 hours).
type MarketCooldown struct {
	mu       sync.Mutex
	lastBets map[string]time.Time
}

// NewMarketCooldown creates a new MarketCooldown tracker.
func NewMarketCooldown() *MarketCooldown {
	return &MarketCooldown{
		lastBets: make(map[string]time.Time),
	}
}

// CanBet returns true if the conditionID has never been bet, or if the last
// bet was placed more than minIntervalHours ago.
//
// minIntervalHours = 4 is the recommended default (don't re-bet within 4h).
func (mc *MarketCooldown) CanBet(conditionID string, minIntervalHours float64) bool {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	last, ok := mc.lastBets[conditionID]
	if !ok {
		return true
	}
	elapsed := time.Since(last).Hours()
	return elapsed >= minIntervalHours
}

// RecordBet records the current time as the last-bet timestamp for conditionID.
// Call this immediately after a bet is successfully placed.
func (mc *MarketCooldown) RecordBet(conditionID string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.lastBets[conditionID] = time.Now()
}
