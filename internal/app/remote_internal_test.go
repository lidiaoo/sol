package app

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

const remoteTestPort = 10014

func remoteTestCommand() wol.RemoteCommand {
	return wol.RemoteCommand{
		ID:   "backup",
		Exec: wol.ExecParams{Command: []string{"/usr/local/bin/backup.sh", "--target={{.Arg.target}}"}},
		Args: map[string]wol.ArgSpec{
			"target": {Type: wol.ArgTypeString, Enum: []string{"home"}, Required: true},
		},
	}
}

// signedRemotePacket builds a magic packet carrying a signed remote command segment.
func signedRemotePacket(key []byte, segment string) []byte {
	pkt := wol.BuildMagicPacket(testMAC())
	tag := wol.RemoteSignature(key, pkt, []byte(segment))

	return append(pkt, append([]byte(segment), tag...)...)
}

// remoteService returns a listener whose remote command port also carries a noop rule,
// so tests can tell rule matching and remote commands apart.
func remoteService(t *testing.T, key []byte) (*ListenService, *executorMock) {
	t.Helper()

	ifaces := testIfaces()
	policy := mustPolicy(t, ifaces, []wol.Rule{ruleFor(remoteTestPort, wol.Action("noop"))})
	executor := &executorMock{}
	registry := testRegistry(executor)

	cmd := remoteTestCommand()
	registry.RegisterAction(wol.ActionDef{Name: cmd.Action(), Type: wol.ActionTypeExec, Exec: &cmd.Exec})

	svc := NewListenService(&factoryMock{}, registry, policy, ifaces, false).
		WithRemoteCommands(RemoteSettings{
			Commands: map[string]wol.RemoteCommand{"backup": remoteTestCommand()},
			Ports:    []int{remoteTestPort},
			Key:      key,
		})

	return svc, executor
}

func sendRemote(svc *ListenService, payload []byte) {
	svc.handlePacket(context.Background(), packet{
		payload: payload,
		port:    remoteTestPort,
		src:     &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 20001},
	})
}

func TestRemoteRunnerDisabledWithoutPorts(t *testing.T) {
	t.Parallel()

	runner := newRemoteRunner(RemoteSettings{Commands: map[string]wol.RemoteCommand{"backup": remoteTestCommand()}, Key: []byte("k")})

	require.Nil(t, runner)
	require.False(t, runner.accepts(remoteTestPort))

	_, err := runner.manual("backup", nil)
	require.ErrorIs(t, err, ErrRemoteDisabled)
}

func TestListenServiceRunsRemoteCommand(t *testing.T) {
	t.Parallel()

	key := []byte("shared-secret")
	svc, executor := remoteService(t, key)

	sendRemote(svc, signedRemotePacket(key, "backup:target=home"))

	require.Equal(t, 1, executor.calls)
	require.Equal(t, wol.RemoteAction("backup"), executor.action)
	require.Equal(t, map[string]string{"target": "home"}, executor.event.Args)
	require.Equal(t, uint64(1), svc.Stats().Matched)
}

func TestListenServiceRejectsRemoteCommands(t *testing.T) {
	t.Parallel()

	key := []byte("shared-secret")

	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "bad signature", payload: signedRemotePacket([]byte("other-key"), "backup:target=home")},
		{name: "unknown id", payload: signedRemotePacket(key, "lock")},
		{name: "missing argument", payload: signedRemotePacket(key, "backup")},
		{name: "rejected enum", payload: signedRemotePacket(key, "backup:target=cloud")},
		{name: "malformed segment", payload: signedRemotePacket(key, "backup:target")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, executor := remoteService(t, key)

			sendRemote(svc, tc.payload)

			require.Zero(t, executor.calls)
			require.Zero(t, svc.Stats().Matched)
		})
	}
}

func TestListenServiceRemotePortFallsBackToRules(t *testing.T) {
	t.Parallel()

	key := []byte("shared-secret")
	svc, executor := remoteService(t, key)

	// A plain magic packet carries no segment, so the rules still apply on that port.
	sendRemote(svc, wol.BuildMagicPacket(testMAC()))

	require.Equal(t, 1, executor.calls)
	require.Equal(t, wol.Action("noop"), executor.action)
}

func TestListenServiceRunRemoteCommand(t *testing.T) {
	t.Parallel()

	key := []byte("shared-secret")
	svc, executor := remoteService(t, key)
	ctx := context.Background()

	require.NoError(t, svc.RunRemoteCommand(ctx, "backup", map[string]string{"target": "home"}))
	require.Equal(t, 1, executor.calls)
	require.Equal(t, wol.RemoteAction("backup"), executor.action)
	require.Equal(t, map[string]string{"target": "home"}, executor.event.Args)

	require.ErrorIs(t, svc.RunRemoteCommand(ctx, "lock", nil), ErrRemoteUnknownCommand)
	require.ErrorIs(t, svc.RunRemoteCommand(ctx, "backup", nil), wol.ErrRemoteMissingArg)
	require.ErrorIs(t, svc.RunRemoteCommand(ctx, "backup", map[string]string{"target": "cloud"}), wol.ErrRemoteArgValue)

	disabled, _ := remoteServiceWithoutCommands(t)
	require.ErrorIs(t, disabled.RunRemoteCommand(ctx, "backup", nil), ErrRemoteDisabled)
}

func remoteServiceWithoutCommands(t *testing.T) (*ListenService, *executorMock) {
	t.Helper()

	ifaces := testIfaces()
	policy := mustPolicy(t, ifaces, []wol.Rule{ruleFor(remoteTestPort, wol.Action("noop"))})
	executor := &executorMock{}

	return NewListenService(&factoryMock{}, testRegistry(executor), policy, ifaces, false), executor
}
