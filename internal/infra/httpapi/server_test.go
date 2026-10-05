package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
	"github.com/bavix/sol/internal/infra/httpapi"
)

const testToken = "s3cret"

func testDeps() httpapi.Deps {
	return httpapi.Deps{
		Status: func() httpapi.Status {
			return httpapi.Status{
				Uptime:     "1s",
				UptimeSecs: 1,
				Packets:    7,
				Matched:    2,
				Rules:      1,
				Interfaces: []string{"eth0"},
				Actions:    map[string]uint64{"power.shutdown": 1},
			}
		},
		Rules: func() []wol.Rule {
			return []wol.Rule{{
				Match: wol.Match{
					Ports:    []int{8},
					MAC:      wol.MACSelector{Kind: wol.MACSelf},
					Content:  wol.ContentMatcher{Kind: wol.ContentNone},
					SrcCIDRs: []string{"10.0.0.0/8"},
				},
				Action: wol.ActionShutdown,
			}}
		},
		Interfaces: func() []wol.IfaceInfo {
			return []wol.IfaceInfo{{Name: "eth0", MAC: net.HardwareAddr{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}}}
		},
		Dispatch: func(_ context.Context, action wol.Action) error {
			if action != wol.ActionShutdown {
				return fmt.Errorf("%w: %s", wol.ErrUnknownActionRef, action)
			}

			return nil
		},
	}
}

func newServer(t *testing.T) *httpapi.Server {
	t.Helper()

	return httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)},
		testDeps(),
	)
}

func do(t *testing.T, srv *httpapi.Server, method string, path string, token string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	return rec
}

func TestSensitiveEndpointsRequireAuth(t *testing.T) {
	t.Parallel()

	srv := newServer(t)

	for _, path := range []string{"/v1/status", "/v1/rules", "/v1/interfaces", "/metrics"} {
		rec := do(t, srv, http.MethodGet, path, "")
		require.Equal(t, http.StatusUnauthorized, rec.Code, path)
		require.Equal(t, `Bearer realm="sol"`, rec.Header().Get("WWW-Authenticate"), path)
	}

	rec := do(t, srv, http.MethodPost, "/v1/reload", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code, "/v1/reload")

	rec = do(t, srv, http.MethodPost, "/v1/actions/power.shutdown", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code, "/v1/actions/{name}")

	rec = do(t, srv, http.MethodGet, "/v1/status", "wrong")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHealthzIsPublic(t *testing.T) {
	t.Parallel()

	rec := do(t, newServer(t), http.MethodGet, "/healthz", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
}

func TestStatusEndpoint(t *testing.T) {
	t.Parallel()

	rec := do(t, newServer(t), http.MethodGet, "/v1/status", testToken)
	require.Equal(t, http.StatusOK, rec.Code)

	var status httpapi.Status

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	require.Equal(t, uint64(7), status.Packets)
	require.Equal(t, uint64(2), status.Matched)
	require.Equal(t, []string{"eth0"}, status.Interfaces)
	require.Equal(t, uint64(1), status.Actions["power.shutdown"])
}

func TestRulesEndpointRedactsToViews(t *testing.T) {
	t.Parallel()

	rec := do(t, newServer(t), http.MethodGet, "/v1/rules", testToken)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Rules []map[string]any `json:"rules"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Rules, 1)
	require.Equal(t, "power.shutdown", payload.Rules[0]["action"])
	require.Equal(t, "self", payload.Rules[0]["mac"])
	require.Equal(t, "none", payload.Rules[0]["content"])
}

func TestRulesEndpointUsesDefaultsForUnsetKinds(t *testing.T) {
	t.Parallel()

	deps := testDeps()
	deps.Rules = func() []wol.Rule {
		return []wol.Rule{{Match: wol.Match{Ports: []int{8}}, Action: wol.ActionNoop}}
	}

	srv := httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)},
		deps,
	)

	rec := do(t, srv, http.MethodGet, "/v1/rules", testToken)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Rules []map[string]any `json:"rules"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Rules, 1)
	require.Equal(t, "self", payload.Rules[0]["mac"])
	require.Equal(t, "none", payload.Rules[0]["content"])
}

func TestInterfacesEndpoint(t *testing.T) {
	t.Parallel()

	rec := do(t, newServer(t), http.MethodGet, "/v1/interfaces", testToken)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "aa:bb:cc:dd:ee:ff")
	require.Contains(t, rec.Body.String(), `"ipv4":"-"`)
}

func TestActionDispatch(t *testing.T) {
	t.Parallel()

	srv := newServer(t)

	rec := do(t, srv, http.MethodPost, "/v1/actions/power.shutdown", testToken)
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Contains(t, rec.Body.String(), "triggered")

	rec = do(t, srv, http.MethodPost, "/v1/actions/nope", testToken)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestReloadIsNotImplemented(t *testing.T) {
	t.Parallel()

	rec := do(t, newServer(t), http.MethodPost, "/v1/reload", testToken)
	require.Equal(t, http.StatusNotImplemented, rec.Code)
}

func TestActionSuppressedReturns429(t *testing.T) {
	t.Parallel()

	deps := testDeps()
	deps.Dispatch = func(_ context.Context, action wol.Action) error {
		return fmt.Errorf("%w: %s", httpapi.ErrSuppressed, action)
	}

	srv := httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)},
		deps,
	)

	rec := do(t, srv, http.MethodPost, "/v1/actions/noop", testToken)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
}

func TestMetricsExposition(t *testing.T) {
	t.Parallel()

	rec := do(t, newServer(t), http.MethodGet, "/metrics", testToken)
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, "sol_packets_total 7")
	require.Contains(t, body, "sol_matched_total 2")
	require.Contains(t, body, `sol_actions_total{action="power.shutdown"} 1`)
	require.Contains(t, body, "sol_uptime_seconds 1.000")
}

func TestBasicAuth(t *testing.T) {
	t.Parallel()

	srv := httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BasicAuth("sol", "pw")},
		testDeps(),
	)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/status", nil)
	req.SetBasicAuth("sol", "pw")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	badReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/status", nil)
	badReq.SetBasicAuth("sol", "nope")

	badRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(badRec, badReq)
	require.Equal(t, http.StatusUnauthorized, badRec.Code)
	require.Equal(t, `Basic realm="sol"`, badRec.Header().Get("WWW-Authenticate"))
}

func reloadServer(t *testing.T, reload func(context.Context) error) *httpapi.Server {
	t.Helper()

	deps := testDeps()
	deps.Reload = reload

	return httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)},
		deps,
	)
}

func TestReloadEndpointAppliesTheConfiguration(t *testing.T) {
	t.Parallel()

	calls := 0

	srv := reloadServer(t, func(context.Context) error {
		calls++

		return nil
	})

	rec := do(t, srv, http.MethodPost, "/v1/reload", testToken)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"reloaded":true`)
	require.Equal(t, 1, calls)
}

func TestReloadEndpointMapsFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reload  func(context.Context) error
		want    int
		wantMsg string
	}{
		{
			name:    "invalid configuration",
			reload:  func(context.Context) error { return fmt.Errorf("rule 1: %w", wol.ErrUnknownActionRef) },
			want:    http.StatusBadRequest,
			wantMsg: "unknown action reference",
		},
		{
			name: "port set changed",
			reload: func(context.Context) error {
				return fmt.Errorf("%w: ports", httpapi.ErrRestartRequired)
			},
			want:    http.StatusConflict,
			wantMsg: "restart required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := do(t, reloadServer(t, tc.reload), http.MethodPost, "/v1/reload", testToken)

			require.Equal(t, tc.want, rec.Code)
			require.Contains(t, rec.Body.String(), tc.wantMsg)
		})
	}
}

func TestReloadEndpointWithoutCallback(t *testing.T) {
	t.Parallel()

	rec := do(t, newServer(t), http.MethodPost, "/v1/reload", testToken)
	require.Equal(t, http.StatusNotImplemented, rec.Code)
}

func TestReloadEndpointRequiresAuth(t *testing.T) {
	t.Parallel()

	rec := do(t, reloadServer(t, func(context.Context) error { return nil }), http.MethodPost, "/v1/reload", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}
