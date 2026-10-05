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
