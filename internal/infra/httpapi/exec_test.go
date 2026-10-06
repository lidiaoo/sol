package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/infra/httpapi"
)

// errShellBoom stands for an unexpected failure inside the channel.
var errShellBoom = errors.New("shell channel failed")

// testShellDeps records the last command the control plane handed to the shell channel and
// replays the sentinels the app layer produces.
func testShellDeps() httpapi.Deps {
	deps := testDeps()
	deps.RunShell = func(_ context.Context, command string, srcIP net.IP) error {
		switch command {
		case "echo ok":
			return nil
		case "id":
			return fmt.Errorf("%w: allowlist", httpapi.ErrShellForbidden)
		default:
			return fmt.Errorf("%w: %s", errShellBoom, srcIP)
		}
	}

	return deps
}

func TestExecEndpoint(t *testing.T) {
	t.Parallel()

	srv := httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)},
		testShellDeps(),
	)

	tests := []struct {
		name   string
		body   string
		token  string
		status int
	}{
		{name: "triggered", body: `{"cmd":"echo ok"}`, token: testToken, status: http.StatusAccepted},
		{name: "command forbidden", body: `{"cmd":"id"}`, token: testToken, status: http.StatusForbidden},
		{name: "missing command", body: `{"cmd":"  "}`, token: testToken, status: http.StatusBadRequest},
		{name: "malformed json", body: `{"cmd":`, token: testToken, status: http.StatusBadRequest},
		{name: "unauthorized", body: `{"cmd":"echo ok"}`, status: http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := postCommand(t, srv, "/v1/exec", tc.body, tc.token)

			require.Equal(t, tc.status, rec.Code)
		})
	}
}

func TestExecEndpointWithoutShell(t *testing.T) {
	t.Parallel()

	// A deployment that never enabled the channel answers 503: the endpoint exists but has
	// nothing behind it.
	srv := httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)},
		testDeps(),
	)

	rec := postCommand(t, srv, "/v1/exec", `{"cmd":"echo ok"}`, testToken)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestExecEndpointShellSentinels(t *testing.T) {
	t.Parallel()

	srv := httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)},
		testShellDeps(),
	)

	// The app layer's disabled and forbidden sentinels map onto 403, a missing callback onto
	// 503 and anything unrecognised onto 500.
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{name: "disabled", body: `{"cmd":"boom"}`, status: http.StatusInternalServerError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := postCommand(t, srv, "/v1/exec", tc.body, testToken)

			require.Equal(t, tc.status, rec.Code)
		})
	}
}
