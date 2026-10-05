package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

func TestLoadHTTPAction(t *testing.T) {
	t.Setenv("HOOK_TOKEN", "secret-token")

	cfg, err := Load(writeConfig(t, `
version: 1
security:
  url_allowlist: [https://hooks.example.com/]
actions:
  - name: notify
    type: http
    method: POST
    url: "https://hooks.example.com/sol/{{.Action}}"
    headers:
      Authorization: "Bearer ${HOOK_TOKEN}"
      Content-Type: application/json
    body: '{"event":"{{.Action}}","port":{{.DstPort}}}'
    timeout: 5s
    retries: 2
rules:
  - { match: { ports: [8] }, action: notify }
`))
	require.NoError(t, err)

	notify := cfg.Actions["notify"]
	require.Equal(t, wol.ActionTypeHTTP, notify.Type)
	require.Nil(t, notify.Exec)
	require.Equal(t, "POST", notify.HTTP.Method)
	require.Equal(t, "https://hooks.example.com/sol/{{.Action}}", notify.HTTP.URL)
	require.Equal(t, "Bearer secret-token", notify.HTTP.Headers["Authorization"])
	require.Contains(t, notify.HTTP.Body, `"event":"{{.Action}}"`)
	require.Contains(t, notify.HTTP.Body, `"port":{{.DstPort}}`)
	require.Equal(t, 5*time.Second, notify.HTTP.Timeout)
	require.Equal(t, 2, notify.HTTP.Retries)
	require.Equal(t, []string{"https://hooks.example.com/"}, cfg.URLAllowlist)
}
