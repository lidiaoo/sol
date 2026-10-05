package config

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
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

func TestLoadPerInterfaceSecureOnUnsupported(t *testing.T) {
	path := writeConfig(t, `
version: 1
server:
  interfaces:
    - name: eth0
      secure_on: secret
`)

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "secure_on")
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
			body:  "version: 1\nactions:\n  - { name: lock, type: exec }\n",
			match: ErrUnknownActionType,
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
