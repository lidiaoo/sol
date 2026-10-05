//go:build !unix

package wolsend

import "net"

// enableBroadcast is a no-op where the standard library does not expose the option; sending to
// a subnet broadcast is unix-specific here.
func enableBroadcast(*net.UDPConn) error {
	return nil
}
