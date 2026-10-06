//go:build !unix

package exec

import (
	osexec "os/exec"
)

// ErrUserUnsupported is defined in executor.go so that builds on other platforms can name it
// too; it is raised here, where user/group would have to be applied.

// credential is empty outside unix: privilege drops are not attempted there.
type credential struct{}

// isZero always reports true on platforms without setuid support.
func (credential) isZero() bool {
	return true
}

func resolveCredential(userName, groupName string) (credential, error) {
	if userName == "" && groupName == "" {
		return credential{}, nil
	}

	return credential{}, ErrUserUnsupported
}

func requirePrivilege() error {
	return nil
}

func applyCredential(_ *osexec.Cmd, _ credential) {
	// nothing to do: resolveCredential rejects any configured drop on this platform
}
