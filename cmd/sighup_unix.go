//go:build unix

package cmd

import (
	"os"
	"syscall"
)

// reloadSignals are the signals that trigger a configuration reload.
func reloadSignals() []os.Signal {
	return []os.Signal{syscall.SIGHUP}
}
