package auth

import (
	"testing"
	"time"
)

func TestRateLimiterBurstThenThrottle(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(5, time.Minute)
	rl.SetClock(func() time.Time { return now })

	for i := range 5 {
		allowed, _ := rl.Allow("1.2.3.4")
		if !allowed {
			t.Fatalf("attempt %d was refused, want the first 5 to pass", i+1)
		}
	}

	allowed, retryAfter := rl.Allow("1.2.3.4")
	if allowed {
		t.Fatal("the sixth attempt was allowed, want it throttled")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter = %v, want a positive wait", retryAfter)
	}
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(5, time.Minute)
	rl.SetClock(func() time.Time { return now })

	for range 5 {
		rl.Allow("1.2.3.4")
	}
	if allowed, _ := rl.Allow("1.2.3.4"); allowed {
		t.Fatal("bucket was not exhausted")
	}

	// Five per minute means one token back every twelve seconds.
	now = now.Add(12 * time.Second)
	if allowed, wait := rl.Allow("1.2.3.4"); !allowed {
		t.Errorf("attempt after a refill was refused, retry after %v", wait)
	}
}

func TestRateLimiterKeysAreIndependent(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(2, time.Minute)
	rl.SetClock(func() time.Time { return now })

	rl.Allow("1.2.3.4")
	rl.Allow("1.2.3.4")
	if allowed, _ := rl.Allow("1.2.3.4"); allowed {
		t.Fatal("the first key was not exhausted")
	}

	if allowed, _ := rl.Allow("5.6.7.8"); !allowed {
		t.Error("a different address was throttled by someone else's attempts")
	}
}

// A successful sign-in clears the record, so one forgotten password does not lock
// the household out for the rest of the hour.
func TestRateLimiterReset(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(2, time.Minute)
	rl.SetClock(func() time.Time { return now })

	rl.Allow("1.2.3.4")
	rl.Allow("1.2.3.4")
	if allowed, _ := rl.Allow("1.2.3.4"); allowed {
		t.Fatal("bucket was not exhausted")
	}

	rl.Reset("1.2.3.4")
	if allowed, _ := rl.Allow("1.2.3.4"); !allowed {
		t.Error("attempt after Reset was refused")
	}
}

func TestRateLimiterDegenerateSettings(t *testing.T) {
	tests := []struct {
		name   string
		burst  int
		window time.Duration
	}{
		{name: "zero burst", burst: 0, window: time.Minute},
		{name: "negative burst", burst: -3, window: time.Minute},
		{name: "zero window", burst: 5, window: 0},
		{name: "negative window", burst: 5, window: -time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rl := NewRateLimiter(tt.burst, tt.window)
			// The contract is only that it stays usable and never divides by zero.
			if allowed, _ := rl.Allow("k"); !allowed {
				t.Error("the very first attempt was refused")
			}
			for range 10 {
				rl.Allow("k")
			}
		})
	}
}

// Buckets for addresses that stopped knocking must not accumulate forever.
func TestRateLimiterSweepsIdleBuckets(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	rl := NewRateLimiter(5, time.Minute)
	rl.SetClock(func() time.Time { return now })

	for i := range 100 {
		rl.Allow(string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}

	// Far past the idle window; the next miss triggers a sweep.
	now = now.Add(time.Hour)
	rl.Allow("a-brand-new-key")

	rl.mu.Lock()
	remaining := len(rl.buckets)
	rl.mu.Unlock()

	if remaining != 1 {
		t.Errorf("buckets remaining = %d, want only the new one", remaining)
	}
}
