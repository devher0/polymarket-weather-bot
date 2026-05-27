// Package httpclient provides a shared HTTP client with automatic retry and
// basic rate limiting, using only the standard library.
//
// Features:
//   - Exponential backoff on 429 / 503 / transient network errors (max 3 tries)
//   - Token-bucket rate limiter (default 10 req/s, configurable)
//   - Respects Retry-After header when present
//   - User-Agent header injected automatically
//
// Usage:
//
//	c := httpclient.New(httpclient.Options{RatePerSec: 5})
//	resp, err := c.Get("https://example.com/api")
package httpclient

import (
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	defaultUserAgent = "polymarket-weather-bot/1.0 (+https://github.com/devher0/polymarket-weather-bot)"
	defaultTimeout   = 20 * time.Second
	maxRetries       = 3
)

// Options configures the shared HTTP client.
type Options struct {
	// RatePerSec is the maximum number of requests per second (default 10).
	RatePerSec float64
	// Timeout is the per-request timeout (default 20s).
	Timeout time.Duration
	// UserAgent overrides the default User-Agent header.
	UserAgent string
}

// Client is an HTTP client with retry and rate-limiting behaviour.
// A zero-value Client is NOT valid; use New() or Default().
type Client struct {
	inner     *http.Client
	userAgent string
	// token bucket fields
	mu          sync.Mutex
	tokens      float64
	maxTokens   float64
	refillRate  float64 // tokens per nanosecond
	lastRefill  time.Time
}

// New creates a Client with the given options.
func New(opts Options) *Client {
	rps := opts.RatePerSec
	if rps <= 0 {
		rps = 10
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}

	return &Client{
		inner:      &http.Client{Timeout: timeout},
		userAgent:  ua,
		tokens:     rps,                         // start full
		maxTokens:  rps,
		refillRate: rps / float64(time.Second), // tokens per ns
		lastRefill: time.Now(),
	}
}

// Default returns a pre-configured Client suitable for most use cases
// (10 req/s, 20s timeout).
var Default = New(Options{})

// ── Token bucket ──────────────────────────────────────────────────────────

// wait blocks until one token is available.
func (c *Client) wait() {
	for {
		c.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(c.lastRefill)
		c.tokens += float64(elapsed) * c.refillRate
		if c.tokens > c.maxTokens {
			c.tokens = c.maxTokens
		}
		c.lastRefill = now

		if c.tokens >= 1.0 {
			c.tokens--
			c.mu.Unlock()
			return
		}

		// Calculate how long until next token.
		needed := 1.0 - c.tokens
		waitNs := time.Duration(needed / c.refillRate)
		c.mu.Unlock()

		// Sleep at minimum 1ms to avoid a hot spin.
		if waitNs < time.Millisecond {
			waitNs = time.Millisecond
		}
		time.Sleep(waitNs)
	}
}

// ── Retry logic ───────────────────────────────────────────────────────────

// isRetryable returns true for transient errors worth retrying.
func isRetryable(statusCode int, err error) bool {
	if err != nil {
		return true // network error
	}
	return statusCode == 429 || statusCode == 503 || statusCode == 502 || statusCode == 504
}

// retryAfter parses the Retry-After header and returns the suggested delay.
// Returns 0 if the header is absent or unparsable.
func retryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	ra := resp.Header.Get("Retry-After")
	if ra == "" {
		return 0
	}
	// Try integer seconds first.
	if secs, err := strconv.Atoi(ra); err == nil {
		return time.Duration(secs) * time.Second
	}
	// Try HTTP-date.
	if t, err := http.ParseTime(ra); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}

// Do executes req with rate limiting and automatic retry.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	var (
		resp *http.Response
		err  error
	)

	for attempt := 0; attempt < maxRetries; attempt++ {
		c.wait() // rate limit

		resp, err = c.inner.Do(req)
		if err == nil && !isRetryable(resp.StatusCode, nil) {
			return resp, nil // success
		}

		// Close the body before retrying to avoid resource leaks.
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}

		if attempt == maxRetries-1 {
			break // no more retries
		}

		// Compute backoff: honour Retry-After, else exponential 1s, 2s, 4s…
		delay := retryAfter(resp)
		if delay == 0 {
			delay = time.Duration(math.Pow(2, float64(attempt))) * time.Second
		}

		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
		}
		slog.Warn("httpclient: retrying",
			"attempt", attempt+1,
			"status", statusCode,
			"err", err,
			"delay", delay,
		)
		time.Sleep(delay)

		// Clone the request for the next attempt (body may be nil for GET).
		if req.GetBody != nil {
			body, cloneErr := req.GetBody()
			if cloneErr == nil {
				req.Body = body
			}
		}
	}

	if err != nil {
		return nil, fmt.Errorf("httpclient: all %d attempts failed: %w", maxRetries, err)
	}
	return resp, nil
}

// Get is a convenience wrapper for GET requests.
func (c *Client) Get(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}
