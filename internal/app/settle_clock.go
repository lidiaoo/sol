package app

import "time"

// sleepClock answers the one question the settle window needs: of the time that has passed since the
// anchor, how much did the machine spend not running? A suspend is the only thing that makes the
// answer large, and it is exactly what a repeated wake-on-LAN packet arrives on - the NIC wakes the
// machine for every copy of the packet, and a "sleep" rule would put it straight back down.
//
// It is platform-specific because operating systems disagree about suspend, and the obvious
// measurement only works on two of the three:
//
//   - Linux and macOS: CLOCK_MONOTONIC stops while the machine is suspended and the wall clock does
//     not, so wall-minus-monotonic is the time spent down.
//   - Windows: nothing stops. Go's monotonic clock there is QueryPerformanceCounter, which keeps
//     counting through standby and hibernation, so wall-minus-monotonic is always zero and a resume
//     is invisible to it. The suspend shows up in a different pair instead: GetTickCount64 includes
//     sleep time, QueryUnbiasedInterruptTime is working-state time only, so their difference is the
//     time the machine was down.
type sleepClock interface {
	// sleepSince reports how much of the elapsed time since the anchor the machine was not running.
	sleepSince() time.Duration
	// reanchor moves the anchor to now, so one suspend is not counted twice.
	reanchor()
}
