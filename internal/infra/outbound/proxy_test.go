package outbound_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
	"github.com/lidiaoo/sol/internal/infra/outbound"
)

// TestProxyCarriesTheRequest proves the request really goes through the configured proxy: the
// destination is a closed local port, so only a request that the proxy answered can succeed.
func TestProxyCarriesTheRequest(t *testing.T) {
	t.Parallel()

	var seen atomic.Value

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen.Store(req.Method + " " + req.Host + " " + req.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()

	def := wol.ActionDef{
		Name: "hook",
		Type: wol.ActionTypeHTTP,
		HTTP: &wol.HTTPParams{
			URL:   "http://127.0.0.1:1/hook",
			Proxy: proxy.URL,
		},
	}

	executor := outbound.NewExecutor(nil)
	require.NoError(t, executor.Validate(def))
	require.NoError(t, executor.Execute(context.Background(), def, testEvent()))

	require.Equal(t, "POST 127.0.0.1:1 /hook", seen.Load())
}

// TestProxyDoesNotBypassTheAllowlist is the security half: with a proxy in place the destination
// is still checked against security.url_allowlist, and a refused destination never reaches the
// proxy (a proxy must not become a way around the allowlist).
func TestProxyDoesNotBypassTheAllowlist(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()

	def := wol.ActionDef{
		Name: "hook",
		Type: wol.ActionTypeHTTP,
		HTTP: &wol.HTTPParams{
			URL:   "http://evil.example/hook",
			Proxy: proxy.URL,
		},
	}

	executor := outbound.NewExecutor([]string{"http://hooks.internal"})

	// Start-up refuses it...
	require.ErrorIs(t, executor.Validate(def), outbound.ErrURLNotAllowed)

	// ...and the check is repeated per attempt, so even a call that skipped Validate cannot
	// reach the proxy.
	err := executor.Execute(context.Background(), def, testEvent())
	require.ErrorIs(t, err, outbound.ErrURLNotAllowed)
	require.Zero(t, hits.Load(), "the proxy must not hear about a refused destination")
}

// TestProxyValidationRefusesWhatTheTransportCannotUse keeps the mistake at start-up: a typo in the
// proxy would otherwise turn into a slow per-packet retry loop.
func TestProxyValidationRefusesWhatTheTransportCannotUse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		proxy string
		match error
	}{
		{name: "empty means the shared client", proxy: "", match: nil},
		{name: "http", proxy: "http://proxy.internal:3128", match: nil},
		{name: "https", proxy: "https://proxy.internal:3128", match: nil},
		{name: "socks5", proxy: "socks5://proxy.internal:1080", match: nil},
		{name: "an ftp proxy", proxy: "ftp://proxy.internal:21", match: outbound.ErrProxy},
		{name: "a missing scheme", proxy: "proxy.internal:3128", match: outbound.ErrProxy},
		{name: "a missing host", proxy: "http://", match: outbound.ErrProxy},
		{name: "a broken url", proxy: "http://proxy.internal:3128/%zz", match: outbound.ErrProxy},
	}

	executor := outbound.NewExecutor(nil)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			def := wol.ActionDef{
				Name: "hook",
				Type: wol.ActionTypeHTTP,
				HTTP: &wol.HTTPParams{URL: "http://hooks.internal/hook", Proxy: tc.proxy},
			}

			err := executor.Validate(def)
			if tc.match == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.match)
		})
	}
}
