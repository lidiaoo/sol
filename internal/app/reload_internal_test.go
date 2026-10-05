package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

func reloadFixture(t *testing.T, ports ...int) (*ListenService, []wol.IfaceInfo) {
	t.Helper()

	ifaces := testIfaces()

	rules := make([]wol.Rule, 0, len(ports))
	for _, port := range ports {
		rules = append(rules, ruleFor(port, wol.ActionNoop))
	}

	service := NewListenService(
		&factoryMock{},
		testRegistry(&executorMock{}),
		mustPolicy(t, ifaces, rules),
		ifaces,
		false,
	)

	return service, ifaces
}

func TestReloadSwapsTheRoutingState(t *testing.T) {
	t.Parallel()

	service, ifaces := reloadFixture(t, 10041)

	swapped := mustPolicy(t, ifaces, []wol.Rule{ruleFor(10041, wol.ActionShutdown)})

	require.NoError(t, service.Reload(ReloadOptions{
		Policy:   swapped,
		Registry: testRegistry(&executorMock{}),
	}))

	require.Equal(t, wol.ActionShutdown, service.Rules()[0].Action)
}

func TestReloadAcceptsAChangedPortSet(t *testing.T) {
	t.Parallel()

	service, ifaces := reloadFixture(t, 10041)

	// The port set may move: the sockets are rebound by the listener set (see
	// TestReloadBindsAndClosesPorts). Without a running listener this is just the state swap.
	grown := mustPolicy(t, ifaces, []wol.Rule{ruleFor(10041, wol.ActionNoop), ruleFor(10042, wol.ActionNoop)})

	require.NoError(t, service.Reload(ReloadOptions{Policy: grown, Registry: testRegistry(&executorMock{})}))
	require.Len(t, service.Rules(), 2)
}

func TestReloadRequiresPolicyAndRegistry(t *testing.T) {
	t.Parallel()

	service, ifaces := reloadFixture(t, 10041)

	require.ErrorIs(t, service.Reload(ReloadOptions{}), ErrReloadIncomplete)
	require.ErrorIs(t, service.Reload(ReloadOptions{
		Policy: mustPolicy(t, ifaces, []wol.Rule{ruleFor(10041, wol.ActionNoop)}),
	}), ErrReloadIncomplete)
}

func TestReloadKeepsTheRunningCooldownGuard(t *testing.T) {
	t.Parallel()

	service, ifaces := reloadFixture(t, 10041)
	service.WithCooldowns(time.Minute, nil)

	require.NoError(t, service.allowAction(service.snapshot(), wol.ActionNoop))

	next := mustPolicy(t, ifaces, []wol.Rule{ruleFor(10041, wol.ActionNoop)})

	// unchanged windows: the window started above still applies, so a reload cannot be
	// used to refresh the guard
	require.NoError(t, service.Reload(ReloadOptions{
		Policy:   next,
		Registry: testRegistry(&executorMock{}),
		Cooldown: time.Minute,
	}))
	require.ErrorIs(t, service.allowAction(service.snapshot(), wol.ActionNoop), ErrActionSuppressed)

	// changed windows: a new guard starts
	require.NoError(t, service.Reload(ReloadOptions{
		Policy:   next,
		Registry: testRegistry(&executorMock{}),
		Cooldown: 2 * time.Minute,
	}))
	require.NoError(t, service.allowAction(service.snapshot(), wol.ActionNoop))
}

// TestReloadKeepsTheRunningRateLimitGuard is the same guarantee for the token bucket: an
// unchanged limit must not hand back the tokens a storm already spent.
func TestReloadKeepsTheRunningRateLimitGuard(t *testing.T) {
	t.Parallel()

	service, ifaces := reloadFixture(t, 10041)
	service.WithRateLimit(1, 1)

	require.NoError(t, service.allowAction(service.snapshot(), wol.ActionNoop))
	require.ErrorIs(t, service.allowAction(service.snapshot(), wol.ActionNoop), ErrActionRateLimited,
		"the bucket holds a single token")

	next := mustPolicy(t, ifaces, []wol.Rule{ruleFor(10041, wol.ActionNoop)})

	// unchanged limit: still drained
	require.NoError(t, service.Reload(ReloadOptions{
		Policy:    next,
		Registry:  testRegistry(&executorMock{}),
		RateLimit: 1,
		RateBurst: 1,
	}))
	require.ErrorIs(t, service.allowAction(service.snapshot(), wol.ActionNoop), ErrActionRateLimited)

	// changed limit: the operator asked for a different guard, so it starts full
	require.NoError(t, service.Reload(ReloadOptions{
		Policy:    next,
		Registry:  testRegistry(&executorMock{}),
		RateLimit: 1,
		RateBurst: 5,
	}))
	require.NoError(t, service.allowAction(service.snapshot(), wol.ActionNoop))
}
