package ipc

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// CheckPeer refuses a connection from another user.
//
// Socket permissions are the primary control and this is the second one, for
// the case they do not hold: a socket in a directory some administrator has
// loosened, a filesystem mounted without permission enforcement, or a
// descriptor passed to another user's process. The kernel answers who is on
// the other end, so a caller cannot claim otherwise.
//
// root is not special-cased. A root process can read the key file directly and
// does not need the socket, so admitting it would widen the boundary for no
// capability it lacks.
func CheckPeer(conn net.Conn) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		// Not a unix socket -- a test pipe, or a future transport. Nothing to
		// check, and refusing here would break the in-process tests that use
		// net.Pipe.
		return nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return fmt.Errorf("peer credentials: %w", err)
	}

	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return fmt.Errorf("peer credentials: %w", err)
	}
	if credErr != nil {
		return fmt.Errorf("peer credentials: %w", credErr)
	}

	if want := os.Getuid(); int(cred.Uid) != want {
		return fmt.Errorf("refusing connection from uid %d (pid %d); this daemon serves uid %d only",
			cred.Uid, cred.Pid, want)
	}
	return nil
}
