package discovery

import (
	"net"
	"syscall"
)

// setBroadcast is the Windows half of the SO_BROADCAST call; see the comment
// on the !windows build. Winsock spells the socket a Handle rather than an
// int, which is the entire reason this file exists separately.
func setBroadcast(c *net.UDPConn) error {
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var setErr error
	err = rc.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	})
	if err != nil {
		return err
	}
	return setErr
}
