package cmd

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWatchConfigFileReloadsOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sol.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\n"), 0o600))

	var calls atomic.Int64

	watchConfigFile(t.Context(), path, 10*time.Millisecond, func(context.Context) error {
		calls.Add(1)

		return nil
	})

	// An unchanged file must not reload: the watcher compares size and modification time.
	time.Sleep(60 * time.Millisecond)
	require.Zero(t, calls.Load(), "an untouched config must not trigger a reload")

	require.NoError(t, os.WriteFile(path, []byte("version: 1\n# a change\n"), 0o600))
	require.Eventually(t, func() bool { return calls.Load() == 1 }, time.Second, 5*time.Millisecond)

	// The stamp is refreshed before the reload, so one change means one reload.
	time.Sleep(60 * time.Millisecond)
	require.Equal(t, int64(1), calls.Load(), "a single change must not reload on every tick")
}

func TestWatchConfigFileCatchesACreatedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sol.yaml")

	var calls atomic.Int64

	watchConfigFile(t.Context(), path, 10*time.Millisecond, func(context.Context) error {
		calls.Add(1)

		return nil
	})

	// A missing file has a zero stamp, so creating it counts as a change.
	require.NoError(t, os.WriteFile(path, []byte("version: 1\n"), 0o600))
	require.Eventually(t, func() bool { return calls.Load() == 1 }, time.Second, 5*time.Millisecond)
}

func TestWatchConfigFileDisabled(t *testing.T) {
	var calls atomic.Int64

	reload := func(context.Context) error {
		calls.Add(1)

		return nil
	}

	// No path (config came from flags/defaults) or no interval: no watcher at all.
	watchConfigFile(t.Context(), "", time.Millisecond, reload)
	watchConfigFile(t.Context(), "/tmp/sol-does-not-exist.yaml", 0, reload)

	time.Sleep(30 * time.Millisecond)
	require.Zero(t, calls.Load())
}

func TestWatchConfigFileStopsWithTheContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sol.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\n"), 0o600))

	ctx, cancel := context.WithCancel(context.Background())

	var calls atomic.Int64

	watchConfigFile(ctx, path, 5*time.Millisecond, func(context.Context) error {
		calls.Add(1)

		return nil
	})

	cancel()
	time.Sleep(20 * time.Millisecond)

	// After the context is done the watcher stops observing the file.
	require.NoError(t, os.WriteFile(path, []byte("version: 1\n# after cancel\n"), 0o600))
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, calls.Load())
}
