// Package ratelimit provides an in-memory token-bucket limiter behind the
// Limiter interface, so a shared store can replace it for
// multi-node deployments without touching call sites.
package ratelimit

import (
	"sync"
	"time"
)

// Decision is the result of a limiter check, carrying the values needed for the
// X-RateLimit-* and Retry-After headers.
type Decision struct {
	Allowed    bool
	Limit      int
	Remaining  int
	ResetAt    time.Time
	RetryAfter time.Duration
}

// Limiter decides whether a request keyed by a string may proceed.
type Limiter interface {
	Allow(key string) Decision
}

type bucket struct {
	tokens float64
	last   time.Time
}

// TokenBucket is a per-key token-bucket limiter safe for concurrent use.
type TokenBucket struct {
	mu          sync.Mutex
	buckets     map[string]*bucket
	ratePerSec  float64
	burst       float64
	limitPerMin int
	lastSweep   time.Time
}

// NewTokenBucket builds a limiter allowing perMin sustained requests with the
// given burst capacity.
func NewTokenBucket(perMin, burst int) *TokenBucket {
	if burst < 1 {
		burst = 1
	}
	return &TokenBucket{
		buckets:     make(map[string]*bucket),
		ratePerSec:  float64(perMin) / 60.0,
		burst:       float64(burst),
		limitPerMin: perMin,
		lastSweep:   time.Now(),
	}
}

// Allow consumes one token for key.
func (t *TokenBucket) Allow(key string) Decision {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	b := t.buckets[key]
	if b == nil {
		b = &bucket{tokens: t.burst, last: now}
		t.buckets[key] = b
	}

	// Refill based on elapsed time.
	b.tokens = min(t.burst, b.tokens+now.Sub(b.last).Seconds()*t.ratePerSec)
	b.last = now

	t.sweep(now)

	if b.tokens >= 1 {
		b.tokens--
		return Decision{
			Allowed:   true,
			Limit:     t.limitPerMin,
			Remaining: int(b.tokens),
			ResetAt:   now.Add(t.timeToFull(b.tokens)),
		}
	}
	wait := time.Duration(float64(time.Second) * (1 - b.tokens) / t.ratePerSec)
	return Decision{
		Allowed:    false,
		Limit:      t.limitPerMin,
		Remaining:  0,
		ResetAt:    now.Add(wait),
		RetryAfter: wait,
	}
}

func (t *TokenBucket) timeToFull(tokens float64) time.Duration {
	if t.ratePerSec <= 0 {
		return time.Minute
	}
	return time.Duration(float64(time.Second) * (t.burst - tokens) / t.ratePerSec)
}

// sweep evicts idle buckets occasionally to bound memory.
func (t *TokenBucket) sweep(now time.Time) {
	if now.Sub(t.lastSweep) < 10*time.Minute {
		return
	}
	for k, b := range t.buckets {
		if now.Sub(b.last) > 10*time.Minute {
			delete(t.buckets, k)
		}
	}
	t.lastSweep = now
}
