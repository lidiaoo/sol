package app

import (
	"sync"
	"time"
)

// cooldowns rate-limits action execution: an action that fired recently is suppressed
// until its window has elapsed. It is the guardrail against replayed broadcast packets
// (§4.3, §21 guardrails) and applies to packet triggers and manual triggers alike.
type cooldowns struct {
	mu        sync.Mutex
	last      map[string]time.Time
	global    time.Duration
	perAction map[string]time.Duration
	now       func() time.Time
}

func newCooldowns(global time.Duration, perAction map[string]time.Duration) *cooldowns {
	return &cooldowns{
		last:      make(map[string]time.Time),
		global:    global,
		perAction: perAction,
		now:       time.Now,
	}
}

// allow reports whether the action may run now; when it may not, it also returns the
// time left in the current window. An allowed call starts a new window.
func (c *cooldowns) allow(action string) (time.Duration, bool) {
	window := c.windowFor(action)
	if window <= 0 {
		return 0, true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()

	if last, ok := c.last[action]; ok {
		if remaining := window - now.Sub(last); remaining > 0 {
			return remaining, false
		}
	}

	c.last[action] = now

	return 0, true
}

func (c *cooldowns) windowFor(action string) time.Duration {
	if window, ok := c.perAction[action]; ok {
		return window
	}

	return c.global
}
