package auth

import (
	"sync"
	"time"
)

// RateLimiter is a per-key token bucket used to slow down password guessing.
//
// It lives in memory, which is the right scope here: the deployment runs a single
// replica against a single SQLite file, so there is no second process to share
// state with. If this ever scales out, the limiter is what has to move first.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	// burst is how many attempts are allowed back to back before throttling.
	burst float64
	// refill is how many tokens are restored per second.
	refill float64
	// idle is how long an untouched bucket is kept before being swept.
	idle time.Duration

	now func() time.Time
}

type bucket struct {
	tokens float64
	seen   time.Time
}

// NewRateLimiter allows burst attempts immediately and then one more every
// window/burst afterwards.
func NewRateLimiter(burst int, window time.Duration) *RateLimiter {
	if burst < 1 {
		burst = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		burst:   float64(burst),
		refill:  float64(burst) / window.Seconds(),
		idle:    window * 4,
		now:     time.Now,
	}
}

// SetClock replaces the limiter's clock. Intended for tests.
func (rl *RateLimiter) SetClock(now func() time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.now = now
}

// Allow consumes one token for key. When it returns false, retryAfter says how long
// until the next attempt would succeed.
func (rl *RateLimiter) Allow(key string) (allowed bool, retryAfter time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	b, ok := rl.buckets[key]
	if !ok {
		// Sweeping on miss keeps the map bounded without a background goroutine.
		rl.sweepLocked(now)
		b = &bucket{tokens: rl.burst, seen: now}
		rl.buckets[key] = b
	} else {
		elapsed := now.Sub(b.seen).Seconds()
		if elapsed > 0 {
			b.tokens = min(rl.burst, b.tokens+elapsed*rl.refill)
		}
		b.seen = now
	}

	if b.tokens < 1 {
		missing := 1 - b.tokens
		return false, time.Duration(missing / rl.refill * float64(time.Second))
	}

	b.tokens--
	return true, 0
}

// Reset clears a key's history. Called after a successful sign-in so that one
// forgotten password does not throttle the rest of the evening.
func (rl *RateLimiter) Reset(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.buckets, key)
}

func (rl *RateLimiter) sweepLocked(now time.Time) {
	for key, b := range rl.buckets {
		if now.Sub(b.seen) > rl.idle {
			delete(rl.buckets, key)
		}
	}
}
