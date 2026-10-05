//go:build !unix

package cmd

import "os"

// reloadSignals is empty where SIGHUP does not exist; POST /v1/reload still works.
func reloadSignals() []os.Signal {
	return nil
}
