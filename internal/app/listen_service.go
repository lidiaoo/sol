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
	// ErrActionInFlight reports that the same action is already running: the duplicate trigger
	// was suppressed rather than run a second time (§19.12.1).
	ErrActionInFlight = errors.New("action suppressed by in-flight guard")
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
	// Inflight counts the triggers refused because the same run was already going (§19.12.1).
	Inflight  uint64
	Actions   map[string]uint64
	LastEvent *EventRecord
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
	inFlight    atomic.Uint64

	mu       sync.Mutex
	actions  map[string]uint64
	lastSeen *EventRecord

	// ifacesSel re-reads the machine's interfaces on demand (§17.2). Nil means the identity
	// set is whatever start-up resolved.
	ifacesSel func() ([]wol.IfaceInfo, error)
	// ifaceRefreshInterval is the background poll period: 0 takes the default, negative
	// disables the poll (tests drive the refresh themselves).
	ifaceRefreshInterval time.Duration
	// lastIfaceRefresh bounds the on-demand refresh, so a flood of noise packets cannot turn
	// into a syscall storm. Unix nanoseconds; 0 means "never refreshed".
	lastIfaceRefresh atomic.Int64

	// rtMu guards the routing state that Reload swaps in. The counters above stay
	// outside it so a reload never waits for an in-flight action.
	rtMu      sync.RWMutex
	registry  *wol.Registry
	policy    *wol.RoutingPolicy
	dryRun    bool
	cooldowns *cooldowns
	limiter   *rateLimiter
	remote    *remoteRunner
	// inflight is not part of the routing snapshot: it holds no configuration, and a reload must
	// not forget which runs are going.
	inflight *inflight
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
		inflight:  newInflight(),
	}
}

const (
	// defaultIfaceRefresh is how often the background poll re-reads the interface list. It
	// covers the machine that has received nothing yet: the on-demand refresh only fires on a
	// packet that misses, so without the poll a dock plugged in after start-up would wait for
	// the first packet it is meant to answer.
	defaultIfaceRefresh = 30 * time.Second
	// ifaceRefreshFloor is the shortest distance between two on-demand refreshes.
	ifaceRefreshFloor = time.Second
)

// WithInterfaceSelector installs the function that re-reads the machine's interfaces, plus the
// background poll period (0 = default, negative = no poll). Without it the identity set stays
// whatever start-up resolved.
func (s *ListenService) WithInterfaceSelector(sel func() ([]wol.IfaceInfo, error), interval time.Duration) *ListenService {
	s.ifacesSel = sel
	s.ifaceRefreshInterval = interval

	return s
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
		Inflight:    s.inFlight.Load(),
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

	release, err := s.allowAction(rt, action, runKey(string(action), ev, ""))
	if err != nil {
		return err
	}

	defer release()

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

	if s.ifacesSel != nil && s.ifaceRefreshInterval >= 0 {
		go s.pollInterfaces(ctx)
	}

	s.eventLoop(ctx, pktCh, errCh)

	return nil
}

// refreshIfaces re-reads the machine's interfaces and swaps them into the routing policy when
// they changed, reporting whether they did. minGap limits how often the on-demand path may do
// this (the poll passes 0: it is slow enough by construction).
//
// The identity set is not a start-up property: a NIC can be down and come up later, and its MAC
// can change when it does (a wireless card that NetworkManager randomizes while down reads as a
// placeholder), a dock can be plugged in, a NIC can be created. The listening sockets bind
// 0.0.0.0, so those packets do arrive - only the match is missing.
func (s *ListenService) refreshIfaces(minGap time.Duration, reason string) bool {
	sel := s.ifacesSel
	if sel == nil {
		return false
	}

	if minGap > 0 {
		now := time.Now().UnixNano()
		last := s.lastIfaceRefresh.Load()

		if last != 0 && now-last < int64(minGap) {
			return false
		}

		// One refresher at a time: the ones that lose the race wait for the next window.
		if !s.lastIfaceRefresh.CompareAndSwap(last, now) {
			return false
		}
	}

	ifaces, err := sel()
	if err != nil {
		slog.Warn("cannot re-read the interfaces", "error", err)

		return false
	}

	rt := s.snapshot()

	changed, err := rt.policy.SetInterfaces(ifaces)
	if err != nil {
		slog.Warn("cannot refresh the interface set", "error", err)

		return false
	}

	old := s.replaceIfaces(ifaces)

	if !changed {
		return false
	}

	added, removed := diffIfaces(old, ifaces)

	slog.Info("interface set changed",
		"reason", reason,
		"added", added,
		"removed", removed,
	)

	return true
}

// replaceIfaces stores the fresh list and returns the one it replaced.
func (s *ListenService) replaceIfaces(ifaces []wol.IfaceInfo) []wol.IfaceInfo {
	s.rtMu.Lock()
	defer s.rtMu.Unlock()

	old := s.ifaces
	s.ifaces = ifaces

	return old
}

// diffIfaces reports the NICs that appeared and those that went away, as name=mac pairs.
func diffIfaces(old, fresh []wol.IfaceInfo) ([]string, []string) {
	added := make([]string, 0, len(fresh))
	removed := make([]string, 0, len(old))

	was := make(map[string]string, len(old))
	for _, iface := range old {
		was[iface.Name] = iface.MAC.String()
	}

	now := make(map[string]string, len(fresh))
	for _, iface := range fresh {
		now[iface.Name] = iface.MAC.String()
	}

	for _, iface := range fresh {
		if mac, ok := was[iface.Name]; !ok || mac != iface.MAC.String() {
			added = append(added, iface.Name+"="+iface.MAC.String())
		}
	}

	for _, iface := range old {
		if _, ok := now[iface.Name]; !ok {
			removed = append(removed, iface.Name+"="+iface.MAC.String())
		}
	}

	return added, removed
}

// pollInterfaces re-reads the interface list on a timer, so a NIC that shows up while nothing is
// being received is picked up before the first packet that needs it.
func (s *ListenService) pollInterfaces(ctx context.Context) {
	interval := s.ifaceRefreshInterval
	if interval <= 0 {
		interval = defaultIfaceRefresh
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshIfaces(0, "periodic poll")
		}
	}
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

	// A miss may simply mean the identity set is stale (a NIC that came up after start-up, a
	// dock that was plugged in). Re-read it at most once a second and try the packet again.
	if !matched && s.refreshIfaces(ifaceRefreshFloor, "unmatched packet") {
		decision, matched = rt.policy.Resolve(ev)
	}

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

	dispatchEv := ev
	dispatchEv.Interface = decision.Interface
	dispatchEv.TargetMAC = decision.TargetMAC

	release, err := s.allowAction(rt, decision.Action, runKey(string(decision.Action), dispatchEv, ""))
	if err != nil {
		// allowAction already counted and logged which guard stopped it.
		return
	}

	defer release()

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

// allowAction applies the guardrails: the per-action cooldown first, the global rate limit next,
// and last the in-flight guard, which needs the key of the run it is about to mark. Every guard
// counts and logs a suppressed run, and the returned error names the one that stopped it; the
// caller must run the action and call the returned release when it is done. A suppressed attempt
// still counts as an attempt for the cooldown window, which is what the packet path has always
// done.
func (s *ListenService) allowAction(rt routingSnapshot, action wol.Action, key string) (func(), error) {
	if remaining, allowed := rt.cooldowns.allow(string(action)); !allowed {
		s.blocked.Add(1)
		slog.Warn("action suppressed by cooldown", "action", string(action), "retry_in", remaining.String())

		return nil, fmt.Errorf("%w: retry in %s", ErrActionSuppressed, remaining)
	}

	if retryIn, allowed := rt.limiter.allow(); !allowed {
		s.blocked.Add(1)
		s.rateLimited.Add(1)
		slog.Warn("action suppressed by rate limit", "action", string(action), "retry_in", retryIn.String())

		return nil, fmt.Errorf("%w: retry in %s", ErrActionRateLimited, retryIn)
	}

	release, started := s.inflight.begin(key)
	if !started {
		s.blocked.Add(1)
		s.inFlight.Add(1)
		slog.Warn("action suppressed: already running", "action", string(action))

		return nil, fmt.Errorf("%w: %s is already running", ErrActionInFlight, action)
	}

	return release, nil
}
