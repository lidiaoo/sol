//go:build windows

package app

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	modkernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procGetTickCount64             = modkernel32.NewProc("GetTickCount64")
	procQueryUnbiasedInterruptTime = modkernel32.NewProc("QueryUnbiasedInterruptTime")
)

// windowsSleepClock uses the one pair of Windows counters that disagree about sleep:
// GetTickCount64 counts time spent suspended, QueryUnbiasedInterruptTime ("unbiased") does not.
// The difference is therefore the time the machine was down. Neither Go's monotonic clock
// (QueryPerformanceCounter) nor the wall clock can tell - both keep counting through standby.
type windowsSleepClock struct {
	wall     time.Time // wall reading only: Round(0) drops the monotonic reading
	mono     time.Time
	ticks    uint64 // GetTickCount64 in milliseconds, sleep included
	unbiased uint64 // QueryUnbiasedInterruptTime in nanoseconds, sleep excluded
}

func newSleepClock() sleepClock {
	c := &windowsSleepClock{}
	c.reanchor()

	return c
}

// tickCount64 returns milliseconds since the machine started, including time spent suspended.
func tickCount64() uint64 {
	r, _, _ := procGetTickCount64.Call()

	return uint64(r)
}

// unbiasedInterruptTime returns working-state time in nanoseconds: time the machine was actually
// running, with suspend and hibernation left out.
func unbiasedInterruptTime() uint64 {
	var raw uint64

	procQueryUnbiasedInterruptTime.Call(uintptr(unsafe.Pointer(&raw)))

	return raw * 100 // the API reports 100-nanosecond units
}

func (c *windowsSleepClock) sleepSince() time.Duration {
	slept := time.Duration(tickCount64()-c.ticks)*time.Millisecond -
		time.Duration(unbiasedInterruptTime()-c.unbiased)

	// Keep the wall-versus-monotonic term as well. It is zero on today's Windows - which is the bug
	// this code exists for - but it costs two reads and still holds if Go ever switches the monotonic
	// clock to a source that freezes during suspend.
	now := time.Now()
	if alt := now.Round(0).Sub(c.wall) - now.Sub(c.mono); alt > slept {
		slept = alt
	}

	if slept > 0 {
		return slept
	}

	return 0
}

func (c *windowsSleepClock) reanchor() {
	now := time.Now()
	c.wall = now.Round(0)
	c.mono = now
	c.ticks = tickCount64()
	c.unbiased = unbiasedInterruptTime()
}
