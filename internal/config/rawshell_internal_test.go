package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// rawShellBody is the smallest valid raw shell configuration: everything §21.6 requires to be
// present, and nothing else.
const rawShellBody = `
version: 1
security:
  allow_remote_commands: true
  remote_command_auth: { type: hmac, key_env: SOL_TEST_CMD_KEY }
  remote_command_ports: [10016]
  allow_raw_shell: true
  raw_shell_auth: { type: hmac, key_env: SOL_TEST_RAW_KEY }
  raw_shell_ports: [10017]
`

func TestLoadRawShellOffByDefault(t *testing.T) {
	t.Parallel()

	cfg, err := Load(writeConfig(t, "version: 1\n"))

	require.NoError(t, err)
	require.False(t, cfg.Remote.RawShell.Enabled)
	require.Empty(t, cfg.Remote.RawShell.Ports)
	require.Empty(t, cfg.Remote.RawShell.Key)
	require.Empty(t, cfg.Remote.RawShell.Exec.Command)
}

func TestLoadRawShell(t *testing.T) {
	t.Setenv("SOL_TEST_CMD_KEY", "cmd-key")
	t.Setenv("SOL_TEST_RAW_KEY", "raw-key")

	cfg, err := Load(writeConfig(t, rawShellBody+`
  raw_shell_src_cidrs: [10.0.0.0/8, 192.168.0.0/16]
  raw_shell_allowlist: ["echo ok", "^/usr/local/bin/[a-z_]+\\.sh$"]
  raw_shell_timeout: 5s
`))

	require.NoError(t, err)

	raw := cfg.Remote.RawShell
	require.True(t, raw.Enabled)
	require.Equal(t, []int{10017}, raw.Ports)
	require.Equal(t, []byte("raw-key"), raw.Key)
	require.Len(t, raw.SrcNets, 2)
	require.Len(t, raw.Allowlist, 2)
	require.True(t, raw.Exec.Shell)
	require.Equal(t, 5*time.Second, raw.Exec.Timeout)
	require.Equal(t, rawShellPlaceholder, raw.Exec.Command[0])

	// Entries are anchored at both ends: a bare entry matches the whole command only.
	require.True(t, raw.Allowlist[0].MatchString("echo ok"))
	require.False(t, raw.Allowlist[0].MatchString("echo ok; id"))
	require.False(t, raw.Allowlist[0].MatchString("xecho ok"))
}

func TestLoadRawShellErrors(t *testing.T) {
	t.Setenv("SOL_TEST_CMD_KEY", "cmd-key")
	t.Setenv("SOL_TEST_RAW_KEY", "raw-key")

	tests := rawShellErrorCases()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))

			require.ErrorIs(t, err, tc.match)
		})
	}
}

// rawShellErrorCase is one configuration that must be refused before any packet arrives.
type rawShellErrorCase struct {
	name  string
	body  string
	match error
}

// rawShellChannel is the preamble the cases share: an enabled command channel with one port and
// its key. Each case then breaks exactly one of the raw shell requirements.
const rawShellChannel = "version: 1\nsecurity:\n  allow_remote_commands: true\n" +
	"  remote_command_auth: { type: hmac, key_env: SOL_TEST_CMD_KEY }\n" +
	"  remote_command_ports: [10016]\n"

// rawShellOptIn builds a case body from the shared channel preamble: the opt-in is on and only
// the auth and port fragments vary, which keeps each case one line.
func rawShellOptIn(auth string, ports string) string {
	return rawShellChannel + "  allow_raw_shell: true\n  raw_shell_auth: { " + auth + " }\n" +
		"  raw_shell_ports: " + ports + "\n"
}

// rawShellErrorCases lists every way to misconfigure the channel: the opt-in without the
// channel it rides on, a missing key, a reserved or shared port, and a pattern that cannot be
// compiled.
func rawShellErrorCases() []rawShellErrorCase {
	return []rawShellErrorCase{
		{
			name:  "settings without the opt-in",
			body:  "version: 1\nsecurity:\n  raw_shell_ports: [10017]\n",
			match: ErrRawShell,
		},
		{
			name: "raw shell without the remote channel",
			body: "version: 1\nsecurity:\n  allow_raw_shell: true\n" +
				"  raw_shell_auth: { type: hmac, key_env: SOL_TEST_RAW_KEY }\n  raw_shell_ports: [10017]\n",
			match: ErrRawShell,
		},
		{
			name:  "missing ports",
			body:  rawShellChannel + "  allow_raw_shell: true\n  raw_shell_auth: { type: hmac, key_env: SOL_TEST_RAW_KEY }\n",
			match: ErrRawShell,
		},
		{
			name:  "wrong auth type",
			body:  rawShellOptIn("type: shared, key_env: SOL_TEST_RAW_KEY", "[10017]"),
			match: ErrRawShell,
		},
		{
			name:  "missing key",
			body:  rawShellOptIn("type: hmac", "[10017]"),
			match: ErrRawShell,
		},
		{
			name:  "reserved port",
			body:  rawShellOptIn("type: hmac, key_env: SOL_TEST_RAW_KEY", "[9]"),
			match: ErrRawShell,
		},
		{
			name:  "overlapping command port",
			body:  rawShellOptIn("type: hmac, key_env: SOL_TEST_RAW_KEY", "[10016]"),
			match: ErrRawShell,
		},
		{
			name:  "bad allowlist pattern",
			body:  rawShellBody + "  raw_shell_allowlist: [\"^echo (\"]\n",
			match: ErrRawShell,
		},
		{
			name:  "empty allowlist entry",
			body:  rawShellBody + "  raw_shell_allowlist: [\"  \"]\n",
			match: ErrRawShell,
		},
		{
			name:  "bad source network",
			body:  rawShellBody + "  raw_shell_src_cidrs: [\"10.0.0.0/33\"]\n",
			match: ErrRawShell,
		},
		{
			name:  "bad timeout",
			body:  rawShellBody + "  raw_shell_timeout: soon\n",
			match: ErrRawShell,
		},
	}
}
