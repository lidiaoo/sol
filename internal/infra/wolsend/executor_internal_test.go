package wolsend

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

// targetMAC is a 6-byte NIC address used as the wake target throughout these tests.
func targetMAC() net.HardwareAddr {
	return net.HardwareAddr{0x58, 0x11, 0x22, 0xbc, 0x78, 0x66}
}

// testSecureOn is the password the collector expects (6 bytes, per SecureOnSize).
func testSecureOn() []byte {
	return []byte("s3cret")
}

// loopback is where the collector listens; a wol.send action accepts any unicast address.
const loopback = "127.0.0.1"

func testDef() wol.ActionDef {
	return wol.ActionDef{
		Name: "wake-nas",
		Type: wol.ActionTypeSend,
		Send: &wol.SendParams{
			MAC:       targetMAC(),
			Broadcast: loopback,
			Port:      9,
			Repeat:    1,
			Interval:  wol.SendDefaultInterval,
		},
	}
}

// collector is a UDP socket standing in for the machine being woken: it keeps every datagram
// that arrives on its port.
type collector struct {
	conn *net.UDPConn
	done chan struct{}
	got  chan []byte
}

func newCollector(t *testing.T) *collector {
	t.Helper()

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)

	col := &collector{conn: conn, done: make(chan struct{}), got: make(chan []byte, 16)}

	go func() {
		defer close(col.got)

		for {
			buf := make([]byte, wol.BufferSize)

			read, _, readErr := conn.ReadFromUDP(buf)
			if readErr != nil {
				return
			}

			select {
			case col.got <- bytes.Clone(buf[:read]):
			case <-col.done:
				return
			}
		}
	}()

	t.Cleanup(func() {
		close(col.done)
		conn.Close()
	})

	return col
}

func (c *collector) port(t *testing.T) int {
	t.Helper()

	addr, ok := c.conn.LocalAddr().(*net.UDPAddr)
	require.True(t, ok)

	return addr.Port
}

// receive waits for one datagram, so a test never sleeps for a fixed period.
func (c *collector) receive(t *testing.T) []byte {
	t.Helper()

	select {
	case packet := <-c.got:
		return packet
	case <-time.After(2 * time.Second):
		t.Fatal("no datagram arrived")

		return nil
	}
}

// silence asserts that nothing else arrives within the window.
func (c *collector) silence(t *testing.T, wait time.Duration) {
	t.Helper()

	select {
	case packet := <-c.got:
		t.Fatalf("unexpected datagram of %d bytes", len(packet))
	case <-time.After(wait):
	}
}

func TestExecuteSendsAParsablePacket(t *testing.T) {
	t.Parallel()

	col := newCollector(t)

	def := testDef()
	def.Send.Port = col.port(t)

	require.NoError(t, NewExecutor().Execute(context.Background(), def, wol.Event{}))

	packet := col.receive(t)
	require.Len(t, packet, wol.PacketLenPlain)

	parsed, ok := wol.ParsePacket(packet, nil)
	require.True(t, ok, "the packet we send must be one we accept")
	require.Equal(t, targetMAC(), parsed.MAC)
	require.Empty(t, parsed.Content)
}

func TestExecuteCarriesSecureOn(t *testing.T) {
	t.Parallel()

	col := newCollector(t)

	def := testDef()
	def.Send.Port = col.port(t)
	def.Send.SecureOn = testSecureOn()

	require.NoError(t, NewExecutor().Execute(context.Background(), def, wol.Event{}))

	packet := col.receive(t)
	require.Len(t, packet, wol.PacketLenSecureOn)

	// A receiver checking the password accepts it; one checking a different password does not.
	parsed, ok := wol.ParsePacket(packet, testSecureOn())
	require.True(t, ok)
	require.Equal(t, targetMAC(), parsed.MAC)

	_, ok = wol.ParsePacket(packet, []byte("wrong!"))
	require.False(t, ok, "the wrong password must not match")
}

func TestExecuteRepeatsWithTheConfiguredGap(t *testing.T) {
	t.Parallel()

	col := newCollector(t)

	def := testDef()
	def.Send.Port = col.port(t)
	def.Send.Repeat = 3
	def.Send.Interval = 20 * time.Millisecond

	start := time.Now()

	require.NoError(t, NewExecutor().Execute(context.Background(), def, wol.Event{}))

	elapsed := time.Since(start)

	for copyIndex := range 3 {
		parsed, ok := wol.ParsePacket(col.receive(t), nil)
		require.True(t, ok, "copy %d", copyIndex+1)
		require.Equal(t, targetMAC(), parsed.MAC)
	}

	col.silence(t, 100*time.Millisecond)

	// Three copies leave two gaps of 20ms; a trailing wait would push this past 40ms by
	// another 20ms, so the bound below catches it.
	require.GreaterOrEqual(t, elapsed, 40*time.Millisecond, "the copies should be spaced apart")
	require.Less(t, elapsed, 58*time.Millisecond, "the last copy must not be followed by a wait")
}

func TestExecuteStopsWithTheContext(t *testing.T) {
	t.Parallel()

	col := newCollector(t)

	def := testDef()
	def.Send.Port = col.port(t)
	def.Send.Repeat = 5
	def.Send.Interval = time.Second

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		// The first copy goes out immediately; cancel while the executor waits for the second.
		time.Sleep(50 * time.Millisecond)

		cancel()
	}()

	require.ErrorIs(t, NewExecutor().Execute(ctx, def, wol.Event{}), context.Canceled)

	col.receive(t)
	col.silence(t, 200*time.Millisecond)
}

func TestExecuteUsesTheInjectedSocket(t *testing.T) {
	t.Parallel()

	// The dial seam is what lets a test watch the datagram on a loopback port instead of
	// binding a broadcast address.
	col := newCollector(t)
	colPort := col.port(t)

	var target string

	executor := &Executor{dial: func(ctx context.Context, address string) (*net.UDPConn, error) {
		target = address

		return dialBroadcast(ctx, address)
	}}

	def := testDef()
	def.Send.Port = colPort

	require.NoError(t, executor.Execute(context.Background(), def, wol.Event{}))
	require.Equal(t, net.JoinHostPort(loopback, strconv.Itoa(colPort)), target)
	require.Len(t, col.receive(t), wol.PacketLenPlain)
}

func TestValidate(t *testing.T) {
	t.Parallel()

	require.NoError(t, NewExecutor().Validate(testDef()))

	for _, tc := range sendValidationCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			def := testDef()
			tc.mutate(&def)

			require.ErrorIs(t, NewExecutor().Validate(def), tc.match)
		})
	}
}

// sendValidationCases lists the definitions Validate must refuse, and why.
func sendValidationCases() []struct {
	name   string
	mutate func(def *wol.ActionDef)
	match  error
} {
	return []struct {
		name   string
		mutate func(def *wol.ActionDef)
		match  error
	}{
		{name: "missing params", mutate: func(def *wol.ActionDef) { def.Send = nil }, match: ErrSendParams},
		{
			name:   "short mac",
			mutate: func(def *wol.ActionDef) { def.Send.MAC = net.HardwareAddr{0x01, 0x02} },
			match:  ErrSendMAC,
		},
		{
			name:   "secure_on too short",
			mutate: func(def *wol.ActionDef) { def.Send.SecureOn = []byte("short") },
			match:  wol.ErrSecureOnLength,
		},
		{
			name:   "broken broadcast",
			mutate: func(def *wol.ActionDef) { def.Send.Broadcast = "not-an-address" },
			match:  ErrSendBroadcast,
		},
		{
			name:   "port zero",
			mutate: func(def *wol.ActionDef) { def.Send.Port = 0 },
			match:  ErrSendPort,
		},
		{
			name:   "port too high",
			mutate: func(def *wol.ActionDef) { def.Send.Port = 70000 },
			match:  ErrSendPort,
		},
		{
			name:   "no copies",
			mutate: func(def *wol.ActionDef) { def.Send.Repeat = 0 },
			match:  ErrSendRepeat,
		},
		{
			name:   "too many copies",
			mutate: func(def *wol.ActionDef) { def.Send.Repeat = wol.SendMaxRepeat + 1 },
			match:  ErrSendRepeat,
		},
		{
			name:   "negative interval",
			mutate: func(def *wol.ActionDef) { def.Send.Interval = -time.Second },
			match:  ErrSendInterval,
		},
		{
			name:   "interval too long",
			mutate: func(def *wol.ActionDef) { def.Send.Interval = wol.SendMaxInterval + time.Second },
			match:  ErrSendInterval,
		},
	}
}

func TestExecuteRejectsUnusableParams(t *testing.T) {
	t.Parallel()

	def := testDef()
	def.Send = nil
	require.ErrorIs(t, NewExecutor().Execute(context.Background(), def, wol.Event{}), ErrSendParams)

	def = testDef()
	def.Send.MAC = net.HardwareAddr{0x01}
	require.ErrorIs(t, NewExecutor().Execute(context.Background(), def, wol.Event{}), wol.ErrMACLength)
}

func TestExecuteReportsAnUnreachableSocket(t *testing.T) {
	t.Parallel()

	// An address the kernel refuses: the error must name the action and the socket step.
	executor := &Executor{dial: func(context.Context, string) (*net.UDPConn, error) {
		return nil, net.ErrClosed
	}}

	require.ErrorIs(t, executor.Execute(context.Background(), testDef(), wol.Event{}), ErrSendSocket)
}

func TestExecuteSignsPackets(t *testing.T) {
	t.Parallel()

	key := []byte("shared-key")
	col := newCollector(t)

	def := testDef()
	def.Send.Broadcast = loopback
	def.Send.Port = col.port(t)
	def.Send.Sign = true

	require.NoError(t, NewExecutor(WithPacketKey(key)).Execute(context.Background(), def, wol.Event{}))

	received := col.receive(t)
	data, ok := wol.SplitPacketSignature(key, received)
	require.True(t, ok, "the tag must verify with the shared key")
	require.Len(t, received, len(data)+wol.PacketSignatureLen)

	parsed, ok := wol.ParsePacket(data, nil)
	require.True(t, ok)
	require.Equal(t, targetMAC(), parsed.MAC)
}

func TestValidateRefusesSignWithoutAKey(t *testing.T) {
	t.Parallel()

	def := testDef()
	def.Send.Sign = true

	require.ErrorIs(t, NewExecutor().Validate(def), ErrSendSign)
	require.NoError(t, NewExecutor(WithPacketKey([]byte("k"))).Validate(def))
}
