//go:build unix

package identity

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// lockPages asks the kernel to keep b resident, so the decrypted privilege key
// is not written to swap (D-19). Android counts as unix here, which is what we
// want: a phone's swap file is exactly the place a six-hour key must not land.
func lockPages(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if err := unix.Mlock(b); err != nil {
		return fmt.Errorf("identity: mlock %d bytes: %w", len(b), err)
	}
	return nil
}

func unlockPages(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	return unix.Munlock(b)
}
