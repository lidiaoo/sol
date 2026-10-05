//go:build unix

package exec

import (
	"os"
	osexec "os/exec"
	"os/user"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

func TestResolveCredentialInheritsWhenUnset(t *testing.T) {
	cred, err := resolveCredential("", "")
	require.NoError(t, err)
	require.True(t, cred.isZero(), "no user/group configured means sol keeps its own identity")
}

func TestResolveCredentialFromIDsAndName(t *testing.T) {
	cred, err := resolveCredential(strconv.Itoa(os.Getuid()), "")
	require.NoError(t, err)
	require.EqualValues(t, os.Getuid(), cred.uid)
	require.EqualValues(t, os.Getgid(), cred.gid, "a user without an explicit group keeps its primary group")

	cred, err = resolveCredential("", strconv.Itoa(os.Getgid()))
	require.NoError(t, err)
	require.Zero(t, cred.uid)
	require.EqualValues(t, os.Getgid(), cred.gid)
}

func TestResolveCredentialRejectsUnknownEntries(t *testing.T) {
	_, err := resolveCredential("sol-no-such-user-xyz", "")
	require.ErrorIs(t, err, ErrUnknownUser)

	_, err = resolveCredential("", "sol-no-such-group-xyz")
	require.ErrorIs(t, err, ErrUnknownGroup)

	_, err = resolveCredential(strconv.Itoa(os.Getuid()), "sol-no-such-group-xyz")
	require.ErrorIs(t, err, ErrUnknownGroup)
}

func TestValidateCredentialFailsFast(t *testing.T) {
	executor := NewExecutor(nil)

	unknown := wol.ActionDef{Name: "lock", Type: wol.ActionTypeExec, Exec: &wol.ExecParams{
		Command: []string{"/bin/true"},
		User:    "sol-no-such-user-xyz",
	}}
	require.ErrorIs(t, executor.Validate(unknown), ErrUnknownUser)

	drop := wol.ActionDef{Name: "lock", Type: wol.ActionTypeExec, Exec: &wol.ExecParams{
		Command: []string{"/bin/true"},
		User:    strconv.Itoa(os.Getuid()),
	}}

	if os.Geteuid() != 0 {
		// not root: a configured drop must be refused at startup rather than
		// silently running the command with sol's own identity
		require.ErrorIs(t, executor.Validate(drop), ErrNotRoot)
	}
}

func TestApplyCredentialWiring(t *testing.T) {
	cmd := &osexec.Cmd{}
	applyCredential(cmd, credential{uid: 1234, gid: 5678, groups: []uint32{5678, 999}, set: true})

	require.NotNil(t, cmd.SysProcAttr)
	require.NotNil(t, cmd.SysProcAttr.Credential)
	require.Equal(t, uint32(1234), cmd.SysProcAttr.Credential.Uid)
	require.Equal(t, uint32(5678), cmd.SysProcAttr.Credential.Gid)
	require.Equal(t, []uint32{5678, 999}, cmd.SysProcAttr.Credential.Groups,
		"the command gets the target user's groups")
	require.False(t, cmd.SysProcAttr.Credential.NoSetGroups,
		"setgroups must run: sol's own supplementary groups (root, docker, ...) cannot leak")

	inherit := &osexec.Cmd{}
	applyCredential(inherit, credential{})
	require.Nil(t, inherit.SysProcAttr)
}

func TestResolveCredentialUsesTheAccountGroups(t *testing.T) {
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	require.NoError(t, err)

	ids, err := account.GroupIds()
	require.NoError(t, err)

	want := make([]uint32, 0, len(ids))

	for _, id := range ids {
		value, convErr := strconv.ParseUint(id, 10, 32)
		require.NoError(t, convErr)

		want = append(want, uint32(value))
	}

	cred, err := resolveCredential(strconv.Itoa(os.Getuid()), "")
	require.NoError(t, err)
	require.False(t, cred.isZero())
	require.EqualValues(t, os.Getuid(), cred.uid)
	require.Equal(t, want, cred.groups,
		"the command runs with the target account's groups, never with sol's inherited ones")
}

func TestDropLabel(t *testing.T) {
	tests := []struct {
		name   string
		params wol.ExecParams
		want   string
	}{
		{name: "none", params: wol.ExecParams{}, want: ""},
		{name: "user", params: wol.ExecParams{User: "nobody"}, want: "nobody"},
		{name: "group", params: wol.ExecParams{Group: "nogroup"}, want: ":nogroup"},
		{name: "both", params: wol.ExecParams{User: "nobody", Group: "nogroup"}, want: "nobody:nogroup"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := tc.params
			require.Equal(t, tc.want, dropLabel(&params))
		})
	}
}
