package wol_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

func TestParsePacketAny(t *testing.T) {
	t.Parallel()

	mac := testMAC()
	plain := wol.BuildMagicPacket(mac)
	password := []byte("secret")
	other := []byte("other!")

	t.Run("no candidate matches, so it is read as plain", func(t *testing.T) {
		t.Parallel()

		parsed, ok := wol.ParsePacketAny(append(plain, other...), [][]byte{password})
		require.True(t, ok)
		require.Nil(t, parsed.SecureOn)
		require.Equal(t, string(other), string(parsed.Content), "the bytes stay in the content")
	})

	t.Run("the matching candidate is recorded and removed from the content", func(t *testing.T) {
		t.Parallel()

		parsed, ok := wol.ParsePacketAny(append(plain, append(password, "extra"...)...), [][]byte{other, password})
		require.True(t, ok)
		require.Equal(t, string(password), string(parsed.SecureOn))
		require.Equal(t, "extra", string(parsed.Content))
	})

	t.Run("a plain payload with candidates configured", func(t *testing.T) {
		t.Parallel()

		parsed, ok := wol.ParsePacketAny(plain, [][]byte{password})
		require.True(t, ok)
		require.Nil(t, parsed.SecureOn)
		require.Empty(t, parsed.Content)
	})

	t.Run("a payload too short to carry a password", func(t *testing.T) {
		t.Parallel()

		parsed, ok := wol.ParsePacketAny(append(plain, "abc"...), [][]byte{password})
		require.True(t, ok)
		require.Nil(t, parsed.SecureOn)
		require.Equal(t, "abc", string(parsed.Content))
	})

	t.Run("a broken magic header is not a packet at all", func(t *testing.T) {
		t.Parallel()

		broken := append([]byte(nil), plain...)
		broken[3] = 0x00

		_, ok := wol.ParsePacketAny(broken, [][]byte{password})
		require.False(t, ok)
	})
}

func TestRoutingPolicyPerRuleSecureOn(t *testing.T) {
	t.Parallel()

	password := []byte("secret")
	other := []byte("other!")

	passwordRule := plainRule(8, wol.ActionShutdown)
	passwordRule.Match.SecureOn = password

	otherRule := plainRule(8, wol.ActionReboot)
	otherRule.Match.SecureOn = other

	// The same scope, the same port and the same content, told apart by the password alone: a
	// payload carries exactly one of them, so the two rules never match the same packet.
	policy, err := wol.NewRoutingPolicy([]wol.Rule{passwordRule, otherRule}, testIfaces(), wol.PolicyOptions{})
	require.NoError(t, err)

	magic := wol.BuildMagicPacket(testMAC())

	for name, tc := range map[string]struct {
		payload []byte
		want    wol.Action
		matched bool
	}{
		"the first password":  {append(magic, password...), wol.ActionShutdown, true},
		"the second password": {append(magic, other...), wol.ActionReboot, true},
		"a wrong password":    {append(magic, "wrong!"...), "", false},
		"no password":         {magic, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			decision, matched := policy.Resolve(wol.Event{Payload: tc.payload, DstPort: 8})
			require.Equal(t, tc.matched, matched)
			require.Equal(t, tc.want, decision.Action)
		})
	}
}

func TestRoutingPolicySecureOnInheritance(t *testing.T) {
	t.Parallel()

	global := []byte("global")
	block := []byte("block!")

	inherits := plainRule(8, wol.ActionShutdown)
	overrides := plainRule(10, wol.ActionReboot)
	overrides.Match.SecureOn = block

	none := plainRule(12, wol.ActionSleep)
	none.Match.SecureOn = []byte{}

	rules := []wol.Rule{inherits, overrides, none}

	policy, err := wol.NewRoutingPolicy(rules, testIfaces(), wol.PolicyOptions{SecureOn: global})
	require.NoError(t, err)

	magic := wol.BuildMagicPacket(testMAC())

	t.Run("a rule without its own password uses the default", func(t *testing.T) {
		t.Parallel()

		decision, matched := policy.Resolve(wol.Event{Payload: append(magic, global...), DstPort: 8})
		require.True(t, matched)
		require.Equal(t, wol.ActionShutdown, decision.Action)

		_, matched = policy.Resolve(wol.Event{Payload: magic, DstPort: 8})
		require.False(t, matched, "the default is required, not optional")
	})

	t.Run("a rule can override the default", func(t *testing.T) {
		t.Parallel()

		decision, matched := policy.Resolve(wol.Event{Payload: append(magic, block...), DstPort: 10})
		require.True(t, matched)
		require.Equal(t, wol.ActionReboot, decision.Action)

		_, matched = policy.Resolve(wol.Event{Payload: append(magic, global...), DstPort: 10})
		require.False(t, matched, "the default does not open the rule")
	})

	t.Run("an explicitly empty password opts out of the default", func(t *testing.T) {
		t.Parallel()

		decision, matched := policy.Resolve(wol.Event{Payload: magic, DstPort: 12})
		require.True(t, matched)
		require.Equal(t, wol.ActionSleep, decision.Action)

		_, matched = policy.Resolve(wol.Event{Payload: append(magic, global...), DstPort: 12})
		require.False(t, matched)
	})
}

func TestRoutingPolicySecureOnRejectsReservedPorts(t *testing.T) {
	t.Parallel()

	for name, mangle := range map[string]func(rule *wol.Rule){
		"on the rule":         func(rule *wol.Rule) { rule.Match.SecureOn = []byte("secret") },
		"through the default": func(_ *wol.Rule) {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rule := plainRule(9, wol.ActionNoop)
			mangle(&rule)

			opts := wol.PolicyOptions{}
			if name == "through the default" {
				opts.SecureOn = []byte("secret")
			}

			_, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), opts)
			require.ErrorIs(t, err, wol.ErrReservedPortAction)
		})
	}
}

func TestRoutingPolicySecureOnLength(t *testing.T) {
	t.Parallel()

	rule := plainRule(8, wol.ActionShutdown)
	rule.Match.SecureOn = []byte("short")

	_, err := wol.NewRoutingPolicy([]wol.Rule{rule}, testIfaces(), wol.PolicyOptions{})
	require.ErrorIs(t, err, wol.ErrSecureOnLength)
}
