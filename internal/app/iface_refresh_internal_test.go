package app

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

// wifiIface is a NIC that was not there when the service started, like a wireless link that comes
// up after boot or a dock that gets plugged in.
func wifiIface() wol.IfaceInfo {
	return wol.IfaceInfo{Name: "wlp5s0", MAC: net.HardwareAddr{0xF0, 0xD4, 0x15, 0x57, 0x9C, 0xC5}}
}

func growingSelector(start []wol.IfaceInfo, reads *atomic.Int64) func() ([]wol.IfaceInfo, error) {
	return func() ([]wol.IfaceInfo, error) {
		reads.Add(1)

		return append(append([]wol.IfaceInfo{}, start...), wifiIface()), nil
	}
}

// TestUnmatchedPacketRefreshesTheIdentitySet is the lazy path: a packet for a NIC that was not
// there at start-up costs one re-read and then matches.
func TestUnmatchedPacketRefreshesTheIdentitySet(t *testing.T) {
	t.Parallel()

	start := testIfaces()
	wifi := wifiIface()

	var reads atomic.Int64

	executor := &executorMock{}
	svc := NewListenService(
		&factoryMock{},
		testRegistry(executor),
		mustPolicy(t, start, []wol.Rule{ruleFor(8, wol.ActionShutdown)}),
		start,
		false,
	).WithInterfaceSelector(growingSelector(start, &reads), -1)

	svc.handlePacket(context.Background(), packet{
		payload: wol.BuildMagicPacket(wifi.MAC),
		port:    8,
	})

	require.Equal(t, 1, executor.calls, "the packet must match after the refresh")
	require.Equal(t, int64(1), reads.Load())
	require.Equal(t, uint64(1), svc.Stats().Matched)
	require.Len(t, svc.Interfaces(), 2)
}

// TestIfaceRefreshIsRateLimited keeps a flood of noise from turning into a syscall storm: the
// refresh has a floor, and misses inside it are simply misses.
func TestIfaceRefreshIsRateLimited(t *testing.T) {
	t.Parallel()

	var reads atomic.Int64

	svc := NewListenService(
		&factoryMock{},
		testRegistry(&executorMock{}),
		mustPolicy(t, testIfaces(), []wol.Rule{ruleFor(8, wol.ActionShutdown)}),
		testIfaces(),
		false,
	).WithInterfaceSelector(growingSelector(testIfaces(), &reads), -1)

	for range 5 {
		svc.handlePacket(context.Background(), packet{payload: []byte("junk"), port: 8})
	}

	require.Equal(t, int64(1), reads.Load(), "five misses inside the floor window cost one re-read")
}

// TestThePollPicksUpANewNICWithoutTraffic covers what the lazy path cannot: the on-demand refresh
// only fires on a packet that misses, so a machine that has received nothing yet needs the poll.
func TestThePollPicksUpANewNICWithoutTraffic(t *testing.T) {
	t.Parallel()

	start := testIfaces()
	wifi := wifiIface()

	var reads atomic.Int64

	svc := NewListenService(
		&factoryMock{},
		testRegistry(&executorMock{}),
		mustPolicy(t, start, []wol.Rule{ruleFor(8, wol.ActionShutdown)}),
		start,
		false,
	).WithInterfaceSelector(growingSelector(start, &reads), 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go svc.pollInterfaces(ctx)

	require.Eventually(t, func() bool {
		_, matched := svc.snapshot().policy.Resolve(wol.Event{
			Payload: wol.BuildMagicPacket(wifi.MAC),
			DstPort: 8,
		})

		return matched
	}, 2*time.Second, 5*time.Millisecond, "the poll must pick the NIC up without any packet")

	require.Len(t, svc.Interfaces(), 2)

	cancel()
}
