package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/infra/httpapi"
)

func testCommandDeps() httpapi.Deps {
	deps := testDeps()
	deps.RunCommand = func(_ context.Context, id string, args map[string]string) error {
		switch {
		case id != "backup":
			return fmt.Errorf("%w: %s", httpapi.ErrCommandNotFound, id)
		case args["target"] != "home":
			return fmt.Errorf("%w: target", httpapi.ErrCommandArgs)
		default:
			return nil
		}
	}

	return deps
}

func postCommand(t *testing.T, srv *httpapi.Server, path string, body string, token string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	return rec
}

func TestCommandEndpoint(t *testing.T) {
	t.Parallel()

	srv := httpapi.New(
		httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)},
		testCommandDeps(),
	)

	tests := []struct {
		name   string
		path   string
		body   string
		token  string
		status int
	}{
		{name: "triggered", path: "/v1/commands/backup", body: `{"target":"home"}`, token: testToken, status: http.StatusAccepted},
		{name: "no arguments", path: "/v1/commands/backup", body: "", token: testToken, status: http.StatusBadRequest},
		{name: "rejected argument", path: "/v1/commands/backup", body: `{"target":"work"}`, token: testToken, status: http.StatusBadRequest},
		{name: "malformed json", path: "/v1/commands/backup", body: `{"target":`, token: testToken, status: http.StatusBadRequest},
		{name: "non string argument", path: "/v1/commands/backup", body: `{"target":5}`, token: testToken, status: http.StatusBadRequest},
		{name: "unknown command", path: "/v1/commands/lock", body: `{"target":"home"}`, token: testToken, status: http.StatusNotFound},
		{name: "unauthorized", path: "/v1/commands/backup", body: `{"target":"home"}`, status: http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := postCommand(t, srv, tc.path, tc.body, tc.token)
			require.Equal(t, tc.status, rec.Code)
		})
	}
}

func TestCommandEndpointUnavailable(t *testing.T) {
	t.Parallel()

	srv := newServer(t)

	rec := postCommand(t, srv, "/v1/commands/backup", `{}`, testToken)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestCommandEndpointForbidden(t *testing.T) {
	t.Parallel()

	deps := testDeps()
	deps.RunCommand = func(_ context.Context, _ string, _ map[string]string) error {
		return fmt.Errorf("%w: remote commands are disabled", httpapi.ErrCommandForbidden)
	}

	srv := httpapi.New(httpapi.Config{Listen: "127.0.0.1:0", Auth: httpapi.BearerAuth(testToken)}, deps)

	rec := postCommand(t, srv, "/v1/commands/backup", `{}`, testToken)
	require.Equal(t, http.StatusForbidden, rec.Code)
}
