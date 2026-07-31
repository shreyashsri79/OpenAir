package identity

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// DisableCoreDumps stops this process producing a core file and stops other
// processes attaching to it.
//
// D-19 requires the decrypted privilege key never to reach a core dump. Two
// steps, because they cover different holes:
//
//   - RLIMIT_CORE 0 means the kernel writes no core file for this process.
//   - PR_SET_DUMPABLE 0 additionally makes the process non-attachable by a
//     debugger running as the same user, and hands ownership of its /proc
//     entries to root. A key locked into RAM is of little use if any process
//     of the same uid can read it out with ptrace.
//
// Callers should invoke this once at startup, before any privilege key is
// unsealed. It affects only this process and is not inherited usefully, so a
// library cannot do it on a caller's behalf.
func DisableCoreDumps() error {
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0}); err != nil {
		return fmt.Errorf("identity: disable core dumps: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("identity: mark process non-dumpable: %w", err)
	}
	return nil
}
