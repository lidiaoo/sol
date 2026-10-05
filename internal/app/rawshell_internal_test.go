package app

import (
	"context"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

const rawShellTestPort = 10015

// shellService returns a listener whose raw shell port also carries a noop rule, so tests can
// tell a consumed shell packet from one that fell through to the rules.
func shellService(t *testing.T, settings RawShellSettings, dryRun bool) (*ListenService, *executorMock) {
	t.Helper()

	ifaces := testIfaces()
	policy := mustPolicy(t, ifaces, []wol.Rule{ruleFor(rawShellTestPort, wol.Action("noop"))})
	executor := &executorMock{}

	svc := NewListenService(&factoryMock{}, testRegistry(executor), policy, ifaces, dryRun).
		WithRawShell(settings)

	return svc, executor
}

// shellSettings is the common shape: one dedicated port and a key.
func shellSettings(key []byte) RawShellSettings {
	return RawShellSettings{
		Enabled: true,
		Ports:   []int{rawShellTestPort},
		Key:     key,
		Exec:    wol.ExecParams{Command: []string{"true"}, Shell: true},
	}
}

// signedShellPacket builds a magic packet whose whole content region is a signed shell command.
func signedShellPacket(key []byte, command string) []byte {
	return signedRemotePacket(key, command)
}

func sendShell(svc *ListenService, payload []byte, src net.IP) {
	svc.handlePacket(context.Background(), packet{
		payload: payload,
		port:    rawShellTestPort,
		src:     &net.UDPAddr{IP: src, Port: 20002},
	})
}

func TestRawShellRunnerDisabledWithoutSettings(t *testing.T) {
	t.Parallel()

	require.Nil(t, newRawShellRunner(false, []int{rawShellTestPort}, []byte("k"), nil, nil, wol.ExecParams{}))

	svc, executor := shellService(t, RawShellSettings{Enabled: true, Key: []byte("k")}, false)

	// No ports resolved means no runner, so a plain magic packet still reaches the rules.
	sendShell(svc, wol.BuildMagicPacket(testMAC()), net.IPv4(127, 0, 0, 1))

	require.Equal(t, 1, executor.calls)
	require.Equal(t, wol.Action("noop"), executor.action)
}

func TestRawShellRunsSignedCommand(t *testing.T) {
	t.Parallel()

	key := []byte("raw-shell-key")
	svc, executor := shellService(t, shellSettings(key), false)

	sendShell(svc, signedShellPacket(key, "echo hi > /tmp/sol-raw"), net.IPv4(127, 0, 0, 1))

	require.Equal(t, 1, executor.calls)
	require.Equal(t, wol.RawShellAction, executor.action)
	require.NotNil(t, executor.def.Exec)
	require.True(t, executor.def.Exec.Shell)
	require.Equal(t, []string{"echo hi > /tmp/sol-raw"}, executor.def.Exec.Command)
	require.Equal(t, uint64(1), svc.Stats().Matched)
	require.Equal(t, uint64(1), svc.Stats().Actions[string(wol.RawShellAction)])
}

func TestRawShellRejectsUnsignedOrMisconfiguredCommands(t *testing.T) {
	t.Parallel()

	key := []byte("raw-shell-key")

	tests := []struct {
		name     string
		settings RawShellSettings
		payload  []byte
	}{
		{
			name:     "wrong key",
			settings: shellSettings(key),
			payload:  signedShellPacket([]byte("other-key"), "echo hi"),
		},
		{
			name:     "missing signature",
			settings: shellSettings(key),
			payload:  append(wol.BuildMagicPacket(testMAC()), []byte("echo hi")...),
		},
		{
			name:     "empty command",
			settings: shellSettings(key),
			payload:  signedShellPacket(key, ""),
		},
		{
			name:     "command outside the allowlist",
			settings: shellSettingsWithAllowlist(key, `^echo ok$`),
			payload:  signedShellPacket(key, "echo bad"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, executor := shellService(t, tc.settings, false)

			sendShell(svc, tc.payload, net.IPv4(127, 0, 0, 1))

			require.Zero(t, executor.calls)
			require.Zero(t, svc.Stats().Matched)
		})
	}
}

func shellSettingsWithAllowlist(key []byte, entries ...string) RawShellSettings {
	settings := shellSettings(key)
	for _, entry := range entries {
		settings.Allowlist = append(settings.Allowlist, regexp.MustCompile("^(?:"+entry+")$"))
	}

	return settings
}

func TestRawShellAllowlistIsFullMatch(t *testing.T) {
	t.Parallel()

	key := []byte("raw-shell-key")
	svc, executor := shellService(t, shellSettingsWithAllowlist(key, `echo ok`), false)

	sendShell(svc, signedShellPacket(key, "echo ok"), net.IPv4(127, 0, 0, 1))
	require.Equal(t, 1, executor.calls)

	// The same entry does not leak into commands that merely contain it.
	sendShell(svc, signedShellPacket(key, "echo ok; id"), net.IPv4(127, 0, 0, 1))
	require.Equal(t, 1, executor.calls)
}

func TestRawShellSourceCIDRs(t *testing.T) {
	t.Parallel()

	key := []byte("raw-shell-key")
	settings := shellSettings(key)
	settings.SrcNets = []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}

	svc, executor := shellService(t, settings, false)

	sendShell(svc, signedShellPacket(key, "echo hi"), net.IPv4(192, 168, 1, 5))
	require.Zero(t, executor.calls)

	sendShell(svc, signedShellPacket(key, "echo hi"), net.IPv4(10, 1, 2, 3))
	require.Equal(t, 1, executor.calls)
}

func mustCIDR(t *testing.T, cidr string) *net.IPNet {
	t.Helper()

	_, network, err := net.ParseCIDR(cidr)
	require.NoError(t, err)

	return network
}

func TestRawShellDryRunOnlyLogs(t *testing.T) {
	t.Parallel()

	key := []byte("raw-shell-key")
	svc, executor := shellService(t, shellSettings(key), true)

	// A dry-run instance keeps its rules dry; the shell channel must obey that too.
	sendShell(svc, signedShellPacket(key, "echo hi"), net.IPv4(127, 0, 0, 1))
	require.Zero(t, executor.calls)

	// Without dry-run the same packet reaches the executor, so the guard is the flag alone.
	live, liveExecutor := shellService(t, shellSettings(key), false)
	sendShell(live, signedShellPacket(key, "echo hi"), net.IPv4(127, 0, 0, 1))
	require.Equal(t, 1, liveExecutor.calls)
}

func TestRawShellObeysCooldown(t *testing.T) {
	t.Parallel()

	key := []byte("raw-shell-key")
	svc, executor := shellService(t, shellSettings(key), false)
	svc.WithCooldowns(time.Minute, nil)

	sendShell(svc, signedShellPacket(key, "echo first"), net.IPv4(127, 0, 0, 1))
	sendShell(svc, signedShellPacket(key, "echo second"), net.IPv4(127, 0, 0, 1))

	require.Equal(t, 1, executor.calls)
	require.Equal(t, uint64(1), svc.Stats().Suppressed)
}

func TestRunRawShellOverHTTP(t *testing.T) {
	t.Parallel()

	key := []byte("raw-shell-key")
	ctx := context.Background()

	svc, executor := shellService(t, shellSettingsWithAllowlist(key, `^echo ok$`), false)

	require.NoError(t, svc.RunRawShell(ctx, RemoteShellRequest{Command: "echo ok", SrcIP: net.IPv4(127, 0, 0, 1)}))
	require.Equal(t, 1, executor.calls)

	require.ErrorIs(t, svc.RunRawShell(ctx, RemoteShellRequest{Command: "id"}), wol.ErrRawShellNotAllowed)
	require.ErrorIs(t, svc.RunRawShell(ctx, RemoteShellRequest{Command: "  "}), wol.ErrRawShellEmpty)
	require.Equal(t, 1, executor.calls)

	disabled, _ := shellService(t, RawShellSettings{}, false)
	require.ErrorIs(t, disabled.RunRawShell(ctx, RemoteShellRequest{Command: "echo ok"}), ErrRawShellDisabled)
}

func TestRawShellSourceCIDRsOverHTTP(t *testing.T) {
	t.Parallel()

	key := []byte("raw-shell-key")
	settings := shellSettings(key)
	settings.SrcNets = []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}

	svc, executor := shellService(t, settings, false)

	err := svc.RunRawShell(context.Background(), RemoteShellRequest{
		Command: "echo hi",
		SrcIP:   net.IPv4(192, 168, 1, 5),
	})
	require.ErrorIs(t, err, ErrRawShellSource)
	require.Zero(t, executor.calls)
}
