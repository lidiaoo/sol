package outbound_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
	"github.com/lidiaoo/sol/internal/infra/outbound"
)

func testEvent() wol.Event {
	return wol.Event{
		SrcIP:     net.IPv4(10, 0, 0, 5),
		SrcPort:   1234,
		DstPort:   10031,
		Interface: "eth0",
		TargetMAC: net.HardwareAddr{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF},
		Args:      map[string]string{"target": "home"},
	}
}

func TestHTTPActionInterpolatesAndSends(t *testing.T) {
	t.Parallel()

	var got struct {
		method string
		header string
		body   string
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		got.method = r.Method
		got.header = r.Header.Get("X-Event")
		got.body = string(body)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	def := wol.ActionDef{
		Name: "notify",
		Type: wol.ActionTypeHTTP,
		HTTP: &wol.HTTPParams{
			Method:  "PUT",
			URL:     server.URL + "/hook/{{.Action}}",
			Headers: map[string]string{"X-Event": "{{.Action}}"},
			Body:    `{"action":"{{.Action}}","port":{{.DstPort}},"name":"{{.Arg.target}}"}`,
		},
	}

	executor := outbound.NewExecutor(nil)

	require.NoError(t, executor.Validate(def))
	require.NoError(t, executor.Execute(context.Background(), def, testEvent()))

	require.Equal(t, http.MethodPut, got.method)
	require.Equal(t, "notify", got.header)
	require.JSONEq(t, `{"action":"notify","port":10031,"name":"home"}`, got.body)
}

func TestHTTPActionRetriesServerErrors(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	def := wol.ActionDef{
		Name: "hook",
		Type: wol.ActionTypeHTTP,
		HTTP: &wol.HTTPParams{URL: server.URL, Retries: 2, Timeout: time.Second},
	}

	err := outbound.NewExecutor(nil).Execute(context.Background(), def, testEvent())
	require.ErrorIs(t, err, outbound.ErrStatus)
	require.EqualValues(t, 3, calls.Load())
}

func TestHTTPActionDoesNotRetryClientErrors(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	def := wol.ActionDef{
		Name: "hook",
		Type: wol.ActionTypeHTTP,
		HTTP: &wol.HTTPParams{URL: server.URL, Retries: 3, Timeout: time.Second},
	}

	err := outbound.NewExecutor(nil).Execute(context.Background(), def, testEvent())
	require.ErrorIs(t, err, outbound.ErrStatus)
	require.EqualValues(t, 1, calls.Load())
}

func TestHTTPActionValidate(t *testing.T) {
	t.Parallel()

	executor := outbound.NewExecutor([]string{"https://hooks.example.com/"})

	tests := []struct {
		name    string
		params  wol.HTTPParams
		wantErr error
	}{
		{name: "missing url", params: wol.HTTPParams{}, wantErr: outbound.ErrEmptyURL},
		{name: "bad scheme", params: wol.HTTPParams{URL: "ftp://hooks.example.com/x"}, wantErr: outbound.ErrInvalidURL},
		{name: "missing host", params: wol.HTTPParams{URL: "https:///x"}, wantErr: outbound.ErrInvalidURL},
		{
			name:    "bad method",
			params:  wol.HTTPParams{URL: "https://hooks.example.com/x", Method: "TRACE"},
			wantErr: outbound.ErrMethod,
		},
		{
			name:    "timeout out of range",
			params:  wol.HTTPParams{URL: "https://hooks.example.com/x", Timeout: time.Hour},
			wantErr: outbound.ErrTimeout,
		},
		{
			name:    "retries out of range",
			params:  wol.HTTPParams{URL: "https://hooks.example.com/x", Retries: 9},
			wantErr: outbound.ErrRetries,
		},
		{
			name:    "outside allowlist",
			params:  wol.HTTPParams{URL: "https://evil.example/oops"},
			wantErr: outbound.ErrURLNotAllowed,
		},
		{
			name:    "templated host rejected",
			params:  wol.HTTPParams{URL: "{{.Action}}"},
			wantErr: outbound.ErrInvalidURL,
		},
		{
			name:   "allowed",
			params: wol.HTTPParams{URL: "https://hooks.example.com/{{.Action}}", Body: "{{.Action}}", Method: "POST"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := executor.Validate(wol.ActionDef{Name: "notify", Type: wol.ActionTypeHTTP, HTTP: &tc.params})

			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestHTTPActionRejectsBadTemplate(t *testing.T) {
	t.Parallel()

	def := wol.ActionDef{
		Name: "notify",
		Type: wol.ActionTypeHTTP,
		HTTP: &wol.HTTPParams{URL: "https://example.com/x", Body: "{{.Action"},
	}

	require.Error(t, outbound.NewExecutor(nil).Validate(def))
}
