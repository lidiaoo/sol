package wol_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

func TestSignAndSplitPacket(t *testing.T) {
	t.Parallel()

	key := []byte("shared-key")
	packet := wol.BuildMagicPacket(testMAC())

	signed := append(append([]byte{}, packet...), wol.SignPacket(key, packet)...)
	require.Len(t, signed, len(packet)+wol.PacketSignatureLen)

	data, ok := wol.SplitPacketSignature(key, signed)
	require.True(t, ok)
	require.Equal(t, packet, data)

	// A wrong key, a flipped byte and a truncated payload are all unauthenticated, and the
	// payload comes back untouched so the caller can treat it as a plain packet.
	_, ok = wol.SplitPacketSignature([]byte("other-key"), signed)
	require.False(t, ok)

	tampered := append([]byte{}, signed...)
	tampered[len(packet)] ^= 0xFF
	_, ok = wol.SplitPacketSignature(key, tampered)
	require.False(t, ok)

	_, ok = wol.SplitPacketSignature(key, packet[:wol.PacketSignatureLen])
	require.False(t, ok)

	// No key configured means nothing is ever authenticated.
	_, ok = wol.SplitPacketSignature(nil, signed)
	require.False(t, ok)
}

// The tag covers the SecureOn password too: it is part of the packet the receiver verifies.
func TestSignedPacketWithSecureOn(t *testing.T) {
	t.Parallel()

	key := []byte("shared-key")
	password := []byte("s3cret")

	packet, err := wol.EncodeMagicPacket(testMAC(), password)
	require.NoError(t, err)

	signed := append(append([]byte{}, packet...), wol.SignPacket(key, packet)...)

	data, ok := wol.SplitPacketSignature(key, signed)
	require.True(t, ok)
	require.Equal(t, packet, data)
}
