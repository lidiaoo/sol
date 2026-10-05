package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

func allowed(guard *cooldowns, action string) bool {
	_, ok := guard.allow(action)

	return ok
}

func TestCooldownsPerActionWindows(t *testing.T) {
	t.Parallel()

	guard := newCooldowns(time.Minute, map[string]time.Duration{"power.shutdown": 5 * time.Second})

	now := time.Now()
	guard.now = func() time.Time { return now }

	require.True(t, allowed(guard, "power.shutdown"))

	remaining, ok := guard.allow("power.shutdown")
	require.False(t, ok)
	require.Equal(t, 5*time.Second, remaining)

	require.True(t, allowed(guard, "noop"))

	remaining, ok = guard.allow("noop")
	require.False(t, ok)
	require.Equal(t, time.Minute, remaining)

	now = now.Add(5 * time.Second)

	require.True(t, allowed(guard, "power.shutdown"))
}

func TestCooldownsDisabledByDefault(t *testing.T) {
	t.Parallel()

	guard := newCooldowns(0, nil)

	for range 3 {
		require.True(t, allowed(guard, "noop"))
	}
}

func TestListenServiceCooldownSuppressesDispatch(t *testing.T) {
	t.Parallel()

	ifaces := testIfaces()
	policy := mustPolicy(t, ifaces, []wol.Rule{ruleFor(8, wol.ActionShutdown)})
	executor := &executorMock{}

	svc := NewListenService(&factoryMock{}, testRegistry(executor), policy, ifaces, false).
		WithCooldowns(0, map[string]time.Duration{"power.shutdown": time.Minute})

	pkt := packet{payload: wol.BuildMagicPacket(testMAC()), port: 8}

	svc.handlePacket(context.Background(), pkt)
	svc.handlePacket(context.Background(), pkt)

	stats := svc.Stats()

	require.Equal(t, 1, executor.calls)
	require.Equal(t, uint64(2), stats.Matched)
	require.Equal(t, uint64(1), stats.Suppressed)
	require.Equal(t, uint64(1), stats.Actions["power.shutdown"])

	err := svc.Dispatch(context.Background(), wol.ActionShutdown, wol.Event{})
	require.ErrorIs(t, err, ErrActionSuppressed)
	require.Equal(t, 1, executor.calls)
}
