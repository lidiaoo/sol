//go:build windows

package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWindowsSleepClockDoesNotDriftWhileAwake is the anti-false-positive test. While the machine is
// awake the two counters have to tick together: if they did not, every packet would look like it
// arrived right after a resume and nothing would ever run.
func TestWindowsSleepClockDoesNotDriftWhileAwake(t *testing.T) {
	t.Parallel()

	c := newSleepClock()

	time.Sleep(300 * time.Millisecond)

	require.Less(t, c.sleepSince(), 50*time.Millisecond,
		"GetTickCount64 and QueryUnbiasedInterruptTime must agree while the machine is awake")
}

// TestWindowsSleepClockCountersAdvance pins that both calls actually work here. A counter that
// silently returned zero would make sleepSince() negative and hide every resume.
func TestWindowsSleepClockCountersAdvance(t *testing.T) {
	t.Parallel()

	ticks, unbiased := tickCount64(), unbiasedInterruptTime()

	time.Sleep(200 * time.Millisecond)

	require.Greater(t, tickCount64(), ticks, "GetTickCount64 has to advance")
	require.Greater(t, unbiasedInterruptTime(), unbiased, "QueryUnbiasedInterruptTime has to advance")
}
