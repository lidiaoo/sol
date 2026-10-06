package wolsend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

var (
	// ErrSendParams reports a wol.send action without its parameters.
	ErrSendParams = errors.New("wol.send action requires parameters")
	// ErrSendMAC reports a missing or malformed target MAC.
	ErrSendMAC = errors.New("wol.send mac must be 6 bytes")
	// ErrSendBroadcast reports an address that is not a valid IPv4 address.
	ErrSendBroadcast = errors.New("wol.send broadcast must be a valid IPv4 address")
	// ErrSendPort reports a destination port outside 1-65535.
	ErrSendPort = errors.New("wol.send port must be between 1 and 65535")
	// ErrSendRepeat reports a copy count outside the allowed range.
	ErrSendRepeat = errors.New("wol.send repeat must be between 1 and the allowed maximum")
	// ErrSendInterval reports a gap outside the allowed range.
	ErrSendInterval = errors.New("wol.send interval is out of range")
	// ErrSendSocket reports a socket or system call failure while sending.
	ErrSendSocket = errors.New("wol.send socket")
	// ErrSendConnType reports a socket that is not a UDP connection.
	ErrSendConnType = errors.New("wol.send expects a UDP connection")
	// ErrSendSign reports `sign: true` without a configured packet key.
	ErrSendSign = errors.New("wol.send sign requires security.packet_auth")
)

// maxPort bounds a destination port.
const maxPort = 65535

// Executor transmits Wake-on-LAN magic packets (type: wol.send, §19.13). It is how one sol
// instance wakes another machine, including another sol whose rules then fire.
//
// The guardrails apply to it like to any other action: cooldowns, the global rate limit,
// dry-run and the audit log are all handled by the caller.
type Executor struct {
	// dial is the socket factory, injectable so a test can observe the datagrams without
	// binding a real broadcast address.
	dial func(ctx context.Context, target string) (*net.UDPConn, error)
	// packetKey, when set, lets an action with `sign: true` authenticate its packets
	// (§19.14) so a target rule with `auth: hmac` accepts them.
	packetKey    []byte
	packetWindow time.Duration
}

// Option tunes the executor at construction time.
type Option func(*Executor)

// WithPacketWindow turns on replay-protected signing (§19.16): with a positive window the
// packets carry a stamp in front of the tag, so the receiver can bound how long a copy stays
// usable.
func WithPacketWindow(window time.Duration) Option {
	return func(e *Executor) { e.packetWindow = window }
}

// WithPacketKey enables signed packets: `sign: true` appends the truncated HMAC tag the
// target expects. Without a key, a `sign: true` action is refused at start-up.
func WithPacketKey(key []byte) Option {
	return func(e *Executor) { e.packetKey = key }
}

// NewExecutor returns an executor sending over real UDP sockets.
func NewExecutor(opts ...Option) *Executor {
	executor := &Executor{dial: dialBroadcast}
	for _, opt := range opts {
		opt(executor)
	}

	return executor
}

// Validate checks a wol.send definition at start-up.
func (e *Executor) Validate(def wol.ActionDef) error {
	params := def.Send
	if params == nil {
		return fmt.Errorf("%w: %s", ErrSendParams, def.Name)
	}

	if err := validateTarget(def.Name, params); err != nil {
		return err
	}

	if err := validateDelivery(def.Name, params); err != nil {
		return err
	}

	if params.Sign && len(e.packetKey) == 0 {
		return fmt.Errorf("%w: %s", ErrSendSign, def.Name)
	}

	return nil
}

// validateTarget checks whom the packet is addressed to.
func validateTarget(name wol.Action, params *wol.SendParams) error {
	if len(params.MAC) != wol.MACSize {
		return fmt.Errorf("%w: %s: got %d bytes", ErrSendMAC, name, len(params.MAC))
	}

	if len(params.SecureOn) != 0 && len(params.SecureOn) != wol.SecureOnSize {
		return fmt.Errorf("%w: %s: secure_on has %d bytes", wol.ErrSecureOnLength, name, len(params.SecureOn))
	}

	if _, err := netip.ParseAddr(params.Broadcast); err != nil {
		return fmt.Errorf("%w: %s: %q", ErrSendBroadcast, name, params.Broadcast)
	}

	return nil
}

// validateDelivery checks how the packet is sent.
func validateDelivery(name wol.Action, params *wol.SendParams) error {
	if params.Port < 1 || params.Port > maxPort {
		return fmt.Errorf("%w: %s: %d", ErrSendPort, name, params.Port)
	}

	if params.Repeat < 1 || params.Repeat > wol.SendMaxRepeat {
		return fmt.Errorf("%w: %s: %d (1..%d)", ErrSendRepeat, name, params.Repeat, wol.SendMaxRepeat)
	}

	if params.Interval < 0 || params.Interval > wol.SendMaxInterval {
		return fmt.Errorf("%w: %s: %s (0..%s)", ErrSendInterval, name, params.Interval, wol.SendMaxInterval)
	}

	return nil
}

// Execute sends the magic packet to the configured destination, Repeat times.
func (e *Executor) Execute(ctx context.Context, def wol.ActionDef, _ wol.Event) error {
	params := def.Send
	if params == nil {
		return fmt.Errorf("%w: %s", ErrSendParams, def.Name)
	}

	packet, err := wol.EncodeMagicPacket(params.MAC, params.SecureOn)
	if err != nil {
		return fmt.Errorf("action %s: %w", def.Name, err)
	}

	packet = e.sign(params, packet)

	target := net.JoinHostPort(params.Broadcast, strconv.Itoa(params.Port))

	conn, err := e.dial(ctx, target)
	if err != nil {
		return fmt.Errorf("%w: action %s: %w", ErrSendSocket, def.Name, err)
	}
	defer conn.Close()

	for copyIndex := range params.Repeat {
		if _, err := conn.Write(packet); err != nil {
			return fmt.Errorf("%w: action %s: %w", ErrSendSocket, def.Name, err)
		}

		// No trailing wait: the last copy has nowhere to go.
		if copyIndex+1 < params.Repeat {
			if err := wait(ctx, params.Interval); err != nil {
				return err
			}
		}
	}

	slog.Info("wol packet sent",
		"action", string(def.Name),
		"mac", params.MAC.String(),
		"broadcast", params.Broadcast,
		"port", params.Port,
		"secure_on", len(params.SecureOn) > 0,
		"signed", params.Sign,
		"copies", params.Repeat,
		"bytes", len(packet),
	)

	return nil
}

// sign appends the authentication tag of §19.14 when the action asks for it: with a window
// configured (§19.16) the tag is preceded by a stamp, so the receiver can also bound replays.
func (e *Executor) sign(params *wol.SendParams, packet []byte) []byte {
	if !params.Sign {
		return packet
	}

	if e.packetWindow > 0 {
		return append(packet, wol.SignTimestampedPacket(e.packetKey, packet, time.Now())...)
	}

	return append(packet, wol.SignPacket(e.packetKey, packet)...)
}

// wait pauses between two copies, giving up as soon as the context ends so a shutdown does not
// sit through the remaining copies.
func wait(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return nil
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("wol.send: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// dialBroadcast opens the sending socket and enables broadcast sending, which Linux refuses for
// a broadcast address unless the socket asked for it.
func dialBroadcast(ctx context.Context, target string) (*net.UDPConn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp4", target)
	if err != nil {
		return nil, err
	}

	udp, ok := conn.(*net.UDPConn)
	if !ok {
		conn.Close()

		return nil, fmt.Errorf("%w: got %T", ErrSendConnType, conn)
	}

	if err := enableBroadcast(udp); err != nil {
		udp.Close()

		return nil, err
	}

	return udp, nil
}
