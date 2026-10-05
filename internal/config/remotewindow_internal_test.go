package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func remoteWindowBody(window string) string {
	return `version: 1
security:
  allow_remote_commands: true
  remote_command_auth: { type: hmac, key_env: SOL_TEST_CMD_KEY, window: ` + window + ` }
  remote_command_ports: [10019]
commands:
  - id: backup
    type: exec
    command: [/bin/echo, backup]
`
}

func TestLoadRemoteCommandWindow(t *testing.T) {
	t.Setenv("SOL_TEST_CMD_KEY", "cmd-key")

	cfg, err := Load(writeConfig(t, remoteWindowBody("90s")))
	require.NoError(t, err)
	require.Equal(t, 90*time.Second, cfg.Remote.Window)
}

func TestLoadRemoteCommandWindowOffByDefault(t *testing.T) {
	t.Setenv("SOL_TEST_CMD_KEY", "cmd-key")

	cfg, err := Load(writeConfig(t, `version: 1
security:
  allow_remote_commands: true
  remote_command_auth: { type: hmac, key_env: SOL_TEST_CMD_KEY }
  remote_command_ports: [10019]
commands:
  - id: backup
    type: exec
    command: [/bin/echo, backup]
`))
	require.NoError(t, err)
	require.Zero(t, cfg.Remote.Window, "off by default: the plain §21 wire format")
}

func TestLoadRemoteCommandWindowRejectsNonsense(t *testing.T) {
	for name, window := range map[string]string{
		"zero":     "0s",
		"negative": "-1m",
		"garbage":  "soon-ish",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SOL_TEST_CMD_KEY", "cmd-key")

			_, err := Load(writeConfig(t, remoteWindowBody(window)))
			require.ErrorIs(t, err, ErrRemoteAuth)
		})
	}

	t.Run("without a key", func(t *testing.T) {
		// A window reads like protection, so it is refused rather than ignored when there is no
		// key to bind it to.
		_, err := Load(writeConfig(t, `version: 1
security:
  allow_remote_commands: true
  remote_command_auth: { type: hmac, window: 60s }
  remote_command_ports: [10019]
commands:
  - id: backup
    type: exec
    command: [/bin/echo, backup]
`))
		require.ErrorIs(t, err, ErrRemoteAuth)
	})
}

func TestLoadRawShellWindow(t *testing.T) {
	t.Setenv("SOL_TEST_CMD_KEY", "cmd-key")
	t.Setenv("SOL_TEST_RAW_KEY", "raw-key")

	cfg, err := Load(writeConfig(t, `version: 1
security:
  allow_remote_commands: true
  remote_command_auth: { type: hmac, key_env: SOL_TEST_CMD_KEY }
  remote_command_ports: [10019]
  allow_raw_shell: true
  raw_shell_auth: { type: hmac, key_env: SOL_TEST_RAW_KEY, window: 2m }
  raw_shell_ports: [10020]
commands:
  - id: backup
    type: exec
    command: [/bin/echo, backup]
`))
	require.NoError(t, err)
	require.Equal(t, 2*time.Minute, cfg.Remote.RawShell.Window)

	// The window is a companion key: it must not be accepted while the channel is off.
	_, err = Load(writeConfig(t, `version: 1
security:
  raw_shell_auth: { type: hmac, key_env: SOL_TEST_RAW_KEY, window: 2m }
`))
	require.ErrorIs(t, err, ErrRawShell)
}
