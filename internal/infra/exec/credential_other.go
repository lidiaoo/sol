//go:build !unix

package exec

import (
	"errors"
	osexec "os/exec"
)

// ErrUserUnsupported reports user/group on platforms without setuid support.
var ErrUserUnsupported = errors.New("exec user/group is not supported on this platform")

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
