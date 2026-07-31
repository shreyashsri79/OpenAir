package identity

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// lockPages pins b with VirtualLock, Windows' equivalent of mlock (D-19).
//
// VirtualLock is bounded by the process working-set minimum, so it fails more
// readily than mlock does; the caller treats that as a degradation to report
// rather than a reason to refuse the unlock.
func lockPages(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if err := windows.VirtualLock(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b))); err != nil {
		return fmt.Errorf("identity: VirtualLock %d bytes: %w", len(b), err)
	}
	return nil
}

func unlockPages(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	return windows.VirtualUnlock(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
}
