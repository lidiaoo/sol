package app

import (
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

var (
	// ErrReloadIncomplete reports a reload missing the mandatory rule set or registry.
	ErrReloadIncomplete = errors.New("reload requires both a routing policy and an action registry")
	// ErrReloadBind reports a reload whose new ports could not be bound. Nothing is
	// changed: the running listener keeps serving the set it already had.
	ErrReloadBind = errors.New("reload rejected: could not bind the new listening ports")
)

// ReloadOptions is a rebuilt configuration for a running listener.
type ReloadOptions struct {
	Policy      *wol.RoutingPolicy
	Registry    *wol.Registry
	DryRun      bool
	Cooldown    time.Duration
	Cooldowns   map[string]time.Duration
	RateLimit   float64
	RateBurst   int
	Commands    map[string]wol.RemoteCommand
	RemotePorts []int
	RemoteKey   []byte
	// Ifaces is the interface set the fresh configuration resolved. Sockets listen on every
	// address, so nothing is rebound here -- but MAC resolution, the status view and the audit
	// log should describe the machine as it is now.
	Ifaces []wol.IfaceInfo
	// RemoteWindow carries the command channel's replay window across a reload: dropping it
	// here would quietly undo §21.3 on the first SIGHUP. The reporter is not part of the
	// options -- it belongs to the service, so the counters survive.
	RemoteWindow time.Duration
}

// Reload swaps the routing state. It is atomic: a packet already being handled keeps the snapshot
// it started with, everything after the call routes with the new rules. The port set may change:
// the ports the new configuration adds are bound first, and only then is anything closed, so a
// reload that cannot bind one of them leaves the running listener untouched.
func (s *ListenService) Reload(opts ReloadOptions) error {
	if opts.Policy == nil || opts.Registry == nil {
		return ErrReloadIncomplete
	}

	s.rtMu.Lock()
	defer s.rtMu.Unlock()

	ports := opts.Policy.Ports()

	added, err := s.listeners.rebind(ports)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrReloadBind, err)
	}

	// Keep the running guard when the windows did not change, so a reload cannot be
	// used to clear a cooldown by accident.
	if !s.cooldowns.matches(opts.Cooldown, opts.Cooldowns) {
		s.cooldowns = newCooldowns(opts.Cooldown, opts.Cooldowns)
	}

	// Same for the token bucket: an unchanged limit keeps its drained tokens.
	if !s.limiter.matches(opts.RateLimit, opts.RateBurst) {
		s.limiter = newRateLimiter(opts.RateLimit, opts.RateBurst)
	}

	s.policy = opts.Policy
	s.registry = opts.Registry
	s.dryRun = opts.DryRun

	if len(opts.Ifaces) > 0 {
		s.ifaces = opts.Ifaces
	}

	s.remote = newRemoteRunner(RemoteSettings{
		Commands: opts.Commands,
		Ports:    opts.RemotePorts,
		Key:      opts.RemoteKey,
		Window:   opts.RemoteWindow,
		OnReject: s.onReject,
	})

	// Only now can the new ports start reading: their packets route with the state above.
	s.listeners.commit(added, ports)

	return nil
}

// matches reports whether the guard already runs with these windows.
func (c *cooldowns) matches(global time.Duration, perAction map[string]time.Duration) bool {
	return c.global == global && maps.Equal(c.perAction, perAction)
}
