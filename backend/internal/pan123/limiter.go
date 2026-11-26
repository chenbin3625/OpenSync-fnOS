package pan123

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// tokenBucket implements a lightweight, dependency-free token bucket limiter.
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(rate float64) *tokenBucket {
	if rate <= 0 {
		return nil
	}
	burst := math.Ceil(rate)
	if burst < 1 {
		burst = 1
	}
	return &tokenBucket{
		rate:   rate,
		burst:  burst,
		tokens: burst,
		last:   time.Now(),
	}
}

func (tb *tokenBucket) Wait(ctx context.Context) error {
	if tb == nil || tb.rate <= 0 {
		return ctx.Err()
	}
	for {
		tb.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(tb.last).Seconds()
		if elapsed > 0 {
			tb.tokens += elapsed * tb.rate
			if tb.tokens > tb.burst {
				tb.tokens = tb.burst
			}
			tb.last = now
		}
		if tb.tokens >= 1 {
			tb.tokens -= 1
			tb.mu.Unlock()
			return nil
		}
		waitSec := (1 - tb.tokens) / tb.rate
		tb.mu.Unlock()

		waitDur := time.Duration(waitSec * float64(time.Second))
		if waitDur < 5*time.Millisecond {
			waitDur = 5 * time.Millisecond
		}
		if err := sleepContext(ctx, waitDur); err != nil {
			return err
		}
	}
}

// endpointLimiter enforces per-endpoint rate limits plus an optional global
// TPS override supplied by user configuration.
type endpointLimiter struct {
	global   *tokenBucket
	defaults map[string]float64
	fallback float64

	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

func newEndpointLimiter(globalTPS float64, defaults map[string]float64, fallback float64) *endpointLimiter {
	var g *tokenBucket
	if globalTPS > 0 {
		g = newTokenBucket(globalTPS)
	}
	return &endpointLimiter{
		global:   g,
		defaults: defaults,
		fallback: fallback,
		buckets:  make(map[string]*tokenBucket),
	}
}

func (el *endpointLimiter) Wait(ctx context.Context, endpoint string) error {
	if el == nil {
		return ctx.Err()
	}
	if el.global != nil {
		return el.global.Wait(ctx)
	}
	rate := el.fallback
	if el.defaults != nil {
		if r, ok := el.defaults[endpoint]; ok {
			rate = r
		}
	}
	if rate <= 0 {
		return ctx.Err()
	}
	el.mu.Lock()
	b, ok := el.buckets[endpoint]
	if !ok {
		b = newTokenBucket(rate)
		el.buckets[endpoint] = b
	}
	el.mu.Unlock()
	return b.Wait(ctx)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryBackoff(attempt int, retryAfterHeader string) time.Duration {
	if d := parseRetryAfter(retryAfterHeader); d > 0 {
		if d > 15*time.Second {
			return 15 * time.Second
		}
		return d
	}
	if attempt < 0 {
		attempt = 0
	}
	d := 500 * time.Millisecond << attempt
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}

func parseRetryAfter(raw string) time.Duration {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(s); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func isRetryableHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests ||
		(status >= 500 && status <= 511)
}
