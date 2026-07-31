//go:build !windows

package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// DefaultSocketPath is where the daemon listens when nothing says otherwise.
//
// The runtime directory is preferred because it is per-user, mode 0700 by
// systemd's construction, and cleared at logout -- which is the right lifetime
// for a socket that only means anything while the daemon is running. The
// config directory is the fallback for systems without one, and gets the same
// permissions applied explicitly.
func DefaultSocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "openair", "openaird.sock")
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "openair", "openaird.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("openair-%d", os.Getuid()), "openaird.sock")
}

// Listen binds the daemon's socket.
//
// The directory is created 0700 and the socket itself is 0600. Both matter:
// the socket mode is what most systems enforce, and the directory mode is what
// the ones that ignore socket permissions on connect (historically some BSDs)
// still honour. Anything that can open this socket can drive the daemon with
// this device's identity key, so this is the trust boundary D-29 names.
func Listen(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	// A directory that already existed may be looser than we want.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure socket directory: %w", err)
	}

	if err := clearStale(path); err != nil {
		return nil, err
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("secure socket: %w", err)
	}
	return ln, nil
}

// clearStale removes a socket left behind by a daemon that did not shut down
// cleanly. It refuses to remove one that answers, because that is a daemon
// already running and taking its socket would silently steal its clients.
func clearStale(path string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	c, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if err == nil {
		c.Close()
		return fmt.Errorf("a daemon is already listening on %s", path)
	}
	return os.Remove(path)
}

// Dial connects to a running daemon.
func Dial(path string) (net.Conn, error) {
	return net.DialTimeout("unix", path, 2*time.Second)
}
