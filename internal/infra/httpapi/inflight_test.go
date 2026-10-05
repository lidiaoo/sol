package httpapi_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
	"github.com/bavix/sol/internal/infra/httpapi"
)

// doWithBody is do() plus a request body, for the endpoints that take one.
func doWithBody(t *testing.T, srv *httpapi.Server, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	return rec
}

func TestInflightCounterIsExposed(t *testing.T) {
	t.Parallel()

	deps := testDeps()
	read := deps.Status
	deps.Status = func() httpapi.Status {
		status := read()
		status.Inflight = 2

		return status
	}

	srv := httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)},
		deps,
	)

	status := do(t, srv, http.MethodGet, "/v1/status", testToken)
	require.Contains(t, status.Body.String(), `"inflight":2`)

	metrics := do(t, srv, http.MethodGet, "/metrics", testToken)
	require.Contains(t, metrics.Body.String(), "sol_inflight_total 2")
}

func TestInflightRefusalAnswers429(t *testing.T) {
	t.Parallel()

	deps := testDeps()
	deps.Dispatch = func(_ context.Context, _ wol.Action) error {
		return httpapi.ErrInFlight
	}

	srv := httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)},
		deps,
	)

	rec := do(t, srv, http.MethodPost, "/v1/actions/power.shutdown", testToken)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Contains(t, rec.Body.String(), "already running")

	// The same answer on every transport that can trigger an action.
	deps.RunCommand = func(context.Context, string, map[string]string) error { return httpapi.ErrInFlight }

	srv = httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)},
		deps,
	)

	rec = do(t, srv, http.MethodPost, "/v1/commands/backup", testToken)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)

	deps.RunShell = func(context.Context, string, net.IP) error { return httpapi.ErrInFlight }

	srv = httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)},
		deps,
	)

	rec = doWithBody(t, srv, http.MethodPost, "/v1/exec", testToken, `{"cmd":"echo hi"}`)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
}
