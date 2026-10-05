package wol_test

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

func testMAC() net.HardwareAddr {
	return net.HardwareAddr{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
}

func testIfaces() []wol.IfaceInfo {
	return []wol.IfaceInfo{{Name: "eth0", MAC: testMAC(), IPs: []net.IP{net.IPv4(192, 168, 1, 10)}}}
}

func plainRule(port int, action wol.Action) wol.Rule {
	return wol.Rule{
		Match:  wol.Match{Ports: []int{port}, MAC: wol.MACSelector{Kind: wol.MACSelf}},
		Action: action,
	}
}

func contentRule(port int, kind wol.ContentKind, value string, action wol.Action) wol.Rule {
	rule := plainRule(port, action)
	rule.Match.Content = wol.ContentMatcher{Kind: kind, Value: value}

	return rule
}

func TestNewRoutingPolicy(t *testing.T) {
	t.Parallel()

	t.Run("single rule", func(t *testing.T) {
		t.Parallel()

		_, err := wol.NewRoutingPolicy([]wol.Rule{plainRule(8, wol.ActionShutdown)}, testIfaces(), wol.PolicyOptions{})
		require.NoError(t, err)
	})

	t.Run("multiple ports", func(t *testing.T) {
		t.Parallel()

		rules := []wol.Rule{plainRule(8, wol.ActionShutdown), plainRule(10, wol.ActionReboot)}
		_, err := wol.NewRoutingPolicy(rules, testIfaces(), wol.PolicyOptions{})
		require.NoError(t, err)
	})

	t.Run("content beats plain on the same port", func(t *testing.T) {
		t.Parallel()

		rules := []wol.Rule{plainRule(8, wol.ActionShutdown), contentRule(8, wol.ContentSuffix, "reboot", wol.ActionReboot)}
		_, err := wol.NewRoutingPolicy(rules, testIfaces(), wol.PolicyOptions{})
		require.NoError(t, err)
	})

	t.Run("same content on different ports is fine", func(t *testing.T) {
		t.Parallel()

		rules := []wol.Rule{
			contentRule(8, wol.ContentSuffix, "a", wol.ActionReboot),
			contentRule(10, wol.ContentPrefix, "b", wol.ActionShutdown),
		}
		_, err := wol.NewRoutingPolicy(rules, testIfaces(), wol.PolicyOptions{})
		require.NoError(t, err)
	})
}

func TestNewRoutingPolicyReservedPorts(t *testing.T) {
	t.Parallel()

	t.Run("accepts noop", func(t *testing.T) {
		t.Parallel()

		_, err := wol.NewRoutingPolicy([]wol.Rule{plainRule(9, wol.ActionNoop)}, testIfaces(), wol.PolicyOptions{})
		require.NoError(t, err)
	})

	t.Run("rejects non noop", func(t *testing.T) {
		t.Parallel()

		_, err := wol.NewRoutingPolicy([]wol.Rule{plainRule(9, wol.ActionShutdown)}, testIfaces(), wol.PolicyOptions{})
		require.ErrorIs(t, err, wol.ErrReservedPortAction)
	})

	t.Run("rejects content", func(t *testing.T) {
		t.Parallel()

		rule := contentRule(9, wol.ContentSuffix, "x", wol.ActionNoop)

		_, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), wol.PolicyOptions{})
		require.ErrorIs(t, err, wol.ErrReservedPortAction)
	})

	t.Run("allow reserved override", func(t *testing.T) {
		t.Parallel()

		opts := wol.PolicyOptions{AllowReserved: true}
		_, err := wol.NewRoutingPolicy([]wol.Rule{plainRule(9, wol.ActionShutdown)}, testIfaces(), opts)
		require.NoError(t, err)
	})

	t.Run("custom reserved ports", func(t *testing.T) {
		t.Parallel()

		opts := wol.PolicyOptions{ReservedPorts: []int{10}}
		_, err := wol.NewRoutingPolicy([]wol.Rule{plainRule(9, wol.ActionShutdown)}, testIfaces(), opts)
		require.NoError(t, err)
	})
}

func TestNewRoutingPolicyRejectsRules(t *testing.T) {
	t.Parallel()

	t.Run("unknown action reference", func(t *testing.T) {
		t.Parallel()

		_, err := wol.NewRoutingPolicy([]wol.Rule{plainRule(8, wol.Action("exec"))}, testIfaces(), wol.PolicyOptions{})
		require.ErrorIs(t, err, wol.ErrUnknownActionRef)
	})

	t.Run("duplicate port", func(t *testing.T) {
		t.Parallel()

		rules := []wol.Rule{plainRule(8, wol.ActionShutdown), plainRule(8, wol.ActionReboot)}
		_, err := wol.NewRoutingPolicy(rules, testIfaces(), wol.PolicyOptions{})
		require.ErrorIs(t, err, wol.ErrDuplicatePort)
	})

	t.Run("ambiguous prefix versus suffix", func(t *testing.T) {
		t.Parallel()

		rules := []wol.Rule{
			contentRule(8, wol.ContentSuffix, "a", wol.ActionReboot),
			contentRule(8, wol.ContentPrefix, "b", wol.ActionShutdown),
		}
		_, err := wol.NewRoutingPolicy(rules, testIfaces(), wol.PolicyOptions{})
		require.ErrorIs(t, err, wol.ErrAmbiguousRule)
	})
}

func TestNewRoutingPolicyRejectsScope(t *testing.T) {
	t.Parallel()

	t.Run("unknown interface", func(t *testing.T) {
		t.Parallel()

		rule := plainRule(8, wol.ActionShutdown)
		rule.Match.MAC = wol.MACSelector{Kind: wol.MACInterface, Ifaces: []string{"wlan0"}}

		_, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), wol.PolicyOptions{})
		require.ErrorIs(t, err, wol.ErrUnknownInterface)
	})

	t.Run("duplicate interface", func(t *testing.T) {
		t.Parallel()

		ifaces := []wol.IfaceInfo{{Name: "eth0", MAC: testMAC()}, {Name: "eth0", MAC: testMAC()}}

		_, err := wol.NewRoutingPolicy([]wol.Rule{plainRule(8, wol.ActionShutdown)}, ifaces, wol.PolicyOptions{})
		require.ErrorIs(t, err, wol.ErrDuplicateInterface)
	})

	t.Run("invalid port", func(t *testing.T) {
		t.Parallel()

		_, err := wol.NewRoutingPolicy([]wol.Rule{plainRule(0, wol.ActionShutdown)}, testIfaces(), wol.PolicyOptions{})
		require.ErrorIs(t, err, wol.ErrInvalidPort)
	})

	t.Run("mac and interfaces conflict", func(t *testing.T) {
		t.Parallel()

		rule := plainRule(8, wol.ActionShutdown)
		rule.Match.MAC = wol.MACSelector{
			Kind:    wol.MACExplicit,
			Address: "11:22:33:44:55:66",
			Ifaces:  []string{"eth0"},
		}

		_, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), wol.PolicyOptions{})
		require.ErrorIs(t, err, wol.ErrMACConflict)
	})

	t.Run("invalid cidr", func(t *testing.T) {
		t.Parallel()

		rule := plainRule(8, wol.ActionShutdown)
		rule.Match.SrcCIDRs = []string{"not-a-cidr"}

		_, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), wol.PolicyOptions{})
		require.ErrorIs(t, err, wol.ErrInvalidCIDR)
	})
}

func TestRoutingPolicyResolve(t *testing.T) {
	t.Parallel()

	content := contentRule(8, wol.ContentSuffix, "reboot", wol.ActionReboot)
	rules := []wol.Rule{plainRule(9, wol.ActionNoop), plainRule(8, wol.ActionShutdown), content}

	policy, err := wol.NewRoutingPolicy(rules, testIfaces(), wol.PolicyOptions{})
	require.NoError(t, err)

	magic := wol.BuildMagicPacket(testMAC())

	tests := []struct {
		name    string
		payload []byte
		port    int
		wantAct wol.Action
		want    bool
	}{
		{name: "plain shutdown", payload: magic, port: 8, wantAct: wol.ActionShutdown, want: true},
		{name: "content selects reboot", payload: append(magic, "reboot"...), port: 8, wantAct: wol.ActionReboot, want: true},
		{name: "content unmatched", payload: append(magic, "sleep"...), port: 8, wantAct: "", want: false},
		{name: "reserved port noop", payload: magic, port: 9, wantAct: wol.ActionNoop, want: true},
		{name: "unlisted port", payload: magic, port: 10, wantAct: "", want: false},
		{name: "non magic payload", payload: []byte("hello"), port: 8, wantAct: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decision, matched := policy.Resolve(wol.Event{Payload: tt.payload, DstPort: tt.port})
			require.Equal(t, tt.want, matched)
			require.Equal(t, tt.wantAct, decision.Action)
		})
	}
}

func TestRoutingPolicyResolveSrcCIDR(t *testing.T) {
	t.Parallel()

	magic := wol.BuildMagicPacket(testMAC())

	rule := plainRule(6, wol.ActionShutdown)
	rule.Match.SrcCIDRs = []string{"10.0.0.0/24"}

	policy, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), wol.PolicyOptions{})
	require.NoError(t, err)

	act, matched := policy.Resolve(wol.Event{Payload: magic, DstPort: 6, SrcIP: net.IPv4(10, 0, 0, 5)})
	require.True(t, matched)
	require.Equal(t, wol.ActionShutdown, act.Action)

	_, matched = policy.Resolve(wol.Event{Payload: magic, DstPort: 6, SrcIP: net.IPv4(192, 168, 1, 5)})
	require.False(t, matched)
}

func TestRoutingPolicyResolveExplicitMAC(t *testing.T) {
	t.Parallel()

	other := net.HardwareAddr{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}

	rule := plainRule(8, wol.ActionNoop)
	rule.Match.MAC = wol.MACSelector{Kind: wol.MACExplicit, Address: other.String()}

	policy, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), wol.PolicyOptions{})
	require.NoError(t, err)

	_, matched := policy.Resolve(wol.Event{Payload: wol.BuildMagicPacket(testMAC()), DstPort: 8})
	require.False(t, matched)

	act, matched := policy.Resolve(wol.Event{Payload: wol.BuildMagicPacket(other), DstPort: 8})
	require.True(t, matched)
	require.Equal(t, wol.ActionNoop, act.Action)
}

func TestNewRoutingPolicySecureOnLength(t *testing.T) {
	t.Parallel()

	_, err := wol.NewRoutingPolicy(
		[]wol.Rule{plainRule(8, wol.ActionShutdown)},
		testIfaces(),
		wol.PolicyOptions{SecureOn: []byte("12345")},
	)
	require.ErrorIs(t, err, wol.ErrSecureOnLength)

	_, err = wol.NewRoutingPolicy(
		[]wol.Rule{plainRule(8, wol.ActionShutdown)},
		testIfaces(),
		wol.PolicyOptions{SecureOn: []byte("123456")},
	)
	require.NoError(t, err)
}

func TestRoutingPolicyResolveDryRun(t *testing.T) {
	t.Parallel()

	rule := plainRule(11, wol.ActionReboot)
	rule.DryRun = true

	policy, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), wol.PolicyOptions{})
	require.NoError(t, err)

	decision, matched := policy.Resolve(wol.Event{Payload: wol.BuildMagicPacket(testMAC()), DstPort: 11})
	require.True(t, matched)
	require.Equal(t, wol.ActionReboot, decision.Action)
	require.True(t, decision.DryRun)

	plain := plainRule(12, wol.ActionReboot)

	dryPolicy, err := wol.NewRoutingPolicy([]wol.Rule{plain}, testIfaces(), wol.PolicyOptions{})
	require.NoError(t, err)

	decision, matched = dryPolicy.Resolve(wol.Event{Payload: wol.BuildMagicPacket(testMAC()), DstPort: 12})
	require.True(t, matched)
	require.False(t, decision.DryRun)
}

func TestRoutingPolicyPortsAndRules(t *testing.T) {
	t.Parallel()

	rules := []wol.Rule{plainRule(8, wol.ActionShutdown), plainRule(10, wol.ActionReboot)}

	policy, err := wol.NewRoutingPolicy(rules, testIfaces(), wol.PolicyOptions{})
	require.NoError(t, err)

	require.ElementsMatch(t, []int{8, 10}, policy.Ports())
	require.Len(t, policy.Rules(), 2)
}
