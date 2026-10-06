package app

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

type factoryMock struct {
	listener PacketListener
	err      error
}

func (m *factoryMock) Create(_ int) (PacketListener, error) {
	if m.err != nil {
		return nil, m.err
	}

	return m.listener, nil
}

type executorMock struct {
	// handled, when set, is closed by the first execution: it lets a mock listener wait for the
	// packet it just delivered to be acted on before reporting the stop that ends Run.
	handled chan struct{}
	calls   int
	action  wol.Action
	def     wol.ActionDef
	event   wol.Event
	err     error
}

func (m *executorMock) Execute(_ context.Context, def wol.ActionDef, ev wol.Event) error {
	m.calls++
	m.action = def.Name
	m.def = def
	m.event = ev

	if m.handled != nil {
		close(m.handled)
		m.handled = nil
	}

	return m.err
}

type listenerMock struct {
	packets   [][]byte
	srcs      []*net.UDPAddr
	errs      []error
	idx       int
	closed    bool
	delivered int
	handled   chan struct{}
}

func (m *listenerMock) ReadPacket(ctx context.Context) ([]byte, *net.UDPAddr, error) {
	if m.idx >= len(m.errs) {
		m.awaitHandled(ctx)

		return nil, nil, context.Canceled
	}

	err := m.errs[m.idx]
	if err != nil {
		m.awaitHandled(ctx)

		m.idx++

		return nil, nil, err
	}

	payload := m.packets[m.idx]
	src := m.srcs[m.idx]
	m.idx++
	m.delivered++

	return payload, src, nil
}

func (m *listenerMock) Close() error {
	m.closed = true

	return nil
}

// awaitHandled waits for the packets already delivered to be acted on before reporting a stop.
// Without it the event loop is free to notice the stop first and drop a packet the test is about
// to assert on - a race in the harness, not in the service. The wait is bounded because some
// tests deliver a packet and expect no execution at all (a dry run, a non-matching payload).
func (m *listenerMock) awaitHandled(ctx context.Context) {
	if m.handled == nil || m.delivered == 0 {
		return
	}

	select {
	case <-m.handled:
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
	}
}

type testFixture struct {
	listener *listenerMock
	executor *executorMock
	service  *ListenService
}

func testMAC() net.HardwareAddr {
	return net.HardwareAddr{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
}

func testIfaces() []wol.IfaceInfo {
	return []wol.IfaceInfo{{Name: "en0", MAC: testMAC(), IPs: []net.IP{net.IPv4(127, 0, 0, 1)}}}
}

func ruleFor(port int, action wol.Action) wol.Rule {
	return wol.Rule{
		Match:  wol.Match{Ports: []int{port}, MAC: wol.MACSelector{Kind: wol.MACSelf}},
		Action: action,
	}
}

func mustPolicy(t *testing.T, ifaces []wol.IfaceInfo, rules []wol.Rule) *wol.RoutingPolicy {
	t.Helper()

	policy, err := wol.NewRoutingPolicy(rules, ifaces, wol.PolicyOptions{})
	require.NoError(t, err)

	return policy
}

func testRegistry(executor wol.Executor) *wol.Registry {
	registry := wol.NewRegistry()
	registry.Register(
		executor,
		wol.ActionTypeNoop,
		wol.ActionTypeSleep,
		wol.ActionTypeShutdown,
		wol.ActionTypeReboot,
		wol.ActionTypeExec,
	)

	return registry
}

func newFixture(t *testing.T, port int, action wol.Action, packets [][]byte, packetErrs []error, dryRun bool) *testFixture {
	t.Helper()

	ifaces := testIfaces()
	policy := mustPolicy(t, ifaces, []wol.Rule{ruleFor(port, action)})

	srcs := make([]*net.UDPAddr, len(packets))
	for i := range packets {
		srcs[i] = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10001 + i}
	}

	handled := make(chan struct{})
	listener := &listenerMock{
		packets: packets,
		srcs:    srcs,
		errs:    packetErrs,
		handled: handled,
	}
	executor := &executorMock{handled: handled}

	svc := NewListenService(&factoryMock{listener: listener}, testRegistry(executor), policy, ifaces, dryRun)

	return &testFixture{
		listener: listener,
		executor: executor,
		service:  svc,
	}
}

func TestListenServiceRun_MagicPacketShutdown(t *testing.T) {
	t.Parallel()

	magic := wol.BuildMagicPacket(testMAC())

	fixture := newFixture(t, 8, wol.ActionShutdown, [][]byte{magic}, []error{nil, context.Canceled}, false)

	err := fixture.service.Run(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, fixture.executor.calls)
	require.Equal(t, wol.ActionShutdown, fixture.executor.action)
	require.True(t, fixture.listener.closed)
}

func TestListenServiceRun_MagicPacketReboot(t *testing.T) {
	t.Parallel()

	magic := wol.BuildMagicPacket(testMAC())

	fixture := newFixture(t, 8, wol.ActionReboot, [][]byte{magic}, []error{nil, context.Canceled}, false)

	err := fixture.service.Run(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, fixture.executor.calls)
	require.Equal(t, wol.ActionReboot, fixture.executor.action)
}

func TestListenServiceRun_MagicPacketSleep(t *testing.T) {
	t.Parallel()

	magic := wol.BuildMagicPacket(testMAC())

	fixture := newFixture(t, 10, wol.ActionSleep, [][]byte{magic}, []error{nil, context.Canceled}, false)

	err := fixture.service.Run(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, fixture.executor.calls)
	require.Equal(t, wol.ActionSleep, fixture.executor.action)
}

func TestListenServiceRun_NoopActionIsDispatched(t *testing.T) {
	t.Parallel()

	magic := wol.BuildMagicPacket(testMAC())

	fixture := newFixture(t, 9, wol.ActionNoop, [][]byte{magic}, []error{nil, context.Canceled}, false)

	err := fixture.service.Run(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, fixture.executor.calls)
	require.Equal(t, wol.ActionNoop, fixture.executor.action)
}

func TestListenServiceRun_DryRunSkipsDispatch(t *testing.T) {
	t.Parallel()

	magic := wol.BuildMagicPacket(testMAC())

	fixture := newFixture(t, 8, wol.ActionShutdown, [][]byte{magic}, []error{nil, context.Canceled}, true)

	err := fixture.service.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, fixture.executor.calls)
}

func TestListenServiceRun_ContextCanceled(t *testing.T) {
	t.Parallel()

	listener := &listenerMock{
		packets: [][]byte{},
		srcs:    []*net.UDPAddr{},
		errs:    []error{context.Canceled},
	}

	svc := NewListenService(
		&factoryMock{listener: listener},
		testRegistry(&executorMock{}),
		mustPolicy(t, testIfaces(), []wol.Rule{ruleFor(8, wol.ActionShutdown)}),
		testIfaces(),
		false,
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.Run(ctx)
	require.NoError(t, err)
}

func TestListenServiceStatsAndControlPlane(t *testing.T) {
	ifaces := testIfaces()
	policy := mustPolicy(t, ifaces, []wol.Rule{ruleFor(8, wol.ActionShutdown)})
	executor := &executorMock{}

	svc := NewListenService(&factoryMock{}, testRegistry(executor), policy, ifaces, false)

	svc.handlePacket(context.Background(), packet{
		payload: wol.BuildMagicPacket(testMAC()),
		src:     &net.UDPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 9},
		port:    8,
	})
	svc.handlePacket(context.Background(), packet{payload: []byte("junk"), port: 8})

	stats := svc.Stats()
	require.Equal(t, uint64(2), stats.Packets)
	require.Equal(t, uint64(1), stats.Matched)
	require.Equal(t, uint64(1), stats.Actions["power.shutdown"])
	require.NotNil(t, stats.LastEvent)
	require.Equal(t, 8, stats.LastEvent.Port)
	require.Equal(t, "en0", stats.LastEvent.Interface)
	require.Equal(t, 1, executor.calls)
	require.Len(t, svc.Rules(), 1)
	require.Equal(t, ifaces, svc.Interfaces())

	require.NoError(t, svc.Dispatch(context.Background(), wol.ActionShutdown, wol.Event{}))
	require.Equal(t, uint64(2), svc.Stats().Actions["power.shutdown"])
	require.ErrorIs(t, svc.Dispatch(context.Background(), wol.Action("nope"), wol.Event{}), wol.ErrUnknownActionRef)
}

func TestListenServiceDispatchHonoursDryRun(t *testing.T) {
	ifaces := testIfaces()
	policy := mustPolicy(t, ifaces, []wol.Rule{ruleFor(8, wol.ActionShutdown)})
	executor := &executorMock{}

	svc := NewListenService(&factoryMock{}, testRegistry(executor), policy, ifaces, true)

	require.NoError(t, svc.Dispatch(context.Background(), wol.ActionShutdown, wol.Event{}))
	require.Zero(t, executor.calls)
	require.Empty(t, svc.Stats().Actions)
}

func TestListenServiceRun_DuplicatePortRules(t *testing.T) {
	t.Parallel()

	_, err := wol.NewRoutingPolicy(
		[]wol.Rule{ruleFor(8, wol.ActionShutdown), ruleFor(8, wol.ActionReboot)},
		testIfaces(),
		wol.PolicyOptions{},
	)
	require.Error(t, err)
}
