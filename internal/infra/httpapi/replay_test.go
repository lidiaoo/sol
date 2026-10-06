package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/infra/httpapi"
)

func TestReplayCountersAreExposed(t *testing.T) {
	t.Parallel()

	deps := testDeps()
	read := deps.Status
	deps.Status = func() httpapi.Status {
		status := read()
		status.Replayed = 3
		status.ReplayReasons = map[string]uint64{"replay": 2, "stale": 1}

		return status
	}

	srv := httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)},
		deps,
	)

	status := do(t, srv, http.MethodGet, "/v1/status", testToken)
	require.Contains(t, status.Body.String(), `"replayed":3`)
	require.Contains(t, status.Body.String(), `"replay_reasons":{"replay":2,"stale":1}`)

	metrics := do(t, srv, http.MethodGet, "/metrics", testToken)
	require.Contains(t, metrics.Body.String(), "sol_replayed_total 3")
	require.Contains(t, metrics.Body.String(), `sol_replayed_total{reason="replay"} 2`)
	require.Contains(t, metrics.Body.String(), `sol_replayed_total{reason="stale"} 1`)
}

func TestReplayCountersStayOutOfTheWayWhenIdle(t *testing.T) {
	t.Parallel()

	// Nothing refused yet: the status view carries neither a total of zeroes nor a breakdown,
	// so a deployment without replay protection looks exactly as before.
	status := do(t, newServer(t), http.MethodGet, "/v1/status", testToken)
	require.NotContains(t, status.Body.String(), "replay_reasons")
	require.Contains(t, status.Body.String(), `"replayed":0`)

	metrics := do(t, newServer(t), http.MethodGet, "/metrics", testToken)
	require.Contains(t, metrics.Body.String(), "sol_replayed_total 0")
	require.NotContains(t, metrics.Body.String(), "sol_replayed_total{")
}
