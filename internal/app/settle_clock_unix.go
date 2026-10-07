//go:build !windows

package app

import "time"

// unixSleepClock uses the wall-versus-monotonic gap: CLOCK_MONOTONIC freezes during suspend, the
// wall clock keeps going, so the difference is the time the machine spent down.
type unixSleepClock struct {
	wall time.Time // wall reading only: Round(0) drops the monotonic reading
	mono time.Time // the same instant carrying its monotonic reading
}

func newSleepClock() sleepClock {
	c := &unixSleepClock{}
	c.reanchor()

	return c
}

func (c *unixSleepClock) sleepSince() time.Duration {
	now := time.Now()

	// Clamp: the wall clock can also move backwards (NTP, a manual change), and negative drift is
	// not a resume.
	if slept := now.Round(0).Sub(c.wall) - now.Sub(c.mono); slept > 0 {
		return slept
	}

	return 0
}

func (c *unixSleepClock) reanchor() {
	now := time.Now()
	c.wall = now.Round(0)
	c.mono = now
}
