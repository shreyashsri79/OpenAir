//go:build unix && !linux

package identity

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// DisableCoreDumps stops this process producing a core file (D-19).
//
// The ptrace half of the Linux implementation has no portable equivalent here:
// macOS uses entitlements and the BSDs a sysctl, neither of which a process can
// set for itself. A same-uid debugger can therefore still attach on these
// platforms, which belongs in the threat model rather than in a comment that
// implies otherwise.
func DisableCoreDumps() error {
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0}); err != nil {
		return fmt.Errorf("identity: disable core dumps: %w", err)
	}
	return nil
}
