//go:build unix

package exec

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	osexec "os/exec"
	"os/user"
	"strconv"
	"syscall"
)

var (
	// ErrUnknownUser reports a user that cannot be resolved.
	ErrUnknownUser = errors.New("unknown exec user")
	// ErrUnknownGroup reports a group that cannot be resolved.
	ErrUnknownGroup = errors.New("unknown exec group")
	// ErrNotRoot reports a configured privilege drop without the rights to perform it:
	// silently running as the current user would make the configuration a lie.
	//
	// Root is the only accepted setup: requirePrivilege is a plain geteuid check, so a process
	// that holds CAP_SETUID/CAP_SETGID without root is refused too. The message must not promise
	// that path - a runbook granting just those two capabilities would be following our text.
	ErrNotRoot = errors.New("exec user/group requires root")
)

// credential is the uid/gid pair an exec command runs as, plus the supplementary
// groups it is allowed to keep.
type credential struct {
	uid    uint32
	gid    uint32
	groups []uint32
	// set reports whether a drop was configured at all: "user: root" is a real
	// configuration (uid 0) that must still clear sol's supplementary groups.
	set bool
}

// isZero reports whether no privilege drop was configured.
func (c credential) isZero() bool {
	return !c.set
}

// resolveCredential resolves the configured user/group; empty values mean "inherit".
// A user without an explicit group keeps that account's primary group.
func resolveCredential(userName, groupName string) (credential, error) {
	if userName == "" && groupName == "" {
		return credential{}, nil
	}

	cred := credential{set: true}

	if userName != "" {
		account, err := lookupAccount(userName)
		if err != nil {
			return credential{}, err
		}

		if cred.uid, err = parseID(account.Uid, ErrUnknownUser, userName); err != nil {
			return credential{}, err
		}

		if cred.gid, err = parseID(account.Gid, ErrUnknownGroup, userName); err != nil {
			return credential{}, err
		}

		cred.groups = accountGroups(account)
	}

	if groupName != "" {
		gid, err := groupID(groupName)
		if err != nil {
			return credential{}, err
		}

		cred.gid = gid
	}

	return cred, nil
}

// lookupAccount resolves a user name or numeric id.
func lookupAccount(value string) (*user.User, error) {
	account, err := user.Lookup(value)
	if err != nil {
		account, err = user.LookupId(value)
	}

	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownUser, value)
	}

	return account, nil
}

// accountGroups lists the supplementary groups of the account so that the command
// runs with that user's groups instead of inheriting sol's own (root's) groups.
// On failure it returns nil, which clears every supplementary group.
func accountGroups(account *user.User) []uint32 {
	ids, err := account.GroupIds()
	if err != nil {
		slog.Warn("cannot list the exec user's groups: running without supplementary groups",
			"user", account.Username,
			"error", err,
		)

		return nil
	}

	out := make([]uint32, 0, len(ids))

	for _, id := range ids {
		value, convErr := strconv.ParseUint(id, 10, 32)
		if convErr != nil {
			continue
		}

		out = append(out, uint32(value))
	}

	return out
}

// groupID resolves a group name or numeric id.
func groupID(value string) (uint32, error) {
	group, err := user.LookupGroup(value)
	if err != nil {
		group, err = user.LookupGroupId(value)
	}

	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrUnknownGroup, value)
	}

	return parseID(group.Gid, ErrUnknownGroup, value)
}

func parseID(value string, sentinel error, name string) (uint32, error) {
	id, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", sentinel, name)
	}

	return uint32(id), nil
}

// requirePrivilege reports whether the process may drop privileges at all.
// It is deliberately a geteuid check and nothing more: capability-only setups
// (CAP_SETUID/CAP_SETGID without root) are unsupported, and saying so plainly is
// better than half-working drops.
func requirePrivilege() error {
	if os.Geteuid() != 0 {
		return ErrNotRoot
	}

	return nil
}

// applyCredential wires the drop into the command's process attributes.
func applyCredential(cmd *osexec.Cmd, cred credential) {
	if cred.isZero() {
		return
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: cred.uid,
			Gid: cred.gid,
			// NoSetGroups stays false on purpose: setgroups then runs with the target
			// user's groups (or with an empty list), so sol's own supplementary groups
			// -- root's and the invoking account's -- never leak into the command.
			Groups:      cred.groups,
			NoSetGroups: false,
		},
	}
}
