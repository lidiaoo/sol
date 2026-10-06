// Package system holds the privileged and platform-dependent pieces of the process: the power
// actions, and the questions about what this process is allowed to do.
package system

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// capNetBindService is CAP_NET_BIND_SERVICE, bit 10 of the effective capability set.
const capNetBindService = 1 << 10

// procSelfStatus is where Linux reports the process' capability sets.
const procSelfStatus = "/proc/self/status"

// CanBindPrivilegedPorts reports whether this process may bind a port below 1024 without failing
// at start-up. It exists so `sol status` can warn about a configuration that needs privilege
// without warning about the very setups the README recommends: root, and a service unit granting
// CAP_NET_BIND_SERVICE, both answer yes.
func CanBindPrivilegedPorts() bool {
	if runtime.GOOS == "windows" {
		// Windows has no privileged port range, so nothing is needed.
		return true
	}

	if os.Geteuid() == 0 {
		return true
	}

	if runtime.GOOS != "linux" {
		// macOS and the BSDs give no way to look, and below 1024 they require root.
		return false
	}

	// Linux: a non-root process can still hold CAP_NET_BIND_SERVICE, which is exactly the
	// hardened setup the documentation suggests.
	status, err := os.ReadFile(procSelfStatus)
	if err != nil {
		return false
	}

	return hasNetBindService(string(status))
}

// hasNetBindService reports whether a /proc/self/status dump carries CAP_NET_BIND_SERVICE in
// CapEff. It is separate from the read so the parse can be tested without /proc.
func hasNetBindService(status string) bool {
	for line := range strings.SplitSeq(status, "\n") {
		value, found := strings.CutPrefix(line, "CapEff:")
		if !found {
			continue
		}

		mask, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
		if err != nil {
			return false
		}

		return mask&capNetBindService != 0
	}

	return false
}
