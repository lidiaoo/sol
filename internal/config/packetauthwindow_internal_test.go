package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadPacketWindow(t *testing.T) {
	t.Setenv("SOL_TEST_PACKET_KEY", "shared-key")

	cfg, err := Load(writeConfig(t, `
version: 1
security:
  packet_auth: { type: hmac, key_env: SOL_TEST_PACKET_KEY, window: 60s }
rules:
  - match: { ports: [10018], auth: hmac }
    action: noop
`))

	require.NoError(t, err)
	require.Equal(t, []byte("shared-key"), cfg.PacketKey)
	require.Equal(t, time.Minute, cfg.PacketWindow)
}

func TestLoadPacketWindowOffByDefault(t *testing.T) {
	t.Setenv("SOL_TEST_PACKET_KEY", "shared-key")

	cfg, err := Load(writeConfig(t, `
version: 1
security:
  packet_auth: { type: hmac, key_env: SOL_TEST_PACKET_KEY }
`))

	require.NoError(t, err)
	require.Zero(t, cfg.PacketWindow)
}

func TestLoadPacketWindowErrors(t *testing.T) {
	t.Setenv("SOL_TEST_PACKET_KEY", "shared-key")

	tests := []struct {
		name string
		body string
	}{
		{
			name: "unparseable",
			body: "security:\n  packet_auth: { type: hmac, key_env: SOL_TEST_PACKET_KEY, window: soon }\n",
		},
		{
			name: "zero",
			body: "security:\n  packet_auth: { type: hmac, key_env: SOL_TEST_PACKET_KEY, window: 0s }\n",
		},
		{
			name: "negative",
			body: "security:\n  packet_auth: { type: hmac, key_env: SOL_TEST_PACKET_KEY, window: -5s }\n",
		},
		{
			name: "without a key",
			body: "security:\n  packet_auth: { type: hmac, window: 60s }\n",
		},
		{
			name: "window alone",
			body: "security:\n  packet_auth: { window: 60s }\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, "version: 1\n"+tc.body))

			require.ErrorIs(t, err, ErrPacketAuth)
		})
	}
}
