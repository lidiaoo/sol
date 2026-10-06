package outbound

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

func TestAllowlistMatching(t *testing.T) {
	internal := `~^https://[a-z0-9-]+\.internal\.example\.com/`

	tests := []struct {
		name  string
		entry string
		url   string
		want  bool
	}{
		{"prefix allows a path under the host", "https://hooks.example.com", "https://hooks.example.com/hook/a", true},
		{"prefix allows the bare host", "https://hooks.example.com", "https://hooks.example.com", true},
		{"prefix does not leak into a lookalike host", "https://hooks.example.com", "https://hooks.example.comevil.com/hook", false},
		{"an entry path keeps its boundary", "https://api.example.com/v1", "https://api.example.com/v10/x", false},
		{"an entry path covers its subtree", "https://api.example.com/v1", "https://api.example.com/v1/x", true},
		{"the scheme is part of the match", "https://hooks.example.com", "http://hooks.example.com/hook", false},
		{"a portless entry does not cover a port", "https://hooks.example.com", "https://hooks.example.com:8443/hook", false},
		{"exact accepts only that URL", "=https://api.example.com/v1/notify", "https://api.example.com/v1/notify", true},
		{"exact rejects a longer path", "=https://api.example.com/v1/notify", "https://api.example.com/v1/notify/extra", false},
		{"exact rejects a query string", "=https://api.example.com/v1/notify", "https://api.example.com/v1/notify?a=1", false},
		{"regex matches the internal domain", internal, "https://node-1.internal.example.com/hook", true},
		{"regex rejects a suffix attack", internal, "https://node-1.internal.example.com.evil.net/hook", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := parseAllowlist([]string{tc.entry})
			require.NoError(t, err)
			require.Len(t, entries, 1)

			full, err := checkURL(tc.url)
			require.NoError(t, err)

			target, err := url.Parse(full)
			require.NoError(t, err)

			require.Equal(t, tc.want, entries[0].match(full, target))
		})
	}
}

func TestParseAllowlistRejectsMalformedEntries(t *testing.T) {
	for _, entry := range []string{"", "   ", "=ftp://example.com", "~[", "hooks.example.com"} {
		t.Run(entry, func(t *testing.T) {
			_, err := parseAllowlist([]string{entry})
			require.ErrorIs(t, err, ErrAllowlistEntry)
		})
	}
}

func TestExecutorAllowlist(t *testing.T) {
	executor := NewExecutor([]string{"https://hooks.example.com/"})
	require.NoError(t, executor.checkAllowlist("https://hooks.example.com/hook/x"))
	require.ErrorIs(t, executor.checkAllowlist("https://hooks.example.com.evil.net/hook/x"), ErrURLNotAllowed)

	// No allowlist configured stays the documented "any http(s) host".
	open := NewExecutor(nil)
	require.NoError(t, open.checkAllowlist("https://anything.example.com/"))

	// A malformed entry is reported at startup (Validate) and denies everything until
	// it is fixed, rather than silently allowing the request.
	broken := NewExecutor([]string{"~["})
	require.ErrorIs(t, broken.checkAllowlist("https://hooks.example.com/"), ErrAllowlistEntry)

	def := wol.ActionDef{Name: "notify", Type: wol.ActionTypeHTTP, HTTP: &wol.HTTPParams{
		URL: "https://hooks.example.com/hook",
	}}
	require.ErrorIs(t, broken.Validate(def), ErrAllowlistEntry)
	require.NoError(t, NewExecutor([]string{"https://hooks.example.com/"}).Validate(def))
}
