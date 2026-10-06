package wol_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

func TestPolicyRequiresAValidTag(t *testing.T) {
	t.Parallel()

	key := []byte("shared-key")
	rule := plainRule(10010, wol.ActionNoop)
	rule.Match.Auth = wol.AuthHMAC

	policy, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), wol.PolicyOptions{PacketKey: key})
	require.NoError(t, err)

	mac := testMAC()
	packet := wol.BuildMagicPacket(mac)

	// An unauthenticated packet does not satisfy an auth rule.
	_, ok := policy.Resolve(wol.Event{Payload: packet, DstPort: 10010})
	require.False(t, ok)

	// A tag signed with the wrong key is not authentication either.
	wrong := append(append([]byte{}, packet...), wol.SignPacket([]byte("other-key"), packet)...)
	_, ok = policy.Resolve(wol.Event{Payload: wrong, DstPort: 10010})
	require.False(t, ok)

	signed := append(append([]byte{}, packet...), wol.SignPacket(key, packet)...)
	decision, ok := policy.Resolve(wol.Event{Payload: signed, DstPort: 10010})
	require.True(t, ok)
	require.True(t, decision.Authenticated)
	require.Equal(t, wol.ActionNoop, decision.Action)

	// A plain rule never sees the stripped bytes, so the tag cannot make it match.
	plain, err := wol.NewRoutingPolicy([]wol.Rule{plainRule(10010, wol.ActionNoop)}, testIfaces(), wol.PolicyOptions{
		PacketKey: key,
	})
	require.NoError(t, err)

	// A plain rule accepts any payload, so it also matches a signed packet; it is the weaker
	// rule, and an auth rule on the same port outranks it (see the ranking test below).
	decision, ok = plain.Resolve(wol.Event{Payload: signed, DstPort: 10010})
	require.True(t, ok)
	require.True(t, decision.Authenticated, "authentication describes the packet, not the rule")

	decision, ok = plain.Resolve(wol.Event{Payload: packet, DstPort: 10010})
	require.True(t, ok)
	require.False(t, decision.Authenticated)
}

// A signed packet keeps its content: suffix rules match the bytes before the tag.
func TestPolicyAuthWithContent(t *testing.T) {
	t.Parallel()

	key := []byte("shared-key")
	rule := contentRule(10010, wol.ContentSuffix, "off", wol.ActionShutdown)
	rule.Match.Auth = wol.AuthHMAC

	policy, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), wol.PolicyOptions{PacketKey: key})
	require.NoError(t, err)

	packet := append(wol.BuildMagicPacket(testMAC()), []byte("off")...)
	signed := append(append([]byte{}, packet...), wol.SignPacket(key, packet)...)

	decision, ok := policy.Resolve(wol.Event{Payload: signed, DstPort: 10010})
	require.True(t, ok)
	require.True(t, decision.Authenticated)
	require.Equal(t, wol.ActionShutdown, decision.Action)
}

func TestPolicyAuthErrors(t *testing.T) {
	t.Parallel()

	authRule := plainRule(10010, wol.ActionNoop)
	authRule.Match.Auth = wol.AuthHMAC

	t.Run("auth without a key", func(t *testing.T) {
		t.Parallel()

		_, err := wol.NewRoutingPolicy([]wol.Rule{authRule}, testIfaces(), wol.PolicyOptions{})
		require.ErrorIs(t, err, wol.ErrAuthWithoutKey)
	})

	t.Run("unknown auth kind", func(t *testing.T) {
		t.Parallel()

		rule := plainRule(10010, wol.ActionNoop)
		rule.Match.Auth = "shared"

		_, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), wol.PolicyOptions{PacketKey: []byte("k")})
		require.ErrorIs(t, err, wol.ErrUnknownAuthKind)
	})

	t.Run("auth on a reserved port", func(t *testing.T) {
		t.Parallel()

		rule := plainRule(9, wol.ActionNoop)
		rule.Match.Auth = wol.AuthHMAC

		_, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), wol.PolicyOptions{PacketKey: []byte("k")})
		require.ErrorIs(t, err, wol.ErrReservedPortAction)
	})
}

// An authenticated rule outranks a plain one on the same port, so the signed packet is routed to
// the rule written for it.
func TestPolicyAuthBeatsPlainOnTheSamePort(t *testing.T) {
	t.Parallel()

	key := []byte("shared-key")

	authRule := plainRule(10010, wol.ActionReboot)
	authRule.Match.Auth = wol.AuthHMAC
	plain := plainRule(10010, wol.ActionNoop)

	policy, err := wol.NewRoutingPolicy([]wol.Rule{plain, authRule}, testIfaces(), wol.PolicyOptions{PacketKey: key})
	require.NoError(t, err)

	packet := wol.BuildMagicPacket(testMAC())
	signed := append(append([]byte{}, packet...), wol.SignPacket(key, packet)...)

	decision, ok := policy.Resolve(wol.Event{Payload: signed, DstPort: 10010})
	require.True(t, ok)
	require.Equal(t, wol.ActionReboot, decision.Action)
	require.True(t, decision.Authenticated)

	decision, ok = policy.Resolve(wol.Event{Payload: packet, DstPort: 10010})
	require.True(t, ok)
	require.Equal(t, wol.ActionNoop, decision.Action)
	require.False(t, decision.Authenticated)
}

// The tags of two packets are independent: a tag captured from one payload cannot authenticate
// another one.
func TestPolicyAuthRejectsSwappedTags(t *testing.T) {
	t.Parallel()

	key := []byte("shared-key")
	rule := plainRule(10010, wol.ActionNoop)
	rule.Match.Auth = wol.AuthHMAC

	policy, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), wol.PolicyOptions{PacketKey: key})
	require.NoError(t, err)

	first := append(wol.BuildMagicPacket(testMAC()), []byte("off")...)
	second := append(wol.BuildMagicPacket(testMAC()), []byte("other")...)

	forged := append(append([]byte{}, first...), wol.SignPacket(key, second)...)

	_, ok := policy.Resolve(wol.Event{Payload: forged, DstPort: 10010})
	require.False(t, ok)

	// SecureOn is covered by the tag as well.
	password := []byte("s3cret")
	secured, err := wol.EncodeMagicPacket(testMAC(), password)
	require.NoError(t, err)

	securedSigned := append(append([]byte{}, secured...), wol.SignPacket(key, secured)...)
	authSecure := plainRule(10011, wol.ActionNoop)
	authSecure.Match.Auth = wol.AuthHMAC

	withPassword, err := wol.NewRoutingPolicy([]wol.Rule{authSecure}, testIfaces(), wol.PolicyOptions{
		PacketKey: key, SecureOn: password,
	})
	require.NoError(t, err)

	decision, ok := withPassword.Resolve(wol.Event{Payload: securedSigned, DstPort: 10011})
	require.True(t, ok)
	require.True(t, decision.Authenticated)
	require.Equal(t, testMAC(), decision.TargetMAC)
}
