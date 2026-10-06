package network

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

func TestIsVirtualName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want bool
	}{
		{name: "eth0", want: false},
		{name: "enp6s0", want: false},
		{name: "wlan0", want: false},
		{name: "wlp5s0", want: false},
		{name: "docker0", want: true},
		{name: "veth456d809", want: true},
		{name: "virbr0", want: true},
		{name: "br-bcaca6b4f69d", want: true},
		{name: "tailscale0", want: true},
		{name: "wg0", want: true},
		{name: "podman0", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, isVirtualName(tt.name))
		})
	}
}

func TestEligible(t *testing.T) {
	t.Parallel()

	mac := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}

	all := []wol.IfaceInfo{
		{Name: "eth0", MAC: mac, Up: true, Eligible: true},
		{Name: "lo", MAC: mac, Up: true, Loopback: true, Eligible: false},
		{Name: "wlan0", MAC: mac, Up: false, Eligible: true},
		{Name: "docker0", MAC: mac, Up: true, Virtual: true, Eligible: false},
		{Name: "enp6s0", MAC: mac, Up: true, Eligible: true},
	}

	got := eligible(all)
	require.Len(t, got, 3)
	require.Equal(t, "eth0", got[0].Name)
	require.Equal(t, "wlan0", got[1].Name)
	require.Equal(t, "enp6s0", got[2].Name)
}

// TestEligibleInfoIsAboutIdentityNotAvailability pins the criterion that the wireless and
// hot-plug cases depend on: a NIC that is down is still part of this machine, and only loopback,
// virtual names and address-less entries are excluded.
func TestEligibleInfoIsAboutIdentityNotAvailability(t *testing.T) {
	t.Parallel()

	mac := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}

	tests := []struct {
		name string
		info wol.IfaceInfo
		want bool
	}{
		{name: "an up wired NIC", info: wol.IfaceInfo{Name: "enp6s0", MAC: mac, Up: true}, want: true},
		{name: "a down wired NIC", info: wol.IfaceInfo{Name: "enp6s0", MAC: mac}, want: true},
		{name: "a down wireless NIC", info: wol.IfaceInfo{Name: "wlp5s0", MAC: mac}, want: true},
		{name: "loopback", info: wol.IfaceInfo{Name: "lo", MAC: mac, Up: true, Loopback: true}, want: false},
		{name: "a virtual NIC", info: wol.IfaceInfo{Name: "docker0", MAC: mac, Up: true, Virtual: true}, want: false},
		{name: "a NIC without a hardware address", info: wol.IfaceInfo{Name: "eth0", Up: true}, want: false},
		{name: "a truncated hardware address", info: wol.IfaceInfo{Name: "eth0", MAC: mac[:4], Up: true}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, eligibleInfo(tc.info))
		})
	}
}

func TestPickUnknownInterface(t *testing.T) {
	t.Parallel()

	all := []wol.IfaceInfo{{Name: "eth0", MAC: []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}}}

	_, err := pick(all, []string{"wlan0"})
	require.ErrorIs(t, err, wol.ErrUnknownInterface)
}

func TestPickDuplicateInterface(t *testing.T) {
	t.Parallel()

	all := []wol.IfaceInfo{{Name: "eth0", MAC: []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}}}

	_, err := pick(all, []string{"eth0", "eth0"})
	require.ErrorIs(t, err, wol.ErrDuplicateInterface)
}

func TestPickInterfaceWithoutMAC(t *testing.T) {
	t.Parallel()

	all := []wol.IfaceInfo{{Name: "tun0"}}

	_, err := pick(all, []string{"tun0"})
	require.ErrorIs(t, err, ErrNoMACAddress)
}
