package wol

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stampMAC is the target whose magic packet carries the signed segments below.
func stampMAC() net.HardwareAddr {
	return net.HardwareAddr{0x58, 0x11, 0x22, 0xbc, 0x78, 0x66}
}

func TestSplitTimestampedRemoteContent(t *testing.T) {
	t.Parallel()

	key := []byte("command-key")
	prefix := BuildMagicPacket(stampMAC())
	segment := []byte("backup:target=home")
	at := time.Unix(1_700_000_000, 0)

	content := append(append([]byte{}, segment...), RemoteStampSignature(key, prefix, segment, at)...)

	gotSegment, stamp, tag, err := SplitTimestampedRemoteContent(key, prefix, content)
	require.NoError(t, err)
	require.Equal(t, segment, gotSegment)
	require.Equal(t, at.Unix(), stamp)
	require.Len(t, tag, RemoteSignatureLen)

	// Refusals, all of them fail closed.
	refuse := func(key, content []byte) {
		segment, stamp, tag, err := SplitTimestampedRemoteContent(key, prefix, content)
		require.ErrorIs(t, err, ErrRemoteSignature)
		require.Nil(t, segment)
		require.Zero(t, stamp)
		require.Nil(t, tag)
	}

	// The stamp is authenticated: moving it forward while keeping the old tag breaks it.
	shifted := append(append([]byte{}, segment...), TimestampBytes(at.Add(time.Minute))...)
	shifted = append(shifted, RemoteStampSignature(key, prefix, segment, at)[TimestampLen:]...)
	refuse(key, shifted)

	refuse([]byte("another-key"), content)
	refuse(key, append(append([]byte{}, segment...), []byte("eleven byte")...))
	refuse(key, []byte("too short"))

	// Without a key the transport is unsigned (the authenticated HTTP path): the segment comes
	// back and the caller decides what to do with an empty key.
	unsigned, _, _, err := SplitTimestampedRemoteContent(nil, prefix, content)
	require.NoError(t, err)
	require.Equal(t, segment, unsigned)
}

func TestRemoteStampSignatureCoversTheStamp(t *testing.T) {
	t.Parallel()

	key := []byte("command-key")
	prefix := BuildMagicPacket(stampMAC())
	segment := []byte("backup:target=home")
	at := time.Unix(1_700_000_000, 0)

	stamped := RemoteStampSignature(key, prefix, segment, at)
	require.Len(t, stamped, TimestampLen+RemoteSignatureLen)
	require.Equal(t, TimestampBytes(at), stamped[:TimestampLen], "the payload is stamp||tag")

	// The same segment with a different stamp carries a different tag - which is what makes the
	// window meaningful rather than decorative.
	later := RemoteStampSignature(key, prefix, segment, at.Add(time.Second))
	require.NotEqual(t, stamped[TimestampLen:], later[TimestampLen:])
}
