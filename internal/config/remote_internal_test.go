package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

func TestLoadRemoteCommands(t *testing.T) {
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
    command: [loginctl, lock-session]
  - id: backup
    type: exec
    command: [/bin/echo, "--target={{.Arg.target}}"]
    args:
      target: { type: string, enum: [home, work] }
      retries: { type: int, required: false }
`))
	require.NoError(t, err)

	require.True(t, cfg.Remote.Enabled)
	require.Equal(t, []byte("shared-secret"), cfg.Remote.HMACKey)
	require.Equal(t, []int{10014}, cfg.Remote.Ports)
	require.Len(t, cfg.Remote.Commands, 2)

	lock := cfg.Remote.Commands["lock"]
	require.Equal(t, []string{"loginctl", "lock-session"}, lock.Exec.Command)
	require.Equal(t, wol.RemoteAction("lock"), lock.Action())
	require.Empty(t, lock.Args)

	backup := cfg.Remote.Commands["backup"]
	require.Equal(t, []string{"/bin/echo", "--target={{.Arg.target}}"}, backup.Exec.Command)
	require.True(t, backup.Args["target"].Required)
	require.Equal(t, []string{"home", "work"}, backup.Args["target"].Enum)
	require.False(t, backup.Args["retries"].Required)
	require.Equal(t, wol.ArgTypeInt, backup.Args["retries"].Type)
}

func TestLoadRemoteCommandsDisabled(t *testing.T) {
	t.Setenv("SOL_CMD_KEY", "shared-secret")

	cfg, err := Load(writeConfig(t, `
version: 1
commands:
  - id: lock
    type: exec
    command: [loginctl, lock-session]
`))
	require.NoError(t, err)

	require.False(t, cfg.Remote.Enabled)
	require.Empty(t, cfg.Remote.Ports)
	require.Nil(t, cfg.Remote.HMACKey)
	require.Len(t, cfg.Remote.Commands, 1)
}

type remoteErrorCase struct {
	name string
	body string
	err  error
}

// remoteCommandErrorCases lists the rejected §21 configurations.
//
//nolint:gochecknoglobals // test table
var remoteCommandErrorCases = []remoteErrorCase{
	{
		name: "ports while disabled",
		body: "version: 1\nsecurity:\n  remote_command_ports: [10014]\n",
		err:  ErrRemotePorts,
	},
	{
		name: "auth is required",
		body: "version: 1\nsecurity:\n  allow_remote_commands: true\n  remote_command_ports: [10014]\n" +
			"commands:\n  - id: lock\n    type: exec\n    command: [loginctl, lock-session]\n",
		err: ErrRemoteAuth,
	},
	{
		name: "unknown auth type",
		body: "version: 1\nsecurity:\n  allow_remote_commands: true\n" +
			"  remote_command_auth: { type: token, key_env: SOL_CMD_KEY }\n  remote_command_ports: [10014]\n" +
			"commands:\n  - id: lock\n    type: exec\n    command: [loginctl, lock-session]\n",
		err: ErrRemoteAuth,
	},
	{
		name: "reserved port",
		body: "version: 1\nsecurity:\n  allow_remote_commands: true\n" +
			"  remote_command_auth: { type: hmac, key_env: SOL_CMD_KEY }\n  remote_command_ports: [9]\n" +
			"commands:\n  - id: lock\n    type: exec\n    command: [loginctl, lock-session]\n",
		err: ErrRemotePort,
	},
	{
		name: "no ports",
		body: "version: 1\nsecurity:\n  allow_remote_commands: true\n" +
			"  remote_command_auth: { type: hmac, key_env: SOL_CMD_KEY }\n" +
			"commands:\n  - id: lock\n    type: exec\n    command: [loginctl, lock-session]\n",
		err: ErrRemotePorts,
	},
	{
		name: "no commands",
		body: "version: 1\nsecurity:\n  allow_remote_commands: true\n" +
			"  remote_command_auth: { type: hmac, key_env: SOL_CMD_KEY }\n  remote_command_ports: [10014]\n",
		err: ErrRemoteCommandDef,
	},
	{
		name: "unsupported type",
		body: "version: 1\ncommands:\n  - id: lock\n    type: http\n    command: [loginctl, lock-session]\n",
		err:  ErrRemoteCommandType,
	},
	{
		name: "invalid id",
		body: "version: 1\ncommands:\n  - id: \"lock me\"\n    type: exec\n    command: [loginctl, lock-session]\n",
		err:  ErrRemoteCommandID,
	},
	{
		name: "duplicate id",
		body: "version: 1\ncommands:\n  - id: lock\n    type: exec\n    command: [loginctl, lock-session]\n" +
			"  - id: lock\n    type: exec\n    command: [loginctl, unlock-session]\n",
		err: ErrRemoteCommandID,
	},
	{
		name: "unknown argument type",
		body: "version: 1\ncommands:\n  - id: backup\n    type: exec\n    command: [/bin/echo, \"{{.Arg.target}}\"]\n" +
			"    args:\n      target: { type: ip }\n",
		err: ErrRemoteArgSpec,
	},
	{
		name: "enum value violates its type",
		body: "version: 1\ncommands:\n  - id: backup\n    type: exec\n    command: [/bin/echo, \"{{.Arg.retries}}\"]\n" +
			"    args:\n      retries: { type: int, enum: [many] }\n",
		err: ErrRemoteArgSpec,
	},
	{
		name: "invalid argument name",
		body: "version: 1\ncommands:\n  - id: backup\n    type: exec\n    command: [/bin/echo, \"{{.Arg.target}}\"]\n" +
			"    args:\n      \"tar get\": { type: string }\n",
		err: ErrRemoteArgSpec,
	},
}

func TestLoadRemoteCommandErrors(t *testing.T) {
	t.Setenv("SOL_CMD_KEY", "shared-secret")

	for _, tc := range remoteCommandErrorCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			require.ErrorIs(t, err, tc.err)
		})
	}
}
