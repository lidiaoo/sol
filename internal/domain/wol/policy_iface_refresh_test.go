package wol_test

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

// TestSetInterfacesFollowsTheMachine covers the live identity refresh (§17.2): the NICs that
// answer for `mac: self` are the ones the machine has now, not the ones it had at start-up.
func TestSetInterfacesFollowsTheMachine(t *testing.T) {
	t.Parallel()

	wired := wol.IfaceInfo{Name: "enp6s0", MAC: testMAC()}
	wifi := wol.IfaceInfo{Name: "wlp5s0", MAC: net.HardwareAddr{0xF0, 0xD4, 0x15, 0x57, 0x9C, 0xC5}}
	packet := wol.Event{Payload: wol.BuildMagicPacket(wifi.MAC), DstPort: 10030}

	policy, err := wol.NewRoutingPolicy(
		[]wol.Rule{plainRule(10030, wol.ActionNoop)},
		[]wol.IfaceInfo{wired},
		wol.PolicyOptions{},
	)
	require.NoError(t, err)

	_, matched := policy.Resolve(packet)
	require.False(t, matched, "the wireless NIC was not part of the start-up identity")

	changed, err := policy.SetInterfaces([]wol.IfaceInfo{wired, wifi})
	require.NoError(t, err)
	require.True(t, changed)

	decision, matched := policy.Resolve(packet)
	require.True(t, matched, "a NIC that appears later must start matching")
	require.Equal(t, "wlp5s0", decision.Interface)

	changed, err = policy.SetInterfaces([]wol.IfaceInfo{wired, wifi})
	require.NoError(t, err)
	require.False(t, changed, "the same set is not a change")

	changed, err = policy.SetInterfaces([]wol.IfaceInfo{wired})
	require.NoError(t, err)
	require.True(t, changed)

	_, matched = policy.Resolve(packet)
	require.False(t, matched, "a NIC that went away must stop matching")
}

// TestSetInterfacesPicksUpAChangedAddress is the case that started this: while a NIC is down the
// kernel can report a placeholder address, and the address it actually uses appears when it comes
// up. Keeping only the placeholder would mean the machine could never be woken over that link.
func TestSetInterfacesPicksUpAChangedAddress(t *testing.T) {
	t.Parallel()

	placeholder := net.HardwareAddr{0x82, 0x44, 0x59, 0xFF, 0x68, 0x48}
	actual := net.HardwareAddr{0xF0, 0xD4, 0x15, 0x57, 0x9C, 0xC5}

	policy, err := wol.NewRoutingPolicy(
		[]wol.Rule{plainRule(10030, wol.ActionNoop)},
		[]wol.IfaceInfo{{Name: "wlp5s0", MAC: placeholder}},
		wol.PolicyOptions{},
	)
	require.NoError(t, err)

	changed, err := policy.SetInterfaces([]wol.IfaceInfo{{Name: "wlp5s0", MAC: actual}})
	require.NoError(t, err)
	require.True(t, changed)

	_, matched := policy.Resolve(wol.Event{Payload: wol.BuildMagicPacket(actual), DstPort: 10030})
	require.True(t, matched, "the address the NIC actually uses must match")

	_, matched = policy.Resolve(wol.Event{Payload: wol.BuildMagicPacket(placeholder), DstPort: 10030})
	require.False(t, matched, "the placeholder it had while down must not")
}

// TestSetInterfacesToleratesAnAbsentNamedInterface keeps a transient unplug from failing the
// refresh: the rule keeps its name and matches nothing until the NIC is back. At start-up the same
// name is refused, because there it is far more likely to be a typo than a dock that is not
// plugged in yet.
func TestSetInterfacesToleratesAnAbsentNamedInterface(t *testing.T) {
	t.Parallel()

	wired := wol.IfaceInfo{Name: "enp6s0", MAC: testMAC()}
	wifi := wol.IfaceInfo{Name: "wlp5s0", MAC: net.HardwareAddr{0xF0, 0xD4, 0x15, 0x57, 0x9C, 0xC5}}
	rule := wol.Rule{
		Match: wol.Match{
			Ports: []int{10030},
			MAC:   wol.MACSelector{Kind: wol.MACInterface, Ifaces: []string{"wlp5s0"}},
		},
		Action: wol.ActionNoop,
	}

	_, err := wol.NewRoutingPolicy([]wol.Rule{rule}, []wol.IfaceInfo{wired}, wol.PolicyOptions{})
	require.ErrorIs(t, err, wol.ErrUnknownInterface, "start-up still refuses a name that is not here")

	policy, err := wol.NewRoutingPolicy([]wol.Rule{rule}, []wol.IfaceInfo{wired, wifi}, wol.PolicyOptions{})
	require.NoError(t, err)

	changed, err := policy.SetInterfaces([]wol.IfaceInfo{wired})
	require.NoError(t, err, "a NIC that went away must not fail the refresh")
	require.True(t, changed)

	_, matched := policy.Resolve(wol.Event{Payload: wol.BuildMagicPacket(wifi.MAC), DstPort: 10030})
	require.False(t, matched)

	changed, err = policy.SetInterfaces([]wol.IfaceInfo{wired, wifi})
	require.NoError(t, err)
	require.True(t, changed)

	_, matched = policy.Resolve(wol.Event{Payload: wol.BuildMagicPacket(wifi.MAC), DstPort: 10030})
	require.True(t, matched, "plugging it back in must restore the match")
}
