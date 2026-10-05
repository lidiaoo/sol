package app

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/bavix/sol/internal/domain/wol"
)

var (
	// ErrReloadIncomplete reports a reload missing the mandatory rule set or registry.
	ErrReloadIncomplete = errors.New("reload requires both a routing policy and an action registry")
	// ErrReloadRestartRequired reports a reload that changes the bound port set. The
	// sockets are created at startup, and a listener that keeps serving the old ports
	// while the file says something else would be a lie.
	ErrReloadRestartRequired = errors.New("reload rejected: the listening port set changed, restart sol")
)

// ReloadOptions is a rebuilt configuration for a running listener.
type ReloadOptions struct {
	Policy      *wol.RoutingPolicy
	Registry    *wol.Registry
	DryRun      bool
	Cooldown    time.Duration
	Cooldowns   map[string]time.Duration
	Commands    map[string]wol.RemoteCommand
	RemotePorts []int
	RemoteKey   []byte
}

// Reload swaps the routing state. It is atomic: a packet already being handled keeps
// the snapshot it started with, everything after the call routes with the new rules.
// Interfaces may change freely -- the sockets listen on every address -- but a change
// of the port set is refused, because it cannot be applied without rebinding.
func (s *ListenService) Reload(opts ReloadOptions) error {
	if opts.Policy == nil || opts.Registry == nil {
		return ErrReloadIncomplete
	}

	s.rtMu.Lock()
	defer s.rtMu.Unlock()

	if !slices.Equal(s.policy.Ports(), opts.Policy.Ports()) {
		return fmt.Errorf("%w: %v -> %v", ErrReloadRestartRequired, s.policy.Ports(), opts.Policy.Ports())
	}

	// Keep the running guard when the windows did not change, so a reload cannot be
	// used to clear a cooldown by accident.
	if !s.cooldowns.matches(opts.Cooldown, opts.Cooldowns) {
		s.cooldowns = newCooldowns(opts.Cooldown, opts.Cooldowns)
	}

	s.policy = opts.Policy
	s.registry = opts.Registry
	s.dryRun = opts.DryRun
	s.remote = newRemoteRunner(opts.Commands, opts.RemotePorts, opts.RemoteKey)

	return nil
}

// matches reports whether the guard already runs with these windows.
func (c *cooldowns) matches(global time.Duration, perAction map[string]time.Duration) bool {
	return c.global == global && maps.Equal(c.perAction, perAction)
}
