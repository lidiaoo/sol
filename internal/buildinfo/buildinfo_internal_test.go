package buildinfo

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests deliberately do not run in parallel: they edit the package-level stamp.

// TestVersionUsesTheLinkTimeStamp pins the precedence: when the linker stamped a version, that
// string is what every caller sees, whatever the toolchain embedded by itself.
func TestVersionUsesTheLinkTimeStamp(t *testing.T) {
	t.Cleanup(func() { version = "" })

	version = "v9.9.9-test"

	require.Equal(t, "v9.9.9-test", Version())
}

// TestVersionIsNeverEmpty covers the other branch: an unstamped build still has to report
// something printable, since the value goes straight into /v1/status and --version.
func TestVersionIsNeverEmpty(t *testing.T) {
	t.Cleanup(func() { version = "" })

	version = ""

	require.NotEmpty(t, Version())
}

// TestRevisionShape checks the format rather than the value: the revision comes from the
// toolchain's own VCS stamping, so whether it is there depends on where the binary was built (a
// checkout stamps it, a module cache does not).
func TestRevisionShape(t *testing.T) {
	t.Parallel()

	revision := Revision()
	if revision == "" {
		t.Skip("this build carries no VCS information")
	}

	require.Regexp(t, `^[0-9a-f]{7,12}(-dirty)?$`, revision)
	require.LessOrEqual(t, len(revision), revisionLength+len("-dirty"))
}

// TestWithDirtyHonoursTheLinkTimeDirtyStamp pins the rule for the "-dirty" suffix. It is a pure
// function precisely so this does not depend on the build carrying VCS information: the earlier
// version of this test was skipped in some environments and proved nothing there.
func TestWithDirtyHonoursTheLinkTimeDirtyStamp(t *testing.T) {
	t.Cleanup(func() { dirty = "" })

	// Not stamped: whatever the toolchain said stands, so a plain `go build` is unchanged.
	dirty = ""
	require.Equal(t, "abc1234", withDirty("abc1234", false))
	require.Equal(t, "abc1234-dirty", withDirty("abc1234", true))

	// Stamped clean: the toolchain's flag does not override it. This is the case that matters --
	// vcs.modified counts untracked files, and building creates them (dist/stage/...).
	dirty = "false"
	require.Equal(t, "abc1234", withDirty("abc1234", true))

	// Stamped dirty: honest even when the toolchain saw nothing.
	dirty = "true"
	require.Equal(t, "abc1234-dirty", withDirty("abc1234", false))
}
