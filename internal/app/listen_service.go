package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bavix/sol/internal/domain/wol"
)

var (
	errNilListener = errors.New("nil listener")
	// ErrActionSuppressed reports that an action was rate-limited by its cooldown.
	ErrActionSuppressed = errors.New("action suppressed by cooldown")

	// ErrActionRateLimited reports that an action was dropped by the global rate limit.
	ErrActionRateLimited = errors.New("action suppressed by rate limit")
)

const packetChannelSize = 2

type packet struct {
	payload []byte
	src     *net.UDPAddr
	port    int
}

// InterfaceResolver resolves local interfaces, either by explicit name or automatically.
type InterfaceResolver interface {
	Resolve(name string) (net.IP, net.HardwareAddr, error)
	Select(names []string) ([]wol.IfaceInfo, error)
}

type PacketListener interface {
	ReadPacket(ctx context.Context) ([]byte, *net.UDPAddr, error)
	Close() error
}

type PacketListenerFactory interface {
	Create(port int) (PacketListener, error)
}

// EventRecord describes the most recently matched packet.
type EventRecord struct {
	Time      time.Time
	Src       string
	Port      int
	Interface string
	TargetMAC string
	Action    string
	DryRun    bool
}

// Stats is a snapshot of the listener counters, used by the control plane.
type Stats struct {
	StartedAt   time.Time
	Packets     uint64
	Matched     uint64
	Suppressed  uint64
	RateLimited uint64
	Actions     map[string]uint64
	LastEvent   *EventRecord
}

type ListenService struct {
	factory   PacketListenerFactory
	ifaces    []wol.IfaceInfo
	listeners *listenerSet

	startedAt   time.Time
	packets     atomic.Uint64
	matched     atomic.Uint64
	blocked     atomic.Uint64
	rateLimited atomic.Uint64

	mu       sync.Mutex
	actions  map[string]uint64
	lastSeen *EventRecord

	// rtMu guards the routing state that Reload swaps in. The counters above stay
	// outside it so a reload never waits for an in-flight action.
	rtMu      sync.RWMutex
	registry  *wol.Registry
	policy    *wol.RoutingPolicy
	dryRun    bool
	cooldowns *cooldowns
	limiter   *rateLimiter
	remote    *remoteRunner
	// onReject reports a refused remote segment to the same place the policy reports refused
	// packets (§19.16). It lives on the service so a reload keeps the counters.
	onReject func(reason string)
	rawShell *rawShellRunner
}

// routingSnapshot is the state a single packet is routed with. It is taken once per
// packet so that a concurrent reload cannot split one decision across two rule sets
// (matching the old rules, dispatching through the new registry).
type routingSnapshot struct {
	policy    *wol.RoutingPolicy
	ifaces    []wol.IfaceInfo
	registry  *wol.Registry
	cooldowns *cooldowns
	limiter   *rateLimiter
	remote    *remoteRunner
	rawShell  *rawShellRunner
	dryRun    bool
}

func NewListenService(
	factory PacketListenerFactory,
	registry *wol.Registry,
	policy *wol.RoutingPolicy,
	ifaces []wol.IfaceInfo,
	dryRun bool,
) *ListenService {
	return &ListenService{
		factory:   factory,
		listeners: newListenerSet(factory),
		registry:  registry,
		policy:    policy,
		ifaces:    ifaces,
		dryRun:    dryRun,
		startedAt: time.Now(),
		actions:   make(map[string]uint64),
		cooldowns: newCooldowns(0, nil),
		limiter:   newRateLimiter(0, 0),
	}
}

// WithCooldowns installs the per-action execution windows; zero disables the guard.
func (s *ListenService) WithCooldowns(global time.Duration, perAction map[string]time.Duration) *ListenService {
	s.rtMu.Lock()
	defer s.rtMu.Unlock()

	s.cooldowns = newCooldowns(global, perAction)

	return s
}

// WithRateLimit installs the global token bucket; a non-positive rate disables it.
func (s *ListenService) WithRateLimit(rate float64, burst int) *ListenService {
	s.rtMu.Lock()
	defer s.rtMu.Unlock()

	s.limiter = newRateLimiter(rate, burst)

	return s
}

// WithRemoteCommands enables the whitelisted remote command channel (§21). Without
// ports the channel stays disabled.
func (s *ListenService) WithRemoteCommands(settings RemoteSettings) *ListenService {
	s.rtMu.Lock()
	defer s.rtMu.Unlock()

	s.onReject = settings.OnReject
	s.remote = newRemoteRunner(settings)

	return s
}

// WithRawShell enables the raw shell transport of §21.6 from its resolved settings. It is the
// only path in sol that runs a shell, so it stays off unless the operator asked for it, and its
// ports are dedicated to it alone.
func (s *ListenService) WithRawShell(settings RawShellSettings) *ListenService {
	s.rtMu.Lock()
	defer s.rtMu.Unlock()

	if settings.OnReject != nil {
		s.onReject = settings.OnReject
	}

	s.rawShell = newRawShellRunner(settings.Enabled, settings.Ports, settings.Key,
		settings.SrcNets, settings.Allowlist, settings.Exec, settings.Window, settings.OnReject)

	return s
}

// RunRemoteCommand invokes a whitelisted remote command (authenticated HTTP transport).
// It reuses the manual-trigger path, so dry-run, cooldowns and audit logging apply.
func (s *ListenService) RunRemoteCommand(ctx context.Context, id string, args map[string]string) error {
	rt := s.snapshot()

	cmd, err := rt.remote.manual(id, args)
	if err != nil {
		return err
	}

	return s.Dispatch(ctx, cmd.Action(), wol.Event{Args: args})
}

// Stats returns a snapshot of the counters for the control plane.
func (s *ListenService) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	actions := make(map[string]uint64, len(s.actions))
	maps.Copy(actions, s.actions)

	return Stats{
		StartedAt:   s.startedAt,
		Packets:     s.packets.Load(),
		Matched:     s.matched.Load(),
		Suppressed:  s.blocked.Load(),
		RateLimited: s.rateLimited.Load(),
		Actions:     actions,
		LastEvent:   s.lastSeen,
	}
}

// RateLimit returns the live global rate limit: actions per second and bucket size. A zero
// rate means the guard is off.
func (s *ListenService) RateLimit() (float64, int) {
	return s.snapshot().limiter.config()
}

// Rules returns the loaded routing rules.
func (s *ListenService) Rules() []wol.Rule {
	return s.snapshot().policy.Rules()
}

// Interfaces returns the interfaces this instance listens for.
func (s *ListenService) Interfaces() []wol.IfaceInfo {
	return s.snapshot().ifaces
}

// Dispatch triggers a named action outside the packet path (control plane).
func (s *ListenService) Dispatch(ctx context.Context, action wol.Action, ev wol.Event) error {
	rt := s.snapshot()

	if rt.dryRun {
		slog.Warn("dry run: manual action not executed", "action", string(action))

		return nil
	}

	if err := s.allowAction(rt, action); err != nil {
		return err
	}

	if err := rt.registry.Dispatch(ctx, action, ev); err != nil {
		return err
	}

	s.recordAction(string(action))

	return nil
}

func (s *ListenService) Run(ctx context.Context) error {
	rt := s.snapshot()

	s.logIfaces(rt)
	s.logRules(rt)
	s.logCooldowns(rt)
	s.logRateLimit(rt)

	pktCh := make(chan packet, packetChannelSize)
	ports := rt.policy.Ports()
	errCh := make(chan error, len(ports))

	if err := s.listeners.configure(ctx, pktCh, errCh, ports); err != nil {
		return err
	}

	defer s.listeners.closeAll()

	s.eventLoop(ctx, pktCh, errCh)

	return nil
}

// snapshot reads the current routing state. It sits here, after the exported
// methods, so that a reload only ever swaps the whole set at once.
func (s *ListenService) snapshot() routingSnapshot {
	s.rtMu.RLock()
	defer s.rtMu.RUnlock()

	return routingSnapshot{
		policy:    s.policy,
		registry:  s.registry,
		cooldowns: s.cooldowns,
		limiter:   s.limiter,
		remote:    s.remote,
		rawShell:  s.rawShell,
		ifaces:    s.ifaces,
		dryRun:    s.dryRun,
	}
}

func (s *ListenService) recordAction(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.actions[name]++
}

func (s *ListenService) recordEvent(rec EventRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastSeen = &rec
}

func (s *ListenService) logIfaces(rt routingSnapshot) {
	for _, iface := range rt.ifaces {
		slog.Info("using interface",
			"name", iface.Name,
			"ip", iface.IPv4().String(),
			"mac", iface.MAC.String(),
		)
	}
}

func (s *ListenService) logRules(rt routingSnapshot) {
	rules := rt.policy.Rules()
	for i, rule := range rules {
		slog.Info("rule",
			"index", i+1,
			"ports", rule.Match.Ports,
			"action", string(rule.Action),
			"dry_run", rule.DryRun,
		)
	}
}

// logCooldowns reports the configured execution windows at startup.
func (s *ListenService) logCooldowns(rt routingSnapshot) {
	if rt.cooldowns.global > 0 {
		slog.Info("action cooldown", "scope", "default", "window", rt.cooldowns.global.String())
	}

	for action, window := range rt.cooldowns.perAction {
		slog.Info("action cooldown", "action", action, "window", window.String())
	}
}

// logRateLimit reports the global token bucket at startup.
func (s *ListenService) logRateLimit(rt routingSnapshot) {
	rate, burst := rt.limiter.config()
	if rate <= 0 {
		return
	}

	slog.Info("global rate limit", "actions_per_second", rate, "burst", burst)
}

func (s *ListenService) eventLoop(ctx context.Context, pktCh chan packet, errCh chan error) {
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errCh:
			if shouldStop(err) {
				return
			}

			slog.Warn("listener error", "error", err)
		case pkt := <-pktCh:
			s.handlePacket(ctx, pkt)
		}
	}
}

func (s *ListenService) handlePacket(ctx context.Context, pkt packet) {
	s.packets.Add(1)

	rt := s.snapshot()

	ev := wol.Event{Payload: pkt.payload, DstPort: pkt.port}
	if pkt.src != nil {
		ev.SrcIP = pkt.src.IP
		ev.SrcPort = pkt.src.Port
	}

	if rt.rawShell.accepts(pkt.port) && s.handleRawShell(ctx, rt, pkt, ev) {
		return
	}

	if rt.remote.accepts(pkt.port) && s.handleRemote(ctx, rt, pkt, ev) {
		return
	}

	decision, matched := rt.policy.Resolve(ev)
	if !matched {
		slog.Info("non-matching packet",
			"src", addrString(pkt.src),
			"port", pkt.port,
			"length", len(pkt.payload),
		)

		return
	}

	s.matched.Add(1)
	s.runDecision(ctx, rt, pkt, ev, decision)
}

// runDecision logs the match and triggers the action, honouring dry-run and cooldowns.
func (s *ListenService) runDecision(ctx context.Context, rt routingSnapshot, pkt packet, ev wol.Event, decision wol.Decision) {
	logOnly := rt.dryRun || decision.DryRun
	trigger := ternary(logOnly, "DRY-RUN", string(decision.Action))

	s.recordEvent(EventRecord{
		Time:      time.Now(),
		Src:       addrString(pkt.src),
		Port:      pkt.port,
		Interface: decision.Interface,
		TargetMAC: decision.TargetMAC.String(),
		Action:    string(decision.Action),
		DryRun:    logOnly,
	})

	slog.Info("magic packet matched",
		"src", addrString(pkt.src),
		"port", pkt.port,
		"interface", decision.Interface,
		"target_mac", decision.TargetMAC.String(),
		"action", string(decision.Action),
		"authenticated", decision.Authenticated,
		"trigger", trigger,
	)

	if logOnly {
		return
	}

	if err := s.allowAction(rt, decision.Action); err != nil {
		// allowAction already counted and logged which guard stopped it.
		return
	}

	dispatchEv := ev
	dispatchEv.Interface = decision.Interface
	dispatchEv.TargetMAC = decision.TargetMAC

	if dispatchErr := rt.registry.Dispatch(ctx, decision.Action, dispatchEv); dispatchErr != nil {
		slog.Error("action failed", "action", string(decision.Action), "error", dispatchErr)

		return
	}

	s.recordAction(string(decision.Action))
}

func addrString(addr *net.UDPAddr) string {
	if addr == nil {
		return ""
	}

	return addr.String()
}

func shouldStop(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func ternary(cond bool, a string, b string) string {
	if cond {
		return a
	}

	return b
}

// handleRemote consumes a remote command packet (§21) and reports whether the packet
// was handled. A magic packet carrying a command segment is never routed to the rules,
// so a malformed or unknown command cannot accidentally trigger a destructive action.
func (s *ListenService) handleRemote(ctx context.Context, rt routingSnapshot, pkt packet, ev wol.Event) bool {
	parsed, ok := rt.policy.ParsePacket(pkt.payload)
	if !ok || len(parsed.Content) == 0 {
		return false
	}

	prefix := pkt.payload[:len(pkt.payload)-len(parsed.Content)]

	cmd, args, err := rt.remote.resolve(prefix, parsed.Content)
	if err != nil {
		slog.Warn("remote command rejected",
			"src", addrString(pkt.src),
			"port", pkt.port,
			"error", err,
		)

		return true
	}

	ev.Args = args
	ev.Interface = rt.policy.InterfaceForMAC(parsed.MAC)

	s.matched.Add(1)
	s.runDecision(ctx, rt, pkt, ev, wol.Decision{
		Action:    cmd.Action(),
		Interface: ev.Interface,
		TargetMAC: parsed.MAC,
	})

	return true
}

// allowAction applies the guardrails: the per-action cooldown first, then the global rate
// limit. Both count and log a suppressed run, and the returned error names the guard that
// stopped it. A suppressed attempt still counts as an attempt for the cooldown window, which
// is what the packet path has always done.
func (s *ListenService) allowAction(rt routingSnapshot, action wol.Action) error {
	if remaining, allowed := rt.cooldowns.allow(string(action)); !allowed {
		s.blocked.Add(1)
		slog.Warn("action suppressed by cooldown", "action", string(action), "retry_in", remaining.String())

		return fmt.Errorf("%w: retry in %s", ErrActionSuppressed, remaining)
	}

	if retryIn, allowed := rt.limiter.allow(); !allowed {
		s.blocked.Add(1)
		s.rateLimited.Add(1)
		slog.Warn("action suppressed by rate limit", "action", string(action), "retry_in", retryIn.String())

		return fmt.Errorf("%w: retry in %s", ErrActionRateLimited, retryIn)
	}

	return nil
}
