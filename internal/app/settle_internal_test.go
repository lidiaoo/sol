package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

// at returns a settleState whose clock is a variable, plus a pointer to that variable. startWall and
// startMono are anchored on the same instant, except when a test wants to simulate a suspend by
// letting the wall clock run ahead of the monotonic one.
func at(now *time.Time) *settleState {
	st := newSettleState()
	st.now = func() time.Time { return *now }
	st.startWall = now.Round(0)
	st.startMono = *now

	return st
}

func TestSettleSuppressesRightAfterStart(t *testing.T) {
	t.Parallel()

	now := time.Now()
	guard := at(&now)

	require.Equal(t, 2*time.Minute, guard.remaining(2*time.Minute))

	now = now.Add(90 * time.Second)
	require.Equal(t, 30*time.Second, guard.remaining(2*time.Minute))

	now = now.Add(30 * time.Second)

	require.Zero(t, guard.remaining(2*time.Minute), "the window has to end")
}

func TestSettleIgnoresAnOldProcess(t *testing.T) {
	t.Parallel()

	now := time.Now().Add(-2 * time.Hour)
	guard := at(&now)

	now = time.Now()

	require.Zero(t, guard.remaining(2*time.Minute))
}

func TestSettleDisablesWithZeroWindow(t *testing.T) {
	t.Parallel()

	now := time.Now()
	guard := at(&now)

	require.Zero(t, guard.remaining(0))
}

// A suspend shows up as the wall clock running ahead of the monotonic clock: CLOCK_MONOTONIC stops
// while the machine is down, the wall clock does not. The window then runs from the moment sol sees
// the machine is back, which is the copy of the packet that arrived right after the resume.
func TestSettleDetectsAResume(t *testing.T) {
	t.Parallel()

	now := time.Now()
	guard := at(&now)

	// Two hours of wall time passed, but only a second of monotonic time: the machine was suspended.
	guard.startWall = now.Add(-2 * time.Hour).Round(0)
	guard.startMono = now.Add(-time.Second)

	detected := now

	require.Equal(t, 2*time.Minute, guard.remaining(2*time.Minute))

	// The reference was re-anchored when the resume was seen, so the same suspend cannot be counted
	// twice and the window cannot be extended by looking at it again.
	require.Equal(t, detected.Round(0), guard.startWall, "the reference is re-anchored after a resume")

	now = now.Add(2 * time.Minute)

	require.Zero(t, guard.remaining(2*time.Minute))
	require.Equal(t, detected.Round(0), guard.startWall, "re-anchored once, not on every call")
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
