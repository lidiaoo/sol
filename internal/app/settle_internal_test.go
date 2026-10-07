package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

// fakeSleepClock lets a test decide how much of the elapsed time the machine spent not running,
// which is the signal the settle window reacts to. Substituting it keeps these tests independent of
// the platform's clocks - and of whether anyone suspended the machine they run on.
type fakeSleepClock struct{ slept time.Duration }

func (f *fakeSleepClock) sleepSince() time.Duration { return f.slept }
func (f *fakeSleepClock) reanchor()                 { f.slept = 0 }

// at returns a settleState whose clock is a variable, plus a handle on the suspend signal.
func at(now *time.Time) (*settleState, *fakeSleepClock) {
	fake := &fakeSleepClock{}
	st := newSettleState()

	st.now = func() time.Time { return *now }
	st.clock = fake
	st.start = *now

	return st, fake
}

func TestSettleSuppressesRightAfterStart(t *testing.T) {
	t.Parallel()

	now := time.Now()
	guard, _ := at(&now)

	require.Equal(t, 2*time.Minute, guard.remaining(2*time.Minute))

	now = now.Add(90 * time.Second)
	require.Equal(t, 30*time.Second, guard.remaining(2*time.Minute))

	now = now.Add(30 * time.Second)

	require.Zero(t, guard.remaining(2*time.Minute), "the window has to end")
}

func TestSettleIgnoresAnOldProcess(t *testing.T) {
	t.Parallel()

	now := time.Now().Add(-2 * time.Hour)
	guard, _ := at(&now)

	now = time.Now()

	require.Zero(t, guard.remaining(2*time.Minute))
}

func TestSettleDisablesWithZeroWindow(t *testing.T) {
	t.Parallel()

	now := time.Now()
	guard, _ := at(&now)

	require.Zero(t, guard.remaining(0))
}

func TestSettleIgnoresClockNoise(t *testing.T) {
	t.Parallel()

	now := time.Now()
	guard, fake := at(&now)

	// Under the threshold: a drifting wall clock, not a suspend. Nothing may be re-anchored.
	fake.slept = settleDriftThreshold - time.Second

	then := now

	now = now.Add(10 * time.Second)

	require.Zero(t, guard.remaining(time.Second), "the window still ends on schedule")

	require.Equal(t, then, guard.start, "a little drift is not a resume")
}

// A resume is the moment the machine was not running while time passed: the window then runs from
// the moment sol sees the machine is back, which is the copy of the packet that arrives right after
// the resume - the very one that has to be refused.
func TestSettleDetectsAResume(t *testing.T) {
	t.Parallel()

	now := time.Now()
	guard, fake := at(&now)

	// Two hours passed, and the machine spent all of it suspended.
	fake.slept = 2 * time.Hour

	detected := now

	require.Equal(t, 2*time.Minute, guard.remaining(2*time.Minute), "the window restarts on a resume")

	// The reference was re-anchored when the resume was seen, so the same suspend cannot be counted
	// twice and the window cannot be extended by looking at it again.
	require.Equal(t, detected, guard.start, "the reference is re-anchored after a resume")
	require.Zero(t, fake.slept, "the platform clock is re-anchored too")

	now = now.Add(2 * time.Minute)

	require.Zero(t, guard.remaining(2*time.Minute))
	require.Equal(t, detected, guard.start, "re-anchored once, not on every call")
}

func TestListenServiceSettleSuppressesDispatch(t *testing.T) {
	t.Parallel()

	ifaces := testIfaces()
	policy := mustPolicy(t, ifaces, []wol.Rule{ruleFor(8, wol.ActionSleep)})
	executor := &executorMock{}

	svc := NewListenService(&factoryMock{}, testRegistry(executor), policy, ifaces, false).
		WithSettle(2*time.Minute, []wol.Action{wol.ActionSleep})

	pkt := packet{payload: wol.BuildMagicPacket(testMAC()), port: 8}

	svc.handlePacket(context.Background(), pkt)
	svc.handlePacket(context.Background(), pkt)

	stats := svc.Stats()

	require.Zero(t, executor.calls, "the machine just started: nothing may run yet")
	require.Equal(t, uint64(2), stats.Matched)
	require.Equal(t, uint64(2), stats.SettleSkipped)
	require.ErrorIs(t, svc.Dispatch(context.Background(), wol.ActionSleep, wol.Event{}), ErrActionSettle)
	require.Zero(t, executor.calls)
}

func TestListenServiceSettleLeavesOtherActionsAlone(t *testing.T) {
	t.Parallel()

	ifaces := testIfaces()
	policy := mustPolicy(t, ifaces, []wol.Rule{ruleFor(8, wol.ActionNoop)})
	executor := &executorMock{}

	svc := NewListenService(&factoryMock{}, testRegistry(executor), policy, ifaces, false).
		WithSettle(2*time.Minute, []wol.Action{wol.ActionSleep})

	svc.handlePacket(context.Background(), packet{payload: wol.BuildMagicPacket(testMAC()), port: 8})

	require.Equal(t, 1, executor.calls)
	require.Zero(t, svc.Stats().SettleSkipped)
}

func TestListenServiceSettleDisabled(t *testing.T) {
	t.Parallel()

	ifaces := testIfaces()
	policy := mustPolicy(t, ifaces, []wol.Rule{ruleFor(8, wol.ActionSleep)})
	executor := &executorMock{}

	svc := NewListenService(&factoryMock{}, testRegistry(executor), policy, ifaces, false).
		WithSettle(0, nil)

	svc.handlePacket(context.Background(), packet{payload: wol.BuildMagicPacket(testMAC()), port: 8})

	require.Equal(t, 1, executor.calls)
}
