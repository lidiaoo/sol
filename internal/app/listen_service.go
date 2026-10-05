package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"

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

type ListenService struct {
	factory  PacketListenerFactory
	registry *wol.Registry
	policy   *wol.RoutingPolicy
	ifaces   []wol.IfaceInfo
	dryRun   bool
}

func NewListenService(
	factory PacketListenerFactory,
	registry *wol.Registry,
	policy *wol.RoutingPolicy,
	ifaces []wol.IfaceInfo,
	dryRun bool,
) *ListenService {
	return &ListenService{
		factory:  factory,
		registry: registry,
		policy:   policy,
		ifaces:   ifaces,
		dryRun:   dryRun,
	}
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

func (s *ListenService) logIfaces() {
	for _, iface := range s.ifaces {
		log.Printf("Using interface %q: IP=%s, MAC=%s", iface.Name, iface.IPv4(), iface.MAC)
	}
}

func (s *ListenService) logRules() {
	rules := s.policy.Rules()
	for i, rule := range rules {
		log.Printf("Rule %d: ports=%v action=%s", i+1, rule.Match.Ports, rule.Action)
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
		log.Printf("Listening on 0.0.0.0:%d", p)
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
			log.Printf("read error on port %d: %v", port, err)

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

			log.Printf("listener error: %v", err)
		case pkt := <-pktCh:
			s.handlePacket(ctx, pkt)
		}
	}
}

func (s *ListenService) handlePacket(ctx context.Context, pkt packet) {
	ev := wol.Event{Payload: pkt.payload, DstPort: pkt.port}
	if pkt.src != nil {
		ev.SrcIP = pkt.src.IP
		ev.SrcPort = pkt.src.Port
	}

	decision, matched := s.policy.Resolve(ev)
	if !matched {
		log.Printf("Non-matching packet from %s, port=%d, len=%d", pkt.src, pkt.port, len(pkt.payload))

		return
	}

	logOnly := s.dryRun || decision.DryRun
	trigger := ternary(logOnly, "DRY-RUN", string(decision.Action))
	log.Printf("Magic packet match from %s, port=%d, action=%s - triggering %s",
		pkt.src, pkt.port, decision.Action, trigger)

	if logOnly {
		return
	}

	if dispatchErr := s.registry.Dispatch(ctx, decision.Action, ev); dispatchErr != nil {
		log.Printf("%s failed: %v", decision.Action, dispatchErr)
	}
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
