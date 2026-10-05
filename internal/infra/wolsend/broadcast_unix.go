//go:build unix

package wolsend

import (
	"net"
	"syscall"
)

// enableBroadcast sets SO_BROADCAST on the sending socket. A plain dial leaves it off, and the
// kernel then refuses a datagram addressed to a broadcast address with EACCES, which would make
// the default destination (255.255.255.255) fail at runtime instead of at start-up.
func enableBroadcast(conn *net.UDPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}

	var sockErr error

	//nolint:gosec // the file descriptor is a small non-negative int, as the kernel API expects
	if err := raw.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	}); err != nil {
		return err
	}

	return sockErr
}
