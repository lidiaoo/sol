package app

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

// errPortTaken stands in for the kernel's "address already in use".
var errPortTaken = errors.New("bind udp: address already in use")

// blockingListener reads nothing until the context is done: it keeps a service running while a
// test reloads it.
type blockingListener struct {
	mu      sync.Mutex
	closed  bool
	release chan struct{}
}

func newBlockingListener() *blockingListener {
	return &blockingListener{release: make(chan struct{})}
}

func (l *blockingListener) ReadPacket(ctx context.Context) ([]byte, *net.UDPAddr, error) {
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-l.release:
		return nil, nil, context.Canceled
	}
}

func (l *blockingListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.closed = true

	return nil
}

func (l *blockingListener) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.closed
}

// listeningFactory records which ports were bound and handed out, and can refuse one of them the
// way the kernel does when a port is taken.
type listeningFactory struct {
	mu      sync.Mutex
	refuse  int
	opened  map[int]*blockingListener
	order   []int
	creates int
}

func newListeningFactory(refuse int) *listeningFactory {
	return &listeningFactory{refuse: refuse, opened: make(map[int]*blockingListener)}
}

func (f *listeningFactory) Create(port int) (PacketListener, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.creates++

	if port == f.refuse {
		return nil, errPortTaken
	}

	lis := newBlockingListener()
	f.opened[port] = lis
	f.order = append(f.order, port)

	return lis, nil
}

func (f *listeningFactory) ports() []int {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]int, 0, len(f.opened))
	for port := range f.opened {
		out = append(out, port)
	}

	return out
}

func (f *listeningFactory) listener(port int) *blockingListener {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.opened[port]
}

// runInBackground starts the service and returns a stop function that waits for it to return.
func runInBackground(t *testing.T, service *ListenService) (context.CancelFunc, chan struct{}) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = service.Run(ctx)
	}()

	return cancel, done
}

func reloadService(t *testing.T, factory PacketListenerFactory, ports ...int) (*ListenService, []wol.IfaceInfo) {
	t.Helper()

	ifaces := testIfaces()
	rules := make([]wol.Rule, 0, len(ports))

	for _, port := range ports {
		rules = append(rules, ruleFor(port, wol.ActionNoop))
	}

	service := NewListenService(factory, testRegistry(&executorMock{}), mustPolicy(t, ifaces, rules), ifaces, false)

	return service, ifaces
}

func TestReloadBindsAndClosesPorts(t *testing.T) {
	t.Parallel()

	factory := newListeningFactory(0)
	service, ifaces := reloadService(t, factory, 10041)

	stop, done := runInBackground(t, service)

	defer func() {
		stop()
		<-done
	}()

	require.Eventually(t, func() bool { return len(factory.ports()) == 1 }, time.Second, 5*time.Millisecond)
	require.Equal(t, []int{10041}, service.listeners.ports())

	// Grow the set: the new port is bound, the running one is untouched.
	grown := mustPolicy(t, ifaces, []wol.Rule{ruleFor(10041, wol.ActionNoop), ruleFor(10042, wol.ActionNoop)})
	require.NoError(t, service.Reload(ReloadOptions{Policy: grown, Registry: testRegistry(&executorMock{})}))

	require.Eventually(t, func() bool { return len(service.listeners.ports()) == 2 }, time.Second, 5*time.Millisecond)
	require.Contains(t, service.listeners.ports(), 10042)
	require.False(t, factory.listener(10041).isClosed(), "the port that stayed keeps its socket")

	// Move it back: the port that left the set is closed, the other one keeps reading.
	shrunk := mustPolicy(t, ifaces, []wol.Rule{ruleFor(10041, wol.ActionNoop)})
	require.NoError(t, service.Reload(ReloadOptions{Policy: shrunk, Registry: testRegistry(&executorMock{})}))

	require.Eventually(t, func() bool { return factory.listener(10042).isClosed() }, time.Second, 5*time.Millisecond)
	require.Equal(t, []int{10041}, service.listeners.ports())
	require.False(t, factory.listener(10041).isClosed())
}

func TestReloadRefusesAPortItCannotBind(t *testing.T) {
	t.Parallel()

	factory := newListeningFactory(10043)
	service, ifaces := reloadService(t, factory, 10041)

	stop, done := runInBackground(t, service)

	defer func() {
		stop()
		<-done
	}()

	require.Eventually(t, func() bool { return len(factory.ports()) == 1 }, time.Second, 5*time.Millisecond)

	grown := mustPolicy(t, ifaces, []wol.Rule{
		ruleFor(10041, wol.ActionNoop),
		ruleFor(10042, wol.ActionNoop),
		ruleFor(10043, wol.ActionNoop),
	})

	err := service.Reload(ReloadOptions{Policy: grown, Registry: testRegistry(&executorMock{})})
	require.ErrorIs(t, err, ErrReloadBind)

	// Nothing half-applied: the rules are the old ones and only the running port is open. What
	// the failed batch bound on the way is closed again rather than leaked.
	require.Len(t, service.Rules(), 1)
	require.Equal(t, []int{10041}, service.listeners.ports())

	for _, port := range factory.ports() {
		if port != 10041 {
			require.True(t, factory.listener(port).isClosed(), "port %d was left open", port)
		}
	}
}

func TestReloadRefreshesTheInterfaceSet(t *testing.T) {
	t.Parallel()

	factory := newListeningFactory(0)
	service, ifaces := reloadService(t, factory, 10041)

	require.Equal(t, ifaces[0].Name, service.Interfaces()[0].Name, "the start-up set first")

	// The sockets bind every address, so a NIC change needs no rebinding - but the status view,
	// MAC resolution and the audit log must describe the machine as it is now.
	moved := make([]wol.IfaceInfo, len(ifaces))
	copy(moved, ifaces)
	moved[0].Name = "enp0s9"

	require.NoError(t, service.Reload(ReloadOptions{
		Policy:   mustPolicy(t, ifaces, []wol.Rule{ruleFor(10041, wol.ActionNoop)}),
		Registry: testRegistry(&executorMock{}),
		Ifaces:   moved,
	}))

	require.Equal(t, "enp0s9", service.Interfaces()[0].Name)
}

func TestReloadWithoutARunningListenerKeepsTheState(t *testing.T) {
	t.Parallel()

	// Before Run there are no sockets, so a reload is a pure state swap: Run binds the port set
	// of the current policy.
	factory := newListeningFactory(0)
	service, ifaces := reloadService(t, factory, 10041)

	grown := mustPolicy(t, ifaces, []wol.Rule{ruleFor(10041, wol.ActionNoop), ruleFor(10042, wol.ActionNoop)})
	require.NoError(t, service.Reload(ReloadOptions{Policy: grown, Registry: testRegistry(&executorMock{})}))
	require.Len(t, service.Rules(), 2)

	stop, done := runInBackground(t, service)

	defer func() {
		stop()
		<-done
	}()

	require.Eventually(t, func() bool { return len(factory.ports()) == 2 }, time.Second, 5*time.Millisecond)
	require.Equal(t, []int{10041, 10042}, service.listeners.ports())
}
