//go:build !windows

package logging

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeLine keeps the tests readable: the logger writes through unbuffered, so a plain write is
// what the file gets.
func writeLine(t *testing.T, out io.Writer, line string) {
	t.Helper()

	_, err := io.WriteString(out, line+"\n")
	require.NoError(t, err)
}

// TestOpenOutputRoutesToTheConfiguredDestination pins the mapping and the mode of a log file: the
// audit log names source addresses, the action that ran and command lines, so it is not readable
// by other local accounts.
func TestOpenOutputRoutesToTheConfiguredDestination(t *testing.T) {
	t.Parallel()

	t.Run("empty and stderr mean stderr", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"", OutputStderr, "STDERR", " stderr "} {
			out, err := OpenOutput(name, "")
			require.NoError(t, err, "output %q", name)
			require.Equal(t, os.Stderr, out)
		}
	})

	t.Run("stdout", func(t *testing.T) {
		t.Parallel()

		out, err := OpenOutput(OutputStdout, "")
		require.NoError(t, err)
		require.Equal(t, os.Stdout, out)
	})

	t.Run("a file output without a path is refused", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"", "   "} {
			_, err := OpenOutput(OutputFile, name)
			require.ErrorIs(t, err, ErrLogFileMissing)
		}
	})

	t.Run("an unknown output names the alternatives", func(t *testing.T) {
		t.Parallel()

		_, err := OpenOutput("syslog", "")
		require.ErrorIs(t, err, ErrUnknownOutput)
		require.Contains(t, err.Error(), "stderr|stdout|file")
	})

	t.Run("an unusable path fails instead of losing the audit trail", func(t *testing.T) {
		t.Parallel()

		_, err := OpenOutput(OutputFile, filepath.Join(t.TempDir(), "missing", "audit.log"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "cannot open the log file")
	})
}

// TestOpenLogFileCreatesItOnceAndAppends checks the file half: the mode keeps the audit log away
// from other local accounts, and reopening the same path appends instead of truncating, so a
// restart does not erase what a reload or a crash left behind.
func TestOpenLogFileCreatesItOnceAndAppends(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")

	out, err := OpenOutput(OutputFile, path)
	require.NoError(t, err)

	file, ok := out.(*os.File)
	require.True(t, ok)
	t.Cleanup(func() { _ = file.Close() })

	writeLine(t, out, "first")

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(logFileMode), info.Mode().Perm())

	// Opening the same path again appends: a restart must not erase the history that a reload
	// or a crash left behind.
	again, err := OpenOutput(OutputFile, path)
	require.NoError(t, err)

	second, ok := again.(*os.File)
	require.True(t, ok)
	t.Cleanup(func() { _ = second.Close() })

	writeLine(t, again, "second")

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(body), "first")
	require.Contains(t, string(body), "second")
}
