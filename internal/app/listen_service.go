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

var errNilListener = errors.New("nil listener")

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
	StartedAt time.Time
	Packets   uint64
	Matched   uint64
	Actions   map[string]uint64
	LastEvent *EventRecord
}

type ListenService struct {
	factory  PacketListenerFactory
	registry *wol.Registry
	policy   *wol.RoutingPolicy
	ifaces   []wol.IfaceInfo
	dryRun   bool

	startedAt time.Time
	packets   atomic.Uint64
	matched   atomic.Uint64

	mu       sync.Mutex
	actions  map[string]uint64
	lastSeen *EventRecord
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
		registry:  registry,
		policy:    policy,
		ifaces:    ifaces,
		dryRun:    dryRun,
		startedAt: time.Now(),
		actions:   make(map[string]uint64),
	}
}

// Stats returns a snapshot of the counters for the control plane.
func (s *ListenService) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	actions := make(map[string]uint64, len(s.actions))
	maps.Copy(actions, s.actions)

	return Stats{
		StartedAt: s.startedAt,
		Packets:   s.packets.Load(),
		Matched:   s.matched.Load(),
		Actions:   actions,
		LastEvent: s.lastSeen,
	}
}

// Rules returns the loaded routing rules.
func (s *ListenService) Rules() []wol.Rule {
	return s.policy.Rules()
}

// Interfaces returns the interfaces this instance listens for.
func (s *ListenService) Interfaces() []wol.IfaceInfo {
	return s.ifaces
}

// Dispatch triggers a named action outside the packet path (control plane).
func (s *ListenService) Dispatch(ctx context.Context, action wol.Action, ev wol.Event) error {
	if s.dryRun {
		slog.Warn("dry run: manual action not executed", "action", string(action))

		return nil
	}

	if err := s.registry.Dispatch(ctx, action, ev); err != nil {
		return err
	}

	s.recordAction(string(action))

	return nil
}

func (s *ListenService) Run(ctx context.Context) error {
	s.logIfaces()
	s.logRules()

	listeners, err := s.createListeners()
	if err != nil {
		return err
	}
	defer s.closeListeners(listeners)

	pktCh, errCh := s.startListenerGoroutines(ctx, listeners)

	s.eventLoop(ctx, pktCh, errCh)

	return nil
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

func (s *ListenService) logIfaces() {
	for _, iface := range s.ifaces {
		slog.Info("using interface",
			"name", iface.Name,
			"ip", iface.IPv4().String(),
			"mac", iface.MAC.String(),
		)
	}
}

func (s *ListenService) logRules() {
	rules := s.policy.Rules()
	for i, rule := range rules {
		slog.Info("rule",
			"index", i+1,
			"ports", rule.Match.Ports,
			"action", string(rule.Action),
			"dry_run", rule.DryRun,
		)
	}
}

func (s *ListenService) createListeners() ([]PacketListener, error) {
	ports := s.policy.Ports()
	listeners := make([]PacketListener, 0, len(ports))

	for _, p := range ports {
		lis, err := s.factory.Create(p)
		if err != nil {
			for _, l := range listeners {
				_ = l.Close()
			}

			return nil, fmt.Errorf("failed to create listener on port %d: %w", p, err)
		}

		listeners = append(listeners, lis)
	}

	for _, p := range ports {
		slog.Info("listening", "port", p)
	}

	return listeners, nil
}

func (s *ListenService) closeListeners(listeners []PacketListener) {
	for _, l := range listeners {
		_ = l.Close()
	}
}

func (s *ListenService) startListenerGoroutines(ctx context.Context, listeners []PacketListener) (chan packet, chan error) {
	ports := s.policy.Ports()
	pktCh := make(chan packet, packetChannelSize)
	errCh := make(chan error, len(listeners))

	for idx, lis := range listeners {
		go s.listenOnPort(ctx, lis, ports[idx], pktCh, errCh)
	}

	return pktCh, errCh
}

func (s *ListenService) listenOnPort(ctx context.Context, listener PacketListener, port int, pktCh chan packet, errCh chan error) {
	if listener == nil {
		errCh <- errNilListener

		return
	}

	for {
		payload, src, err := listener.ReadPacket(ctx)
		if shouldStop(err) {
			errCh <- err

			return
		}

		if err != nil {
			slog.Warn("read error", "port", port, "error", err)

			continue
		}

		select {
		case pktCh <- packet{payload, src, port}:
		case <-ctx.Done():
			errCh <- ctx.Err()

			return
		}
	}
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

	ev := wol.Event{Payload: pkt.payload, DstPort: pkt.port}
	if pkt.src != nil {
		ev.SrcIP = pkt.src.IP
		ev.SrcPort = pkt.src.Port
	}

	decision, matched := s.policy.Resolve(ev)
	if !matched {
		slog.Info("non-matching packet",
			"src", addrString(pkt.src),
			"port", pkt.port,
			"length", len(pkt.payload),
		)

		return
	}

	s.matched.Add(1)

	logOnly := s.dryRun || decision.DryRun
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
		"trigger", trigger,
	)

	if logOnly {
		return
	}

	dispatchEv := ev
	dispatchEv.Interface = decision.Interface
	dispatchEv.TargetMAC = decision.TargetMAC

	if dispatchErr := s.registry.Dispatch(ctx, decision.Action, dispatchEv); dispatchErr != nil {
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
