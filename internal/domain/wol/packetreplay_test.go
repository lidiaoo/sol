package wol_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

const replayTestPort = 10018

// authAnyRule matches any content on this port but only when the packet is authenticated, so a
// refusal can only come from the tag or the stamp and never from the content matcher.
func authAnyRule(port int) wol.Rule {
	rule := plainRule(port, wol.ActionNoop)
	rule.Match.Content = wol.ContentMatcher{Kind: wol.ContentAny}
	rule.Match.Auth = wol.AuthHMAC

	return rule
}

// replayPolicy builds a policy with a window and a frozen clock, plus a collector for the
// rejection reasons so a test can tell "stale" from "replay".
func replayPolicy(t *testing.T, key []byte, window time.Duration, now time.Time) (*wol.RoutingPolicy, *[]string) {
	t.Helper()

	reasons := &[]string{}

	policy, err := wol.NewRoutingPolicy([]wol.Rule{authAnyRule(replayTestPort)}, testIfaces(), wol.PolicyOptions{
		PacketKey:      key,
		PacketWindow:   window,
		Now:            func() time.Time { return now },
		OnAuthRejected: func(reason string) { *reasons = append(*reasons, reason) },
	})
	require.NoError(t, err)

	return policy, reasons
}

// stamped builds a magic packet with the given content, carrying a stamp and a tag.
func stamped(key []byte, content []byte, at time.Time) []byte {
	packet := append(wol.BuildMagicPacket(testMAC()), content...)

	return append(packet, wol.SignTimestampedPacket(key, packet, at)...)
}

func TestSignTimestampedPacketRoundTrip(t *testing.T) {
	t.Parallel()

	key := []byte("shared-key")
	at := time.Unix(1_700_000_000, 0)

	magic := wol.BuildMagicPacket(testMAC())

	payload := append([]byte{}, magic...)
	payload = append(payload, []byte("payload")...)

	signed := append(append([]byte{}, payload...), wol.SignTimestampedPacket(key, payload, at)...)
	require.Len(t, signed, len(payload)+wol.TimestampLen+wol.PacketSignatureLen)

	data, stamp, tag, ok := wol.SplitTimestampedSignature(key, signed)
	require.True(t, ok)
	require.Equal(t, payload, data)
	require.Equal(t, at.Unix(), stamp)
	require.Len(t, tag, wol.PacketSignatureLen)

	// A wrong key, a flipped bit and a truncated payload are all refused.
	require.False(t, verifies([]byte("other-key"), signed))

	tampered := append([]byte{}, signed...)
	tampered[len(tampered)-1] ^= 0x01
	require.False(t, verifies(key, tampered))
	require.False(t, verifies(key, signed[:len(signed)-1]))
}

// verifies reports whether a stamped payload passes verification, checking the parts it ignores
// so a broken split cannot slip through as "verified".
func verifies(key []byte, payload []byte) bool {
	data, stamp, tag, ok := wol.SplitTimestampedSignature(key, payload)
	if !ok {
		return false
	}

	return len(tag) == wol.PacketSignatureLen && len(data) < len(payload) && stamp != 0
}

func TestPolicyAcceptsAFreshStampedPacketOnce(t *testing.T) {
	t.Parallel()

	key := []byte("shared-key")
	now := time.Unix(1_700_000_000, 0)
	policy, reasons := replayPolicy(t, key, time.Minute, now)

	packet := stamped(key, nil, now)

	decision, ok := policy.Resolve(wol.Event{Payload: packet, DstPort: replayTestPort})
	require.True(t, ok)
	require.True(t, decision.Authenticated)
	require.Equal(t, wol.ActionNoop, decision.Action)

	// The same bytes a second time: the tag is already spent.
	_, ok = policy.Resolve(wol.Event{Payload: packet, DstPort: replayTestPort})
	require.False(t, ok)
	require.Equal(t, []string{wol.ReplaySeen}, *reasons)

	// A different packet in the same second is still fresh.
	decision, ok = policy.Resolve(wol.Event{Payload: stamped(key, []byte("other"), now), DstPort: replayTestPort})
	require.True(t, ok)
	require.True(t, decision.Authenticated)
}

func TestPolicyRefusesStaleAndFutureStamps(t *testing.T) {
	t.Parallel()

	key := []byte("shared-key")
	now := time.Unix(1_700_000_000, 0)
	policy, reasons := replayPolicy(t, key, time.Minute, now)
	event := func(packet []byte) wol.Event {
		return wol.Event{Payload: packet, DstPort: replayTestPort}
	}

	// A minute is the whole window: one second past it is stale, and a stamp from the future is
	// refused too, so a clock running ahead cannot buy a longer life for a captured packet.
	_, ok := policy.Resolve(event(stamped(key, nil, now.Add(-61*time.Second))))
	require.False(t, ok)
	_, ok = policy.Resolve(event(stamped(key, nil, now.Add(61*time.Second))))
	require.False(t, ok)
	require.Equal(t, []string{wol.ReplayStale, wol.ReplayStale}, *reasons)

	// The edges of the window are inside it.
	_, ok = policy.Resolve(event(stamped(key, []byte("a"), now.Add(-time.Minute))))
	require.True(t, ok)
	_, ok = policy.Resolve(event(stamped(key, []byte("b"), now.Add(time.Minute))))
	require.True(t, ok)
}

func TestPolicyWindowRefusesTheLegacyLayout(t *testing.T) {
	t.Parallel()

	key := []byte("shared-key")
	now := time.Unix(1_700_000_000, 0)
	policy, _ := replayPolicy(t, key, time.Minute, now)

	// A §19.14 packet (a bare tag, no stamp) cannot be verified once a window is configured:
	// both ends have to agree on the layout.
	packet := wol.BuildMagicPacket(testMAC())
	legacy := append(append([]byte{}, packet...), wol.SignPacket(key, packet)...)

	_, ok := policy.Resolve(wol.Event{Payload: legacy, DstPort: replayTestPort})
	require.False(t, ok)

	// With no window the same packet is authenticated as before, so the feature is additive.
	plain, err := wol.NewRoutingPolicy([]wol.Rule{authAnyRule(replayTestPort)}, testIfaces(), wol.PolicyOptions{
		PacketKey: key,
	})
	require.NoError(t, err)

	decision, ok := plain.Resolve(wol.Event{Payload: legacy, DstPort: replayTestPort})
	require.True(t, ok)
	require.True(t, decision.Authenticated)
}

func TestPolicyRefusesNewPacketsWhenTheReplayCacheIsFull(t *testing.T) {
	t.Parallel()

	key := []byte("shared-key")
	now := time.Unix(1_700_000_000, 0)
	policy, reasons := replayPolicy(t, key, time.Hour, now)

	// Each packet needs its own tag, so the content carries a counter. The cache holds what the
	// window allows; once it is full, new packets are refused rather than evicting an entry
	// whose stamp is still replayable.
	for i := range 4096 {
		packet := stamped(key, []byte{byte(i >> 8), byte(i)}, now)
		_, ok := policy.Resolve(wol.Event{Payload: packet, DstPort: replayTestPort})
		require.Truef(t, ok, "packet %d inside the cache must be accepted", i)
	}

	_, ok := policy.Resolve(wol.Event{Payload: stamped(key, []byte("overflow"), now), DstPort: replayTestPort})
	require.False(t, ok)
	require.Equal(t, []string{wol.ReplayFull}, *reasons)
}
