package app

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
)

// listenerSet owns the UDP sockets, one per port, together with the goroutines reading them. A
// reload may move ports: the new ones are bound before the old ones are closed, so a bind that
// fails leaves the running listener exactly as it was.
type listenerSet struct {
	factory PacketListenerFactory

	mu   sync.Mutex
	open map[int]PacketListener
	//nolint:containedctx // The set outlives every reader it starts: it is the service's own
	// lifetime context, and keeping it here is what lets a reload attach a reader to a port it
	// has just bound.
	ctx   context.Context
	pktCh chan packet
	errCh chan error
}

func newListenerSet(factory PacketListenerFactory) *listenerSet {
	return &listenerSet{factory: factory, open: make(map[int]PacketListener)}
}

// configure opens the port set and starts reading it. Run calls it once, before the event loop;
// from then on rebind/commit move the set.
func (l *listenerSet) configure(ctx context.Context, pktCh chan packet, errCh chan error, ports []int) error {
	l.mu.Lock()
	l.ctx, l.pktCh, l.errCh = ctx, pktCh, errCh
	l.mu.Unlock()

	added, err := l.rebind(ports)
	if err != nil {
		return err
	}

	l.commit(added, ports)

	return nil
}

// rebind binds the ports that are not open yet and returns them, without touching anything that
// already runs. The caller either commits the result after swapping its routing state, or drops
// it -- which is what makes a reload that cannot apply a no-op instead of an outage.
func (l *listenerSet) rebind(ports []int) (map[int]PacketListener, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Before Run there are no sockets at all: the caller's state swap is all a reload is, and
	// Run binds whatever the policy holds by then.
	if l.ctx == nil {
		return map[int]PacketListener{}, nil
	}

	added := make(map[int]PacketListener, len(ports))

	for _, port := range ports {
		if _, running := l.open[port]; running {
			continue
		}

		lis, err := l.factory.Create(port)
		if err != nil {
			for _, opened := range added {
				_ = opened.Close()
			}

			return nil, errPort(port, err)
		}

		added[port] = lis
	}

	return added, nil
}

// commit installs the bound ports, starts their readers and closes the ports that left the set.
func (l *listenerSet) commit(added map[int]PacketListener, ports []int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ctx, pktCh, errCh := l.ctx, l.pktCh, l.errCh

	for port, lis := range added {
		l.open[port] = lis

		go readPackets(ctx, pktCh, errCh, lis, port)

		slog.Info("listening", "port", port)
	}

	for port, lis := range l.open {
		if slices.Contains(ports, port) {
			continue
		}

		_ = lis.Close()

		delete(l.open, port)

		slog.Info("stopped listening", "port", port)
	}
}

// closeAll closes every socket; Run defers it.
func (l *listenerSet) closeAll() {
	l.mu.Lock()
	defer l.mu.Unlock()

	for port, lis := range l.open {
		_ = lis.Close()

		delete(l.open, port)

		slog.Debug("closed listener", "port", port)
	}
}

// ports lists the open ports, sorted, for the status view.
func (l *listenerSet) ports() []int {
	l.mu.Lock()
	defer l.mu.Unlock()

	ports := make([]int, 0, len(l.open))

	for port := range l.open {
		ports = append(ports, port)
	}

	slices.Sort(ports)

	return ports
}

// errPort names the port a bind failure was for.
func errPort(port int, err error) error {
	return fmt.Errorf("failed to create listener on port %d: %w", port, err)
}

func readPackets(ctx context.Context, pktCh chan packet, errCh chan error, listener PacketListener, port int) {
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
