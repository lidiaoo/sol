package wol

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func cidrs(t *testing.T, values ...string) []*net.IPNet {
	t.Helper()

	out := make([]*net.IPNet, 0, len(values))

	for _, v := range values {
		_, network, err := net.ParseCIDR(v)
		require.NoError(t, err)

		out = append(out, network)
	}

	return out
}

func TestNetsIntersect(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		a, b string
		want bool
	}{
		"identical":            {"10.0.0.0/8", "10.0.0.0/8", true},
		"nested":               {"10.0.0.0/8", "10.1.0.0/16", true},
		"nested the other way": {"10.1.0.0/16", "10.0.0.0/8", true},
		"adjacent halves":      {"10.0.0.0/9", "10.128.0.0/9", false},
		"distant":              {"10.0.0.0/8", "192.168.0.0/16", false},
		"families differ":      {"10.0.0.0/8", "fd00::/8", false},
		"v6 nested":            {"fd00::/8", "fd00:1234::/32", true},
		"single host inside":   {"10.0.0.0/8", "10.1.2.3/32", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			a := cidrs(t, tc.a)[0]
			b := cidrs(t, tc.b)[0]
			require.Equal(t, tc.want, netsIntersect(a, b))
			require.Equal(t, tc.want, netsIntersect(b, a), "the answer must not depend on the order")
		})
	}
}

func TestSrcOverlap(t *testing.T) {
	t.Parallel()

	t.Run("an absent filter accepts everything", func(t *testing.T) {
		t.Parallel()

		require.True(t, srcOverlap(nil, cidrs(t, "10.0.0.0/8")))
		require.True(t, srcOverlap(cidrs(t, "10.0.0.0/8"), nil))
		require.True(t, srcOverlap(nil, nil))
	})

	t.Run("overlapping entries in a larger set still overlap", func(t *testing.T) {
		t.Parallel()

		// Neither set contains the other, but 10.1.0.0/16 sits in both: a packet from
		// 10.1.2.3 can reach either rule, so they are not distinguishable.
		require.True(t, srcOverlap(cidrs(t, "10.0.0.0/8"), cidrs(t, "10.1.0.0/16", "192.168.0.0/16")))
		require.True(t, srcOverlap(cidrs(t, "10.1.0.0/16", "192.168.0.0/16"), cidrs(t, "10.0.0.0/8")))
	})

	t.Run("disjoint sets", func(t *testing.T) {
		t.Parallel()

		require.False(t, srcOverlap(cidrs(t, "10.0.0.0/8", "172.16.0.0/12"), cidrs(t, "192.168.0.0/16")))
	})

	t.Run("same set in a different order", func(t *testing.T) {
		t.Parallel()

		require.True(t, srcOverlap(cidrs(t, "10.0.0.0/8", "192.168.0.0/16"), cidrs(t, "192.168.0.0/16", "10.0.0.0/8")))
	})
}
