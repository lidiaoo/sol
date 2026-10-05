package app

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

// blockingExecutor holds its run open until the test releases it, so a second trigger arrives
// while the first is still going.
type blockingExecutor struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func newBlockingExecutor() *blockingExecutor {
	return &blockingExecutor{started: make(chan struct{}, 4), release: make(chan struct{})}
}

func (b *blockingExecutor) Execute(context.Context, wol.ActionDef, wol.Event) error {
	b.calls.Add(1)

	select {
	case b.started <- struct{}{}:
	default:
	}

	<-b.release

	return nil
}

func inflightService(t *testing.T, executor wol.Executor) *ListenService {
	t.Helper()

	ifaces := testIfaces()

	return NewListenService(
		&factoryMock{},
		testRegistry(executor),
		mustPolicy(t, ifaces, []wol.Rule{ruleFor(10041, wol.ActionShutdown)}),
		ifaces,
		false,
	)
}

func TestInflightGuardMarksAndReleases(t *testing.T) {
	t.Parallel()

	guard := newInflight()
	require.Zero(t, guard.running())

	release, started := guard.begin("shutdown")
	require.True(t, started)
	require.Equal(t, 1, guard.running())

	// A second begin for the same key is refused and must not hand back a release, or the first
	// run would be forgotten by an unrelated caller.
	second, started := guard.begin("shutdown")
	require.False(t, started)
	require.Nil(t, second)
	require.Equal(t, 1, guard.running())

	// A different key is a different run.
	_, started = guard.begin("reboot")
	require.True(t, started)
	require.Equal(t, 2, guard.running())

	release()
	require.Equal(t, 1, guard.running())

	_, started = guard.begin("shutdown")
	require.True(t, started, "the key is free again once the run is over")
}

func TestRunKeySeparatesDifferentInvocations(t *testing.T) {
	t.Parallel()

	require.Equal(t, "power.shutdown", runKey("power.shutdown", wol.Event{}, ""))
	require.Equal(
		t,
		runKey("power.shutdown", wol.Event{}, ""),
		runKey("power.shutdown", wol.Event{Payload: []byte("noise")}, ""),
		"the transport envelope is not part of the identity",
	)
	require.NotEqual(
		t,
		runKey("remote:backup", wol.Event{Args: map[string]string{"target": "home"}}, ""),
		runKey("remote:backup", wol.Event{Args: map[string]string{"target": "work"}}, ""),
		"two different invocations of one action are not duplicates",
	)
	require.Equal(
		t,
		runKey("remote:backup", wol.Event{Args: map[string]string{"a": "1", "b": "2"}}, ""),
		runKey("remote:backup", wol.Event{Args: map[string]string{"b": "2", "a": "1"}}, ""),
		"argument order does not make a duplicate a different run",
	)
	require.NotEqual(
		t,
		runKey("raw:shell", wol.Event{}, "echo hi"),
		runKey("raw:shell", wol.Event{}, "echo bye"),
		"the raw shell's command is the identity",
	)
}

func TestDispatchSuppressesAConcurrentRun(t *testing.T) {
	t.Parallel()

	executor := newBlockingExecutor()
	service := inflightService(t, executor)

	// The first run holds the executor open for the whole test.
	go func() {
		_ = service.Dispatch(context.Background(), wol.ActionShutdown, wol.Event{})
	}()

	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("the first run never started")
	}

	err := service.Dispatch(context.Background(), wol.ActionShutdown, wol.Event{})
	require.ErrorIs(t, err, ErrActionInFlight)
	require.Equal(t, uint64(1), service.Stats().Inflight)
	require.Equal(t, uint64(1), service.Stats().Suppressed, "a refusal is a refusal, whatever the guard")
	require.Equal(t, int64(1), executor.calls.Load(), "the duplicate never reached the executor")

	// The same action asked to do something else is not a duplicate.
	go func() {
		_ = service.Dispatch(context.Background(), wol.ActionShutdown, wol.Event{Args: map[string]string{"target": "home"}})
	}()

	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("a different invocation was suppressed as if it were a duplicate")
	}

	close(executor.release)
	require.Eventually(t, func() bool { return service.inflight.running() == 0 }, time.Second, 5*time.Millisecond,
		"both runs release their mark when they finish")

	require.NoError(t, service.Dispatch(context.Background(), wol.ActionShutdown, wol.Event{}))
}

func TestRunRawShellSuppressesAConcurrentCommand(t *testing.T) {
	t.Parallel()

	executor := newBlockingExecutor()
	service := inflightService(t, executor)

	service.WithRawShell(RawShellSettings{
		Enabled: true,
		Ports:   []int{rawShellTestPort},
		Key:     []byte("raw-key"),
		Exec:    wol.ExecParams{Command: []string{"/bin/echo"}},
	})

	rt := service.snapshot()
	require.NotNil(t, rt.rawShell)

	pkt := packet{src: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, port: rawShellTestPort}

	go func() {
		_ = service.runRawShell(context.Background(), rt, pkt, wol.Event{}, "echo hi")
	}()

	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("the raw shell command never started")
	}

	// The same command again is a duplicate; the same channel with another command is not. The
	// refusal comes back as an error so the HTTP transport can answer 429 instead of 202.
	require.ErrorIs(t, service.runRawShell(context.Background(), rt, pkt, wol.Event{}, "echo hi"), ErrActionInFlight)
	require.Equal(t, uint64(1), service.Stats().Inflight)
	require.Equal(t, int64(1), executor.calls.Load())

	go func() {
		_ = service.runRawShell(context.Background(), rt, pkt, wol.Event{}, "echo bye")
	}()

	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("a different raw shell command was suppressed as if it were a duplicate")
	}

	close(executor.release)
}
