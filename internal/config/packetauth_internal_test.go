package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

func TestLoadPacketAuthFromEnv(t *testing.T) {
	t.Setenv("SOL_TEST_PACKET_KEY", "shared-key")

	cfg, err := Load(writeConfig(t, `
version: 1
security:
  packet_auth: { type: hmac, key_env: SOL_TEST_PACKET_KEY }
  secure_on: "s3cret"
rules:
  - match: { ports: [10010], auth: hmac }
    action: noop
`))

	require.NoError(t, err)
	require.Equal(t, []byte("shared-key"), cfg.PacketKey)
	require.Equal(t, wol.AuthHMAC, cfg.Rules[0].Match.Auth)
}

func TestLoadPacketAuthOffByDefault(t *testing.T) {
	t.Parallel()

	cfg, err := Load(writeConfig(t, "version: 1\n"))

	require.NoError(t, err)
	require.Empty(t, cfg.PacketKey)
}

func TestLoadPacketAuthErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		body  string
		match error
	}{
		{
			name:  "wrong type",
			body:  "version: 1\nsecurity:\n  packet_auth: { type: shared, key_env: SOL_TEST_PACKET_KEY }\n",
			match: ErrPacketAuth,
		},
		{
			name:  "missing key",
			body:  "version: 1\nsecurity:\n  packet_auth: { type: hmac }\n",
			match: ErrPacketAuth,
		},
		{
			name:  "unset variable",
			body:  "version: 1\nsecurity:\n  packet_auth: { type: hmac, key_env: SOL_TEST_UNSET_PACKET_KEY }\n",
			match: ErrPacketAuth,
		},
		{
			name:  "env and file together",
			body:  "version: 1\nsecurity:\n  packet_auth: { type: hmac, key_env: SOL_TEST_PACKET_KEY, key_file: /tmp/k }\n",
			match: ErrPacketAuth,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(writeConfig(t, tc.body))
			require.ErrorIs(t, err, tc.match)
		})
	}
}

func TestLoadUnknownMatchAuth(t *testing.T) {
	t.Parallel()

	_, err := Load(writeConfig(t, `
version: 1
rules:
  - match: { ports: [10010], auth: shared }
    action: noop
`))

	require.ErrorIs(t, err, wol.ErrUnknownAuthKind)
}

func TestLoadSendSign(t *testing.T) {
	t.Setenv("SOL_TEST_PACKET_KEY", "shared-key")

	cfg, err := Load(writeConfig(t, `
version: 1
security:
  packet_auth: { type: hmac, key_env: SOL_TEST_PACKET_KEY }
actions:
  - { name: wake, type: wol.send, mac: "58:11:22:BC:78:66", sign: true }
rules:
  - match: { ports: [10010] }
    action: wake
`))

	require.NoError(t, err)
	require.True(t, cfg.Actions["wake"].Send.Sign)
}
