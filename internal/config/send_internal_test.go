package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

func TestLoadSendActionDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load(writeConfig(t, "version: 1\nactions:\n  - { name: wake, type: wol.send, mac: \"58:11:22:BC:78:66\" }\n"))
	require.NoError(t, err)

	params := cfg.Actions["wake"].Send
	require.NotNil(t, params)
	require.Equal(t, "58:11:22:bc:78:66", params.MAC.String())
	require.Equal(t, "255.255.255.255", params.Broadcast, "the limited broadcast is the default target")
	require.Equal(t, wol.PortDefault, params.Port)
	require.Empty(t, params.SecureOn)
	require.Equal(t, 1, params.Repeat)
	require.Equal(t, wol.SendDefaultInterval, params.Interval)
}

func TestLoadSendActionExplicit(t *testing.T) {
	t.Parallel()

	cfg, err := Load(writeConfig(t, `version: 1
actions:
  - name: wake-nas
    type: wol.send
    mac: "aa-bb-cc-dd-ee-ff"
    broadcast: 192.168.0.255
    port: 7
    secure_on: "s3cret"
    repeat: 3
    interval: 250ms
`))
	require.NoError(t, err)

	params := cfg.Actions["wake-nas"].Send
	require.NotNil(t, params)
	require.Equal(t, "aa:bb:cc:dd:ee:ff", params.MAC.String())
	require.Equal(t, "192.168.0.255", params.Broadcast)
	require.Equal(t, 7, params.Port)
	require.Equal(t, []byte("s3cret"), params.SecureOn)
	require.Equal(t, 3, params.Repeat)
	require.Equal(t, 250*time.Millisecond, params.Interval)
}

// TestLoadSendActionSecureOnFromEnv documents the intended way to keep a password out of the
// file: the loader expands ${VAR} before parsing, so no new mechanism is needed.
func TestLoadSendActionSecureOnFromEnv(t *testing.T) {
	t.Setenv("SOL_WOL_PASSWORD", "s3cret")

	cfg, err := Load(writeConfig(t, `version: 1
actions:
  - name: wake
    type: wol.send
    mac: "58:11:22:BC:78:66"
    secure_on: "${SOL_WOL_PASSWORD}"
`))
	require.NoError(t, err)
	require.Equal(t, []byte("s3cret"), cfg.Actions["wake"].Send.SecureOn)
}

func TestLoadSendActionErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range sendErrorCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(writeConfig(t, tc.body))
			require.ErrorIs(t, err, tc.match)
		})
	}
}

// sendErrorCases lists every wol.send document that must be refused, and by which error.
func sendErrorCases() []struct {
	name  string
	body  string
	match error
} {
	cases := sendValueErrorCases()

	return append(cases, sendParamErrorCases()...)
}

// sendValueErrorCases lists entries whose wol.send values are out of range or malformed.
func sendValueErrorCases() []struct {
	name  string
	body  string
	match error
} {
	return []struct {
		name  string
		body  string
		match error
	}{
		{
			name:  "missing mac",
			body:  "version: 1\nactions:\n  - { name: wake, type: wol.send }\n",
			match: ErrSendMAC,
		},
		{
			name:  "truncated mac",
			body:  "version: 1\nactions:\n  - { name: wake, type: wol.send, mac: \"58:11:22\" }\n",
			match: ErrSendMAC,
		},
		{
			// A valid EUI-64 address parses as a MAC but is not a 6-byte NIC address.
			name:  "eight byte mac",
			body:  "version: 1\nactions:\n  - { name: wake, type: wol.send, mac: \"58:11:22:BC:78:66:00:01\" }\n",
			match: ErrSendMAC,
		},
		{
			name:  "broken broadcast",
			body:  "version: 1\nactions:\n  - { name: wake, type: wol.send, mac: \"58:11:22:BC:78:66\", broadcast: \"192.168.0.999\" }\n",
			match: ErrSendBroadcast,
		},
		{
			name:  "port out of range",
			body:  "version: 1\nactions:\n  - { name: wake, type: wol.send, mac: \"58:11:22:BC:78:66\", port: 70000 }\n",
			match: ErrSendPort,
		},
		{
			name:  "too many copies",
			body:  "version: 1\nactions:\n  - { name: wake, type: wol.send, mac: \"58:11:22:BC:78:66\", repeat: 11 }\n",
			match: ErrSendRepeat,
		},
		{
			name:  "interval out of range",
			body:  "version: 1\nactions:\n  - { name: wake, type: wol.send, mac: \"58:11:22:BC:78:66\", interval: 30s }\n",
			match: ErrSendInterval,
		},
		{
			name:  "interval is not a duration",
			body:  "version: 1\nactions:\n  - { name: wake, type: wol.send, mac: \"58:11:22:BC:78:66\", interval: soon }\n",
			match: ErrSendInterval,
		},
		{
			name:  "secure_on of the wrong length",
			body:  "version: 1\nactions:\n  - { name: wake, type: wol.send, mac: \"58:11:22:BC:78:66\", secure_on: \"short\" }\n",
			match: wol.ErrSecureOnLength,
		},
	}
}

// sendParamErrorCases lists entries that put parameters on the wrong action type.
func sendParamErrorCases() []struct {
	name  string
	body  string
	match error
} {
	return []struct {
		name  string
		body  string
		match error
	}{
		{
			name:  "exec parameters on a send action",
			body:  "version: 1\nactions:\n  - { name: wake, type: wol.send, mac: \"58:11:22:BC:78:66\", command: [/bin/true] }\n",
			match: ErrActionParams,
		},
		{
			name:  "send parameters on a builtin action",
			body:  "version: 1\nactions:\n  - { name: nap, type: power.sleep, mac: \"58:11:22:BC:78:66\" }\n",
			match: ErrActionParams,
		},
		{
			name:  "send parameters on a sequence",
			body:  "version: 1\nactions:\n  - { name: seq, type: sequence, steps: [noop], mac: \"58:11:22:BC:78:66\" }\n",
			match: ErrActionParams,
		},
	}
}
