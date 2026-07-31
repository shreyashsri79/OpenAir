package ipc

import (
	"context"
	"fmt"
	"net"
	"os/user"
	"time"

	"github.com/Microsoft/go-winio"
)

// DefaultSocketPath is the daemon's named pipe.
//
// The user's SID is in the name because a pipe name is machine-global: two
// users logged into the same Windows machine each run their own daemon, and a
// shared name would have the second one fail to bind rather than get its own.
func DefaultSocketPath() string {
	if u, err := user.Current(); err == nil && u.Uid != "" {
		return `\\.\pipe\openaird-` + u.Uid
	}
	return `\\.\pipe\openaird`
}

// ownerOnlySDDL grants full control to the owning user and to SYSTEM, and to
// nobody else. `P` makes the DACL protected, so nothing is inherited in on top
// of it.
//
// This is the Windows half of D-29's requirement. Filesystem permissions do not
// apply to a named pipe; without an explicit descriptor a pipe is reachable by
// every user on the machine, and anything that reaches it drives the daemon.
func ownerOnlySDDL() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("current user: %w", err)
	}
	if u.Uid == "" {
		return "", fmt.Errorf("current user has no SID")
	}
	return fmt.Sprintf("D:P(A;;GA;;;%s)(A;;GA;;;SY)", u.Uid), nil
}

// Listen binds the daemon's named pipe with an owner-only ACL.
func Listen(path string) (net.Listener, error) {
	sddl, err := ownerOnlySDDL()
	if err != nil {
		return nil, err
	}
	ln, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		MessageMode:        false, // the envelope frames itself (§3)
	})
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	return ln, nil
}

// Dial connects to a running daemon.
func Dial(path string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return winio.DialPipeContext(ctx, path)
}
