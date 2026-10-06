//go:build !unix

package exec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// These run on platforms without setuid, where a configured drop must fail at start-up rather
// than silently execute as the current account. They cannot be executed on a linux box, so the
// evidence is that they compile and vet cleanly for windows and darwin
// (GOOS=windows go vet ./internal/infra/exec/), plus the shared code path they exercise.
func TestResolveCredentialRejectsDropsOffUnix(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ user, group string }{
		{user: "nobody"},
		{group: "nobody"},
		{user: "nobody", group: "nogroup"},
		{user: "0"},
	} {
		cred, err := resolveCredential(tc.user, tc.group)
		require.ErrorIs(t, err, ErrUserUnsupported, "user=%q group=%q", tc.user, tc.group)
		require.True(t, cred.isZero())
	}
}

func TestResolveCredentialStaysEmptyWithoutADrop(t *testing.T) {
	t.Parallel()

	cred, err := resolveCredential("", "")
	require.NoError(t, err)
	require.True(t, cred.isZero())
	require.NoError(t, requirePrivilege())
}
