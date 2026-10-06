package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

// stampAndTag renders the [stamp][tag] tail a windowed channel expects.
func stampAndTag(t *testing.T, key []byte, prefix []byte, segment string, at time.Time) []byte {
	t.Helper()

	return wol.RemoteStampSignature(key, prefix, []byte(segment), at)
}

func TestRemoteRunnerRefusesReplay(t *testing.T) {
	t.Parallel()

	key := []byte("command-key")
	at := time.Unix(1_700_000_000, 0)

	var refused []string

	runner := newRemoteRunner(RemoteSettings{
		Commands: map[string]wol.RemoteCommand{"backup": remoteTestCommand()},
		Ports:    []int{remoteTestPort},
		Key:      key,
		Window:   time.Minute,
		OnReject: func(reason string) { refused = append(refused, reason) },
	})
	runner.now = func() time.Time { return at }

	prefix := wol.BuildMagicPacket(testMAC())
	segment := "backup:target=home"
	stamped := append([]byte(segment), stampAndTag(t, key, prefix, segment, at)...)

	cmd, args, err := runner.resolve(prefix, stamped)
	require.NoError(t, err)
	require.Equal(t, "backup", cmd.ID)
	require.Equal(t, map[string]string{"target": "home"}, args)

	// The same bytes again: correctly authenticated, but already used.
	_, _, err = runner.resolve(prefix, stamped)
	require.ErrorIs(t, err, ErrRemoteReplay)
	require.Equal(t, []string{wol.ReplaySeen}, refused)

	// A stamp older than the window is refused as well, and for its own reason.
	stale := append([]byte(segment), stampAndTag(t, key, prefix, segment, at.Add(-2*time.Minute))...)
	_, _, err = runner.resolve(prefix, stale)
	require.ErrorIs(t, err, ErrRemoteReplay)
	require.Equal(t, []string{wol.ReplaySeen, wol.ReplayStale}, refused)

	// The pre-window wire format (segment||tag, no stamp) is refused, and - as with packets -
	// it is refused as stale rather than as malformed: the split happens to reconstruct exactly
	// the bytes the tag covers, so the signature still verifies. What matters is the outcome:
	// nothing runs until the sender speaks the stamped format.
	_, _, err = runner.resolve(prefix, signedRemotePacket(key, segment)[len(prefix):])
	require.ErrorIs(t, err, ErrRemoteReplay)

	// A payload that cannot even be split is refused as malformed.
	_, _, err = runner.resolve(prefix, []byte("short"))
	require.ErrorIs(t, err, wol.ErrRemoteSignature)
}

func TestRemoteRunnerWithoutWindowStaysUnsigned(t *testing.T) {
	t.Parallel()

	// No window: the documented §21 wire format is unchanged, so an operator who does not ask
	// for replay protection gets exactly what they had.
	key := []byte("command-key")

	runner := newRemoteRunner(RemoteSettings{
		Commands: map[string]wol.RemoteCommand{"backup": remoteTestCommand()},
		Ports:    []int{remoteTestPort},
		Key:      key,
	})
	require.Nil(t, runner.guard, "no window, no guard")

	prefix := wol.BuildMagicPacket(testMAC())
	payload := signedRemotePacket(key, "backup:target=home")

	for range 2 {
		_, _, err := runner.resolve(prefix, payload[len(prefix):])
		require.NoError(t, err)
	}
}

func TestRawShellRunnerRefusesReplay(t *testing.T) {
	t.Parallel()

	key := []byte("raw-key")
	at := time.Unix(1_700_000_000, 0)

	var refused []string

	settings := RawShellSettings{
		Enabled:  true,
		Ports:    []int{rawShellTestPort},
		Key:      key,
		Exec:     wol.ExecParams{Command: []string{"/bin/echo"}},
		Window:   time.Minute,
		OnReject: func(reason string) { refused = append(refused, reason) },
	}

	runner := newRawShellRunner(settings.Enabled, settings.Ports, settings.Key, settings.SrcNets,
		settings.Allowlist, settings.Exec, settings.Window, settings.OnReject)
	require.NotNil(t, runner)
	runner.now = func() time.Time { return at }

	prefix := wol.BuildMagicPacket(testMAC())
	segment := "echo hello"
	content := append([]byte(segment), stampAndTag(t, key, prefix, segment, at)...)

	command, err := runner.command(prefix, content)
	require.NoError(t, err)
	require.Equal(t, segment, command)

	// A captured packet sent twice no longer runs twice.
	_, err = runner.command(prefix, content)
	require.ErrorIs(t, err, ErrRemoteReplay)
	require.Equal(t, []string{wol.ReplaySeen}, refused)
}
