package app

import (
	"sync"
	"time"
)

// rateLimiter is the global guardrail: one token bucket shared by every action and every
// trigger (packet, HTTP control plane, remote command). Per-action cooldowns smooth repeated
// firing of a single action; the bucket bounds the total rate, which is what protects the
// machine from a broadcast storm that hits several rules at once (§21 guardrails).
//
// A zero rate disables it, so the default configuration pays nothing.
type rateLimiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time

	rate  float64
	burst float64
	now   func() time.Time
}

func newRateLimiter(rate float64, burst int) *rateLimiter {
	limiter := &rateLimiter{rate: rate, now: time.Now}
	if rate <= 0 {
		return limiter
	}

	capacity := bucketCapacity(rate, burst)
	limiter.burst = capacity
	limiter.tokens = capacity

	return limiter
}

// bucketCapacity is the bucket size: the configured burst, or one second of the configured
// rate when no burst is set. A sub-1/s rate still admits one action at a time.
func bucketCapacity(rate float64, burst int) float64 {
	if burst >= 1 {
		return float64(burst)
	}

	return max(1, rate)
}

func (l *rateLimiter) enabled() bool {
	return l != nil && l.rate > 0
}

// allow consumes one token and reports whether the action may run. When the bucket is empty it
// also returns how long until the next token appears.
func (l *rateLimiter) allow() (time.Duration, bool) {
	if !l.enabled() {
		return 0, true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if l.last.IsZero() {
		l.last = now
	} else {
		l.tokens = min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.rate)
		l.last = now
	}

	if l.tokens >= 1 {
		l.tokens--

		return 0, true
	}

	return time.Duration((1 - l.tokens) / l.rate * float64(time.Second)), false
}

// matches reports whether the limiter already runs with this configuration, so a reload keeps
// the running bucket instead of silently refilling it.
func (l *rateLimiter) matches(rate float64, burst int) bool {
	if !l.enabled() {
		return rate <= 0
	}

	return l.rate == rate && l.burst == bucketCapacity(rate, burst)
}

// config returns the live settings, for the control plane to report.
func (l *rateLimiter) config() (float64, int) {
	if !l.enabled() {
		return 0, 0
	}

	return l.rate, int(l.burst)
}
