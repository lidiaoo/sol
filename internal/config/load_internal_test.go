package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sol.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	return path
}

func TestLoadMinimal(t *testing.T) {
	path := writeConfig(t, `
version: 1
server:
  interfaces: [eth0]
security:
  dry_run: true
  allow_reserved_port_actions: false
rules:
  - match: { ports: [9], content: { kind: none } }
    action: noop
  - match: { ports: [8], content: { kind: none } }
    action: power.shutdown
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	require.Equal(t, []string{"eth0"}, cfg.InterfaceNames)
	require.True(t, cfg.DryRun)
	require.False(t, cfg.AllowReservedActions)
	require.Len(t, cfg.Rules, 2)
	require.Equal(t, []int{9}, cfg.Rules[0].Match.Ports)
	require.Equal(t, wol.ActionNoop, cfg.Rules[0].Action)
	require.Equal(t, wol.ActionShutdown, cfg.Rules[1].Action)
	require.Equal(t, wol.ContentNone, cfg.Rules[1].Match.Content.Kind)
}

func TestLoadServerRulesEquivalentToTopLevel(t *testing.T) {
	path := writeConfig(t, `
version: 1
server:
  rules:
    - match: { ports: [11], content: { kind: none } }
      action: power.shutdown
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Rules, 1)
	require.Equal(t, wol.ActionShutdown, cfg.Rules[0].Action)
}

func TestLoadRulesConflict(t *testing.T) {
	path := writeConfig(t, `
version: 1
server:
  rules:
    - match: { ports: [11] }
      action: power.shutdown
rules:
  - match: { ports: [12] }
    action: power.reboot
`)

	_, err := Load(path)
	require.ErrorIs(t, err, ErrRulesConflict)
}

func TestLoadInterfaceBlocks(t *testing.T) {
	path := writeConfig(t, `
version: 1
server:
  rules:
    - match: { ports: [11], content: { kind: none } }
      action: power.shutdown
  interfaces:
    - name: eth0
    - name: eth1
      rules:
        - match: { ports: [10], content: { kind: none } }
          action: power.sleep
    - name: wlan0
      dry_run: true
      rules:
        - match: { ports: [12], content: { kind: none } }
          action: power.reboot
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	require.Equal(t, []string{"eth0", "eth1", "wlan0"}, cfg.InterfaceNames)
	require.Len(t, cfg.Rules, 3)

	global := cfg.Rules[0]
	require.Equal(t, []int{11}, global.Match.Ports)
	require.Empty(t, global.Match.MAC.Ifaces)
	require.False(t, global.DryRun)

	eth1 := cfg.Rules[1]
	require.Equal(t, wol.MACInterface, eth1.Match.MAC.Kind)
	require.Equal(t, []string{"eth1"}, eth1.Match.MAC.Ifaces)
	require.Equal(t, wol.ActionSleep, eth1.Action)
	require.False(t, eth1.DryRun)

	wlan0 := cfg.Rules[2]
	require.Equal(t, []string{"wlan0"}, wlan0.Match.MAC.Ifaces)
	require.True(t, wlan0.DryRun)
}

func TestLoadInterfaceDryRunWithoutRules(t *testing.T) {
	path := writeConfig(t, `
version: 1
server:
  interfaces:
    - name: eth0
      dry_run: true
  rules:
    - match: { ports: [11] }
      action: power.shutdown
`)

	_, err := Load(path)
	require.ErrorIs(t, err, ErrInterfaceDryRun)
}

func TestLoadDuplicateInterface(t *testing.T) {
	path := writeConfig(t, `
version: 1
server:
  interfaces: [eth0, eth0]
`)

	_, err := Load(path)
	require.ErrorIs(t, err, wol.ErrDuplicateInterface)
}

func TestLoadPerInterfaceSecureOn(t *testing.T) {
	path := writeConfig(t, `
version: 1
server:
  interfaces:
    - name: eth0
      secure_on: secret
      rules:
        - match: { ports: [8] }
          action: noop
        - match: { ports: [10], secure_on: other! }
          action: noop
        - match: { ports: [11], secure_on: "" }
          action: noop
    - name: eth1
      rules:
        - match: { ports: [12] }
          action: noop
  rules:
    - match: { ports: [13] }
      action: noop
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	// The block's password reaches the rules that do not declare one, a rule can override it,
	// and an explicitly empty value means "no password" rather than "inherit".
	secureOn := make(map[int]string, len(cfg.Rules))

	for _, rule := range cfg.Rules {
		require.Len(t, rule.Match.Ports, 1)

		port := rule.Match.Ports[0]
		secureOn[port] = string(rule.Match.SecureOn)

		switch port {
		case 11:
			require.NotNil(t, rule.Match.SecureOn, "an explicit empty value is not the same as leaving it out")
		case 12, 13:
			require.Nil(t, rule.Match.SecureOn, "only the block declares a password")
		}
	}

	require.Equal(t, "secret", secureOn[8])
	require.Equal(t, "other!", secureOn[10])
	require.Empty(t, secureOn[11])
}

func TestLoadSecureOnLength(t *testing.T) {
	path := writeConfig(t, `
version: 1
server:
  interfaces:
    - name: eth0
      rules:
        - match: { ports: [8], secure_on: short }
          action: noop
`)

	_, err := Load(path)
	require.ErrorIs(t, err, wol.ErrSecureOnLength)
}

func TestLoadMatchSHApes(t *testing.T) {
	path := writeConfig(t, `
version: 1
server:
  rules:
    - match:
        ports: [6]
        content: { kind: prefix, offset: 1, value_hex: "a1b2" }
        src_cidrs: ["10.0.0.0/24"]
      action: power.shutdown
    - match: { ports: [8], interfaces: [eth0] }
      action: power.reboot
    - match: { ports: [10], mac: self }
      action: power.sleep
    - match: { ports: [13], mac: "11:22:33:44:55:66" }
      action: power.sleep
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Rules, 4)

	require.Equal(t, wol.ContentPrefix, cfg.Rules[0].Match.Content.Kind)
	require.Equal(t, "a1b2", cfg.Rules[0].Match.Content.Hex)
	require.Equal(t, 1, cfg.Rules[0].Match.Content.Offset)
	require.Equal(t, []string{"10.0.0.0/24"}, cfg.Rules[0].Match.SrcCIDRs)

	require.Equal(t, wol.MACInterface, cfg.Rules[1].Match.MAC.Kind)
	require.Equal(t, []string{"eth0"}, cfg.Rules[1].Match.MAC.Ifaces)

	require.Equal(t, wol.MACSelf, cfg.Rules[2].Match.MAC.Kind)

	require.Equal(t, wol.MACExplicit, cfg.Rules[3].Match.MAC.Kind)
	require.Equal(t, "11:22:33:44:55:66", cfg.Rules[3].Match.MAC.Address)
}

func TestLoadNamedActions(t *testing.T) {
	path := writeConfig(t, `
version: 1
actions:
  - name: shutdown-now
    type: power.shutdown
rules:
  - match: { ports: [8], content: { kind: none } }
    action: shutdown-now
  - match: { ports: [9], content: { kind: none } }
    action: n
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, wol.Action("shutdown-now"), cfg.Rules[0].Action)
	require.Equal(t, wol.ActionNoop, cfg.Rules[1].Action)

	def, ok := cfg.Actions[wol.Action("shutdown-now")]
	require.True(t, ok)
	require.Equal(t, wol.ActionTypeShutdown, def.Type)
}

func TestLoadActionErrors(t *testing.T) {
	tests := map[string]struct {
		body  string
		match error
	}{
		"duplicate builtin name": {
			body:  "version: 1\nactions:\n  - { name: noop, type: noop }\n",
			match: ErrDuplicateAction,
		},
		"unknown type": {
			body:  "version: 1\nactions:\n  - { name: lock, type: wol.wake }\n",
			match: ErrUnknownActionType,
		},
		"http action without url": {
			body:  "version: 1\nactions:\n  - { name: notify, type: http }\n",
			match: ErrHTTPURLRequired,
		},
		"http action with exec params": {
			body:  "version: 1\nactions:\n  - { name: notify, type: http, url: https://example.com, command: [ls] }\n",
			match: ErrActionParams,
		},
		"exec action with http params": {
			body:  "version: 1\nactions:\n  - { name: hook, type: exec, command: [ls], url: https://example.com }\n",
			match: ErrActionParams,
		},
		"unknown action reference": {
			body:  "version: 1\nrules:\n  - { match: { ports: [8] }, action: nope }\n",
			match: wol.ErrUnknownActionRef,
		},
		"missing action": {
			body:  "version: 1\nrules:\n  - { match: { ports: [8] } }\n",
			match: ErrActionRequired,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			require.ErrorIs(t, err, tt.match)
		})
	}
}

func TestLoadUnsupportedVersion(t *testing.T) {
	_, err := Load(writeConfig(t, "version: 2\n"))
	require.ErrorIs(t, err, ErrUnsupportedVersion)
}

func TestLoadUnknownFieldIsRejected(t *testing.T) {
	_, err := Load(writeConfig(t, "version: 1\nsecurity:\n  dry_run: false\n  nope: true\n"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "nope")
}

func TestLoadMissingEnvVar(t *testing.T) {
	path := writeConfig(t, `
version: 1
security:
  secure_on: ${SOL_TEST_UNSET_VAR}
`)

	_, err := Load(path)
	require.ErrorIs(t, err, ErrMissingEnvVar)
}

func TestLoadEnvInterpolation(t *testing.T) {
	t.Setenv("SOL_TEST_SECURE_ON", "hunter2")

	path := writeConfig(t, `
version: 1
security:
  secure_on: ${SOL_TEST_SECURE_ON}
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, []byte("hunter2"), cfg.SecureOn)
}

func TestApplyEnvOverrides(t *testing.T) {
	t.Setenv(EnvDryRun, "true")
	t.Setenv(EnvAllowReservedPortActions, "1")
	t.Setenv(EnvInterfaces, "eth0, wlan0")

	cfg, err := Load(writeConfig(t, "version: 1\nsecurity:\n  dry_run: false\n"))
	require.NoError(t, err)

	require.True(t, cfg.DryRun)
	require.True(t, cfg.AllowReservedActions)
	require.Equal(t, []string{"eth0", "wlan0"}, cfg.InterfaceNames)
}

func TestApplyEnvInvalidBool(t *testing.T) {
	t.Setenv(EnvDryRun, "maybe")

	_, err := Load(writeConfig(t, "version: 1\n"))
	require.ErrorIs(t, err, ErrEnvValue)
}

func TestLoadDiscoveryViaEnv(t *testing.T) {
	path := writeConfig(t, "version: 1\nsecurity:\n  dry_run: true\n")
	t.Setenv(EnvConfig, path)

	cfg, err := Load("")
	require.NoError(t, err)
	require.True(t, cfg.DryRun)
}

func TestLoadNoFileFallsBackToDefaults(t *testing.T) {
	if _, err := os.Stat(systemConfigPath); err == nil {
		t.Skip("/etc/sol/sol.yaml exists on this host")
	}

	t.Setenv(EnvConfig, "")
	t.Setenv("HOME", t.TempDir())

	cfg, err := Load("")
	require.NoError(t, err)
	require.Empty(t, cfg.InterfaceNames)
	require.Empty(t, cfg.Rules)
	require.Len(t, cfg.Actions, 4)
}

func TestLoadExplicitPathMissing(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	require.Error(t, err)
}

func TestLoadLoggingSection(t *testing.T) {
	path := writeConfig(t, "version: 1\nlogging:\n  level: debug\n  format: json\n")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "debug", cfg.Logging.Level)
	require.Equal(t, "json", cfg.Logging.Format)
}

func TestApplyEnvLoggingOverrides(t *testing.T) {
	t.Setenv(EnvLogLevel, "error")
	t.Setenv(EnvLogFormat, "json")

	cfg, err := Load(writeConfig(t, "version: 1\nlogging:\n  level: info\n"))
	require.NoError(t, err)
	require.Equal(t, "error", cfg.Logging.Level)
	require.Equal(t, "json", cfg.Logging.Format)
}

func TestLoadHTTPSection(t *testing.T) {
	t.Setenv("SOL_TEST_TOKEN", "tok-from-env")

	cfg, err := Load(writeConfig(t, `
version: 1
server:
  http:
    enabled: true
    listen: 127.0.0.1:9090
    auth: { type: bearer, token_env: SOL_TEST_TOKEN }
rules:
  - { match: { ports: [8] }, action: noop }
`))

	require.NoError(t, err)
	require.True(t, cfg.HTTP.Enabled)
	require.Equal(t, "127.0.0.1:9090", cfg.HTTP.Listen)
	require.Equal(t, AuthTypeBearer, cfg.HTTP.AuthType)
	require.Equal(t, "tok-from-env", cfg.HTTP.Token)
}

func TestLoadHTTPDisabledByDefault(t *testing.T) {
	cfg, err := Load(writeConfig(t, "version: 1\nrules:\n  - { match: { ports: [8] }, action: noop }\n"))
	require.NoError(t, err)
	require.False(t, cfg.HTTP.Enabled)
	require.Empty(t, cfg.HTTP.Token)
}

func TestLoadHTTPBasicAuth(t *testing.T) {
	t.Setenv("SOL_TEST_PASSWORD", "pw")

	cfg, err := Load(writeConfig(t, `
version: 1
server:
  http:
    enabled: true
    listen: 0.0.0.0:8080
    auth: { type: basic, user: sol, password_env: SOL_TEST_PASSWORD }
rules:
  - { match: { ports: [8] }, action: noop }
`))

	require.NoError(t, err)
	require.Equal(t, AuthTypeBasic, cfg.HTTP.AuthType)
	require.Equal(t, "sol", cfg.HTTP.User)
	require.Equal(t, "pw", cfg.HTTP.Password)
}

func TestLoadSecretErrorsFilePermissions(t *testing.T) {
	t.Setenv("SOL_TEST_TOKEN", "")

	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("file-token\n"), 0o600))

	body := `
version: 1
server:
  http:
    enabled: true
    auth: { type: bearer, token_file: %s }
rules:
  - { match: { ports: [8] }, action: noop }
`

	cfg, err := Load(writeConfig(t, fmt.Sprintf(body, path)))
	require.NoError(t, err)
	require.Equal(t, "file-token", cfg.HTTP.Token)

	require.NoError(t, os.Chmod(path, 0o644))

	_, err = Load(writeConfig(t, fmt.Sprintf(body, path)))
	require.ErrorIs(t, err, ErrSecret)
}

// The secret resolver is shared by the HTTP token, the packet key, the remote command key and the
// raw shell key, so its message must not claim to be about HTTP: it used to read "cannot resolve
// the http auth secret" for all of them, which looks like a bug in sol when the key that is
// missing belongs to a packet.
func TestLoadSecretMessageNamesTheField(t *testing.T) {
	// No t.Parallel: t.Setenv forbids it.
	t.Setenv("SOL_TEST_UNSET_PACKET_KEY", "")

	_, err := Load(writeConfig(t, "version: 1\nsecurity:\n  packet_auth: { type: hmac, key_env: SOL_TEST_PACKET_KEY }\n"))
	require.ErrorIs(t, err, ErrPacketAuth)
	require.ErrorIs(t, err, ErrSecret)
	require.Contains(t, err.Error(), "packet_auth.key")
	require.NotContains(t, err.Error(), "http auth secret")
	require.Contains(t, err.Error(), "SOL_TEST_PACKET_KEY")
}

func TestLoadHTTPErrors(t *testing.T) {
	configBody := func(httpBlock string) string {
		return "version: 1\nserver:\n  http:\n" + httpBlock +
			"rules:\n  - { match: { ports: [8] }, action: noop }\n"
	}

	tests := map[string]struct {
		block string
		match error
	}{
		"unknown auth type": {
			block: "    enabled: true\n    auth: { type: none }\n",
			match: ErrHTTPAuthType,
		},
		"bearer without secret": {
			block: "    enabled: true\n    auth: { type: bearer }\n",
			match: ErrSecret,
		},
		"env variable empty": {
			block: "    enabled: true\n    auth: { type: bearer, token_env: SOL_DEFINITELY_UNSET }\n",
			match: ErrSecret,
		},
		"env and file together": {
			block: "    enabled: true\n    auth: { type: bearer, token_env: SOL_X, token_file: /tmp/x }\n",
			match: ErrSecret,
		},
		"basic without user": {
			block: "    enabled: true\n    auth: { type: basic, password_env: SOL_X }\n",
			match: ErrHTTPUser,
		},
		"bad listen address": {
			block: "    enabled: true\n    listen: 8080\n    auth: { type: bearer, token_env: SOL_X }\n",
			match: ErrHTTPListen,
		},
		"mtls without client ca": {
			block: "    enabled: true\n    auth: { type: mtls }\n",
			match: ErrHTTPTLS,
		},
		"tls half configured": {
			block: "    enabled: true\n    tls: { cert_file: /tmp/cert.pem }\n",
			match: ErrHTTPTLS,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, configBody(tt.block)))
			require.ErrorIs(t, err, tt.match)
		})
	}
}

func TestLoadCooldowns(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
version: 1
security:
  cooldown: 5s
  cooldowns:
    power.shutdown: 30s
rules:
  - { match: { ports: [8] }, action: noop }
`))

	require.NoError(t, err)
	require.Equal(t, 5*time.Second, cfg.Cooldown)
	require.Equal(t, 30*time.Second, cfg.ActionCooldowns[wol.ActionShutdown])
}

// The power actions carry built-in execution windows so that a repeated wake-on-LAN packet does not
// put the machine down again; the configuration can change or switch off each of them.
func TestLoadGuardDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
version: 1
rules:
  - { match: { ports: [8] }, action: power.sleep }
`))

	require.NoError(t, err)
	require.Equal(t, 2*time.Minute, cfg.Settle)
	require.Equal(t, []wol.Action{wol.ActionSleep, wol.ActionShutdown, wol.ActionReboot}, cfg.SettleActions)
	require.Equal(t, 2*time.Minute, cfg.ActionCooldowns[wol.ActionSleep])
	require.Equal(t, 2*time.Minute, cfg.ActionCooldowns[wol.ActionShutdown])
	require.Equal(t, 2*time.Minute, cfg.ActionCooldowns[wol.ActionReboot])
	require.Zero(t, cfg.Cooldown, "the general cooldown stays off")
}

func TestLoadGuardOverrides(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
version: 1
security:
  settle: 45s
  settle_actions: [power.shutdown]
  cooldown: 10s
  cooldowns:
    power.sleep: 0s
rules:
  - { match: { ports: [8] }, action: power.sleep }
`))

	require.NoError(t, err)
	require.Equal(t, 45*time.Second, cfg.Settle)
	require.Equal(t, []wol.Action{wol.ActionShutdown}, cfg.SettleActions)
	require.Equal(t, 10*time.Second, cfg.Cooldown)
	require.NotContains(t, cfg.ActionCooldowns, wol.ActionSleep, "0s switches the built-in window off")
	require.Equal(t, 2*time.Minute, cfg.ActionCooldowns[wol.ActionReboot])
}

func TestLoadGuardDisabled(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
version: 1
security:
  settle: 0
rules:
  - { match: { ports: [8] }, action: power.sleep }
`))

	require.NoError(t, err)
	require.Zero(t, cfg.Settle)
	require.Empty(t, cfg.SettleActions)
}

func TestLoadCooldownErrors(t *testing.T) {
	defaults := "version: 1\nrules:\n  - { match: { ports: [8] }, action: noop }\n"

	tests := map[string]struct {
		security string
		match    error
	}{
		"malformed duration": {
			security: "security:\n  cooldown: soon\n",
			match:    ErrCooldown,
		},
		"negative duration": {
			security: "security:\n  cooldown: -5s\n",
			match:    ErrCooldown,
		},
		"unknown action": {
			security: "security:\n  cooldowns: { nope: 5s }\n",
			match:    wol.ErrUnknownActionRef,
		},
		"negative per-action window": {
			security: "security:\n  cooldowns: { noop: -5s }\n",
			match:    ErrCooldown,
		},
		"malformed settle": {
			security: "security:\n  settle: soon\n",
			match:    ErrSettle,
		},
		"negative settle": {
			security: "security:\n  settle: -1m\n",
			match:    ErrSettle,
		},
		"unknown settle action": {
			security: "security:\n  settle_actions: [nope]\n",
			match:    wol.ErrUnknownActionRef,
		},
		"settle actions without a settle window": {
			security: "security:\n  settle: 0\n  settle_actions: [power.sleep]\n",
			match:    ErrSettle,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, defaults+tt.security))
			require.ErrorIs(t, err, tt.match)
		})
	}
}

func TestLoadUnknownLoggingFieldIsRejected(t *testing.T) {
	_, err := Load(writeConfig(t, "version: 1\nlogging:\n  level: info\n  colour: true\n"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "colour")
}

func TestLoadBlockScopeConflict(t *testing.T) {
	path := writeConfig(t, `
version: 1
server:
  interfaces:
    - name: eth0
      rules:
        - match: { interfaces: [wlan0], ports: [8] }
          action: power.shutdown
`)

	_, err := Load(path)
	require.ErrorIs(t, err, wol.ErrInterfaceScopeConflict)
}

func TestLoadBlockScopeMatchingIsAllowed(t *testing.T) {
	path := writeConfig(t, `
version: 1
server:
  interfaces:
    - name: eth0
      rules:
        - match: { interfaces: [eth0], ports: [8] }
          action: power.shutdown
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, []string{"eth0"}, cfg.Rules[0].Match.MAC.Ifaces)
}

func TestLoadExecAction(t *testing.T) {
	path := writeConfig(t, `
version: 1
security:
  exec_allowlist: [/opt/sol/bin]
actions:
  - name: lock-screen
    type: exec
    command: [loginctl, lock-session]
    timeout: 5s
    workdir: /opt/sol
    env: [SOL_EVENT=hit]
rules:
  - match: { ports: [10], content: { kind: suffix, value: "lock" } }
    action: lock-screen
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, []string{"/opt/sol/bin"}, cfg.ExecAllowlist)

	def, ok := cfg.Actions["lock-screen"]
	require.True(t, ok)
	require.Equal(t, wol.ActionTypeExec, def.Type)
	require.NotNil(t, def.Exec)
	require.Equal(t, []string{"loginctl", "lock-session"}, def.Exec.Command)
	require.Equal(t, 5*time.Second, def.Exec.Timeout)
	require.Equal(t, "/opt/sol", def.Exec.Workdir)
	require.Equal(t, []string{"SOL_EVENT=hit"}, def.Exec.Env)
	require.False(t, def.Exec.Shell)

	decision, matched := newTestPolicy(t, cfg)
	require.True(t, matched)
	require.Equal(t, wol.Action("lock-screen"), decision.Action)
}

func TestLoadExecActionErrors(t *testing.T) {
	tests := map[string]struct {
		body  string
		match error
	}{
		"missing command": {
			body:  "version: 1\nactions:\n  - { name: lock, type: exec }\n",
			match: ErrExecCommandRequired,
		},
		"bad timeout": {
			body:  "version: 1\nactions:\n  - { name: lock, type: exec, command: [loginctl], timeout: soon }\n",
			match: ErrExecTimeout,
		},
		"exec params on a builtin action": {
			body:  "version: 1\nactions:\n  - { name: fast-shutdown, type: power.shutdown, command: [/bin/true] }\n",
			match: ErrActionParams,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			require.ErrorIs(t, err, tt.match)
		})
	}
}

func TestLoadGlobalAndBlockRuleConflict(t *testing.T) {
	path := writeConfig(t, `
version: 1
server:
  rules:
    - match: { ports: [8], content: { kind: none } }
      action: noop
  interfaces:
    - name: eth0
      rules:
        - match: { ports: [8], content: { kind: none } }
          action: power.shutdown
`)

	cfg, err := Load(path)
	require.NoError(t, err)

	ifaces := []wol.IfaceInfo{{Name: "eth0", MAC: net.HardwareAddr{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}}}

	_, err = wol.NewRoutingPolicy(cfg.Rules, ifaces, wol.PolicyOptions{Actions: cfg.Actions})
	require.ErrorIs(t, err, wol.ErrRuleConflict)
}

// newTestPolicy builds a policy from a loaded config against a fake eth0 and resolves a
// magic packet for port 10.
func newTestPolicy(t *testing.T, cfg *Config) (wol.Decision, bool) {
	t.Helper()

	mac := net.HardwareAddr{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	ifaces := []wol.IfaceInfo{{Name: "eth0", MAC: mac}}

	policy, err := wol.NewRoutingPolicy(cfg.Rules, ifaces, wol.PolicyOptions{
		ReservedPorts: cfg.ReservedPorts,
		AllowReserved: cfg.AllowReservedActions,
		SecureOn:      cfg.SecureOn,
		Actions:       cfg.Actions,
	})
	require.NoError(t, err)

	payload := append(wol.BuildMagicPacket(mac), []byte("lock")...)

	return policy.Resolve(wol.Event{Payload: payload, DstPort: 10})
}

// A commented-out line must never make an environment variable required: the README example
// comments out an optional secure_on and would otherwise refuse to start.
func TestLoadIgnoresEnvRefsInComments(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
version: 1
actions:
  - name: wake
    type: wol.send
    mac: "58:11:22:BC:78:66"
    # secure_on: "${SOL_TEST_UNSET_VAR}"   # only if the target requires SecureOn
    repeat: 2  # was ${SOL_TEST_UNSET_VAR}
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 2, cfg.Actions["wake"].Send.Repeat)
}

// A '#' inside a quoted scalar is data, not a comment, so a reference after it still resolves.
func TestLoadExpandsEnvAfterQuotedHash(t *testing.T) {
	t.Setenv("SOL_TEST_QUOTED_HASH", "cret")

	path := writeConfig(t, `
version: 1
security:
  secure_on: "a #${SOL_TEST_QUOTED_HASH}"
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, []byte("a #cret"), cfg.SecureOn)
}

// A block scalar body is data even when one of its lines looks like a comment.
func TestLoadExpandsEnvInBlockScalar(t *testing.T) {
	t.Setenv("SOL_TEST_BODY", "hunter2")

	path := writeConfig(t, `
version: 1
actions:
  - name: notify
    type: http
    method: POST
    url: "https://hooks.example.com/x"
    body: |
      {"token":"${SOL_TEST_BODY}"}
      # not a comment here: ${SOL_TEST_BODY}
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Contains(t, cfg.Actions["notify"].HTTP.Body, `{"token":"hunter2"}`)
	require.Contains(t, cfg.Actions["notify"].HTTP.Body, "# not a comment here: hunter2")
}

// A reference inside a block scalar is still a reference: a missing variable is an error.
func TestLoadBlockScalarStillNeedsEnvVars(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
version: 1
actions:
  - name: notify
    type: http
    method: POST
    url: "https://hooks.example.com/x"
    body: |
      {"token":"${SOL_TEST_UNSET_VAR}"}
`)

	_, err := Load(path)
	require.ErrorIs(t, err, ErrMissingEnvVar)
}
