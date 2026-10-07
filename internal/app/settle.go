package app

import (
	"log/slog"
	"sync"
	"time"
)

// settleState tracks when a state-changing action may be treated as safe to run. Two events make it
// unsafe, and both are exactly the moment a wake-on-LAN packet has just done its job:
//
//   - sol itself having just started, which is what a boot, a reboot and a fresh power-on look like
//     from inside the process. A shutdown packet that lands here would be honoured, the still-armed
//     NIC would wake the machine for the next copy of that packet, and the machine would power down
//     again: the process dies with the machine, so no in-memory window can span that gap, only the
//     age of the process can.
//   - the machine having just come back from suspend. Senders repeat their packet for reliability,
//     the NIC wakes the machine for each copy, and a "sleep" rule would put it straight back down.
//
// The window is measured from whichever event happened last, so a whole burst lands inside it.
type settleState struct {
	mu       sync.Mutex
	now      func() time.Time
	clock    sleepClock // platform-specific: how much of the elapsed time the machine was not running
	start    time.Time  // the window runs from here: process start, or the last resume we noticed
	lastWake time.Time  // when that resume was noticed
}

// settleDriftThreshold is how much of the elapsed time has to be time the machine spent not running
// before that counts as a resume rather than clock noise. How that is measured differs per platform;
// see sleepClock, and note that the obvious measurement does not work on Windows.
const settleDriftThreshold = 5 * time.Second

func newSettleState() *settleState {
	return &settleState{
		now:   time.Now,
		clock: newSleepClock(),
		start: time.Now(),
	}
}

// remaining reports how long the settle window still runs for, or zero when an action may run now.
// Callers check whether the action is protected at all; this only answers the timing question.
func (st *settleState) remaining(window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	now := st.now()

	// Re-anchor on every resume we notice, so one suspend is not counted twice and the window runs
	// from the moment sol sees the machine is back - which is the copy of the packet that arrived
	// right after the resume, the very one that has to be refused.
	if slept := st.clock.sleepSince(); slept > settleDriftThreshold {
		st.lastWake = now
		st.start = now
		st.clock.reanchor()

		slog.Info("machine resumed from sleep", "slept", slept.Round(time.Second).String())
	}

	if left := window - now.Sub(st.start); left > 0 {
		return left
	}

	if !st.lastWake.IsZero() {
		if left := window - now.Sub(st.lastWake); left > 0 {
			return left
		}
	}

	return 0
}
