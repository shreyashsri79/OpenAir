//go:build !windows

package discovery

import (
	"net"
	"syscall"
)

// setBroadcast asks the kernel for permission to send to a broadcast address.
//
// Go's net package does not set SO_BROADCAST, and on Linux a sendto() to a
// broadcast destination without it fails with EPERM/EACCES. There is no way to
// reach §15.2's subnet broadcast without this call.
func setBroadcast(c *net.UDPConn) error {
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var setErr error
	err = rc.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	})
	if err != nil {
		return err
	}
	return setErr
}
