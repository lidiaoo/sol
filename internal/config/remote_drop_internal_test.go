package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

// A remote command is a plain exec action, so a privilege drop configured there flows
// into the same ExecParams the exec executor validates and applies at startup.
func TestLoadRemoteCommandPrivilegeDrop(t *testing.T) {
	t.Setenv("SOL_CMD_KEY", "shared-secret")

	cfg, err := Load(writeConfig(t, `
version: 1
security:
  allow_remote_commands: true
  remote_command_auth: { type: hmac, key_env: SOL_CMD_KEY }
  remote_command_ports: [10014]
commands:
  - id: lock
    type: exec
    command: [/usr/bin/loginctl, lock-session]
    user: nobody
    group: nogroup
`))
	require.NoError(t, err)

	lock := cfg.Remote.Commands["lock"]
	require.Equal(t, "nobody", lock.Exec.User)
	require.Equal(t, "nogroup", lock.Exec.Group)
	require.Equal(t, wol.RemoteAction("lock"), lock.Action())
}
