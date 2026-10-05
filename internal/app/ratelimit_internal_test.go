package app

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// limiterAt builds a limiter whose clock the test drives by hand, so no test ever sleeps.
func limiterAt(rate float64, burst int) (*rateLimiter, func(time.Duration)) {
	now := time.Unix(1700000000, 0)

	limiter := newRateLimiter(rate, burst)
	limiter.now = func() time.Time { return now }

	return limiter, func(step time.Duration) { now = now.Add(step) }
}

// allow expects the bucket to admit one action.
func allow(t *testing.T, limiter *rateLimiter) {
	t.Helper()

	retryIn, ok := limiter.allow()
	require.True(t, ok, "expected the bucket to admit this action")
	require.Zero(t, retryIn, "an admitted action reports no retry delay")
}

// deny expects the bucket to be empty and to say when the next token arrives.
func deny(t *testing.T, limiter *rateLimiter) {
	t.Helper()

	retryIn, ok := limiter.allow()
	require.False(t, ok, "expected the bucket to be empty")
	require.Positive(t, retryIn, "a denied action reports when to retry")
}

func TestRateLimiterDisabled(t *testing.T) {
	t.Parallel()

	var nilLimiter *rateLimiter

	for _, limiter := range []*rateLimiter{nilLimiter, newRateLimiter(0, 0), newRateLimiter(-1, 5)} {
		require.False(t, limiter.enabled())

		for range 10 {
			retryIn, allowed := limiter.allow()
			require.True(t, allowed, "a disabled limiter never suppresses")
			require.Zero(t, retryIn)
		}
	}
}

func TestRateLimiterBurstThenRefill(t *testing.T) {
	t.Parallel()

	limiter, advance := limiterAt(2, 3)

	// the burst is available immediately
	for i := range 3 {
		retryIn, allowed := limiter.allow()
		require.True(t, allowed, "token %d of the burst", i+1)
		require.Zero(t, retryIn)
	}

	// empty: the next token is 1/rate away
	retryIn, allowed := limiter.allow()
	require.False(t, allowed)
	require.Equal(t, 500*time.Millisecond, retryIn)

	// half a second at 2/s refills exactly one token, and only one
	advance(500 * time.Millisecond)

	allow(t, limiter)
	deny(t, limiter)

	// a long idle period refills the bucket, never beyond its capacity
	advance(time.Hour)

	for i := range 3 {
		retryIn, allowed := limiter.allow()
		require.True(t, allowed, "token %d after a long idle period", i+1)
		require.Zero(t, retryIn)
	}

	deny(t, limiter)
}

func TestRateLimiterDefaults(t *testing.T) {
	t.Parallel()

	// no burst: one second of the configured rate, never less than one action
	limiter, _ := limiterAt(0.5, 0)
	require.InDelta(t, 1.0, limiter.burst, 1e-9, "a sub-1/s rate still admits one action at a time")

	allow(t, limiter)
	deny(t, limiter)

	limiter, _ = limiterAt(4, 0)
	require.InDelta(t, 4.0, limiter.burst, 1e-9, "an unset burst means one second of the rate")

	for range 4 {
		allow(t, limiter)
	}

	deny(t, limiter)

	// a negative burst is treated as "not set"
	limiter, _ = limiterAt(4, -2)
	require.InDelta(t, 4.0, limiter.burst, 1e-9, "a negative burst is treated as not set")
}

func TestRateLimiterConfigAndMatches(t *testing.T) {
	t.Parallel()

	limiter, _ := limiterAt(10, 0)

	rate, burst := limiter.config()
	require.InDelta(t, 10.0, rate, 1e-9)
	require.Equal(t, 10, burst)

	require.True(t, limiter.matches(10, 0), "an unset burst means one second of the rate")
	require.True(t, limiter.matches(10, 10))
	require.False(t, limiter.matches(10, 20), "a different bucket is a different guard")
	require.False(t, limiter.matches(5, 0))

	// a drained bucket is still the same guard, so a reload keeps it
	allow(t, limiter)
	require.True(t, limiter.matches(10, 0))

	// a disabled limiter matches only a disabled configuration
	off := newRateLimiter(0, 0)
	require.True(t, off.matches(0, 0))
	require.True(t, off.matches(-1, 0))
	require.False(t, off.matches(1, 0))

	rate, burst = off.config()
	require.Zero(t, rate)
	require.Zero(t, burst)
}

func TestRateLimiterIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	const (
		goroutines = 8
		perRoutine = 50
	)

	// one token, so exactly one of the 400 attempts may succeed
	limiter := newRateLimiter(1, 1)
	limiter.now = func() time.Time { return time.Unix(1700000000, 0) }

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)

	for range goroutines {
		wg.Go(func() {
			for range perRoutine {
				if _, ok := limiter.allow(); ok {
					mu.Lock()
					allowed++

					mu.Unlock()
				}
			}
		})
	}

	wg.Wait()

	require.Equal(t, 1, allowed, "a frozen clock hands out exactly the bucket contents")
}
