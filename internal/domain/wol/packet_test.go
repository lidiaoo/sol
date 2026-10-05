package wol_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

func TestParsePacket(t *testing.T) {
	t.Parallel()

	mac := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}

	tests := []struct {
		name        string
		payload     []byte
		wantOK      bool
		wantContent string
	}{
		{
			name:    "plain packet",
			payload: wol.BuildMagicPacket(mac),
			wantOK:  true,
		},
		{
			name:        "packet with content",
			payload:     append(wol.BuildMagicPacket(mac), []byte("reboot")...),
			wantOK:      true,
			wantContent: "reboot",
		},
		{
			name:    "magic not at offset zero",
			payload: append([]byte{0x00, 0x01}, wol.BuildMagicPacket(mac)...),
			wantOK:  false,
		},
		{
			name:    "too short",
			payload: []byte{0xFF, 0xFF, 0xFF},
			wantOK:  false,
		},
		{
			name:    "inconsistent repetitions",
			payload: brokenPacket(mac),
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parsed, ok := wol.ParsePacket(tt.payload, nil)
			require.Equal(t, tt.wantOK, ok)

			if !tt.wantOK {
				return
			}

			require.Equal(t, mac, []byte(parsed.MAC))
			require.Equal(t, tt.wantContent, string(parsed.Content))
		})
	}
}

func TestParsePacketSecureOn(t *testing.T) {
	t.Parallel()

	mac := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	secureOn := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}

	tests := []struct {
		name        string
		payload     []byte
		secureOn    []byte
		wantOK      bool
		wantContent string
	}{
		{
			name:     "secureon packet",
			payload:  append(wol.BuildMagicPacket(mac), secureOn...),
			secureOn: secureOn,
			wantOK:   true,
		},
		{
			name:        "secureon packet with content",
			payload:     append(append(wol.BuildMagicPacket(mac), secureOn...), []byte("go")...),
			secureOn:    secureOn,
			wantOK:      true,
			wantContent: "go",
		},
		{
			name:     "secureon mismatch",
			payload:  append(wol.BuildMagicPacket(mac), secureOn...),
			secureOn: []byte{0x09, 0x09, 0x09, 0x09, 0x09, 0x09},
			wantOK:   false,
		},
		{
			name:     "secureon missing",
			payload:  wol.BuildMagicPacket(mac),
			secureOn: secureOn,
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parsed, ok := wol.ParsePacket(tt.payload, tt.secureOn)
			require.Equal(t, tt.wantOK, ok)

			if !tt.wantOK {
				return
			}

			require.Equal(t, mac, []byte(parsed.MAC))
			require.Equal(t, tt.wantContent, string(parsed.Content))
		})
	}
}

func brokenPacket(mac []byte) []byte {
	pkt := wol.BuildMagicPacket(mac)
	pkt[len(pkt)-1] ^= 0xFF

	return pkt
}

// TestEncodeMagicPacketRoundTrip is the sender's side of the contract: whatever wol.send
// transmits must be exactly what our own receiver accepts, with and without SecureOn.
func TestEncodeMagicPacketRoundTrip(t *testing.T) {
	t.Parallel()

	mac := []byte{0x58, 0x11, 0x22, 0xbc, 0x78, 0x66}
	secureOn := []byte("s3cret")

	plain, err := wol.EncodeMagicPacket(mac, nil)
	require.NoError(t, err)
	require.Len(t, plain, wol.PacketLenPlain)

	parsed, ok := wol.ParsePacket(plain, nil)
	require.True(t, ok)
	require.Equal(t, mac, []byte(parsed.MAC))
	require.Empty(t, parsed.Content)

	guarded, err := wol.EncodeMagicPacket(mac, secureOn)
	require.NoError(t, err)
	require.Len(t, guarded, wol.PacketLenSecureOn)
	require.Equal(t, plain, guarded[:wol.PacketLenPlain], "the password only appends")

	parsed, ok = wol.ParsePacket(guarded, secureOn)
	require.True(t, ok)
	require.Equal(t, mac, []byte(parsed.MAC))

	// A receiver that expects no password still accepts it: the password shows up as content.
	// That is why a SecureOn-protected target must also be configured with the password.
	parsed, ok = wol.ParsePacket(guarded, nil)
	require.True(t, ok)
	require.Equal(t, string(secureOn), string(parsed.Content))

	// A receiver with a different password refuses it.
	_, ok = wol.ParsePacket(guarded, []byte("wrong!"))
	require.False(t, ok)
}

func TestEncodeMagicPacketRejects(t *testing.T) {
	t.Parallel()

	_, err := wol.EncodeMagicPacket([]byte{0x01, 0x02}, nil)
	require.ErrorIs(t, err, wol.ErrMACLength)

	_, err = wol.EncodeMagicPacket(make([]byte, 8), nil)
	require.ErrorIs(t, err, wol.ErrMACLength)

	_, err = wol.EncodeMagicPacket([]byte{0x58, 0x11, 0x22, 0xbc, 0x78, 0x66}, []byte("short"))
	require.ErrorIs(t, err, wol.ErrSecureOnLength)
}
