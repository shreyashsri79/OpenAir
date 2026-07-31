package identity

import "crypto/ed25519"

// lockedKey is a decrypted privilege key held in pages the kernel is asked not
// to swap, and zeroed the moment the last unlock session lapses.
//
// D-19 is explicit that "hold decrypted state for six hours" is the entire
// security boundary, so the handling here is not hygiene. Three things it does,
// and one it cannot:
//
//   - The key lives in its own allocation, locked with mlock or VirtualLock, so
//     the kernel does not write it to swap where it would outlive the timer.
//   - It is zeroed on expiry, manual lock and shutdown.
//   - Core dumps are disabled separately, by DisableCoreDumps, which the daemon
//     calls at startup.
//
// What it cannot do: guarantee that no copy was left behind on the way here.
// Unsealing runs the ciphertext through an AEAD that allocates its own output
// on the Go heap, and the runtime may have copied that buffer while the
// collector moved work around. Those copies are unreachable and will be reused,
// but they are not zeroed, and claiming otherwise would be false. What is
// bounded is the long-lived copy: exactly one, locked, and wiped on expiry.
type lockedKey struct {
	key ed25519.PrivateKey

	// locked reports whether the pages were actually pinned. mlock is subject
	// to RLIMIT_MEMLOCK, which is small by default on some systems, so a
	// refusal is a real possibility rather than a theoretical one. See
	// FileIdentity.Swappable.
	locked bool
}

// newLockedKey copies priv into a locked allocation. It returns the key even
// when locking failed, together with the error: an unlock that refuses to
// proceed because the pages could not be pinned would take Owned access away
// from the user entirely, which is the worse outcome as long as the degradation
// is visible.
func newLockedKey(priv ed25519.PrivateKey) (*lockedKey, error) {
	buf := make([]byte, len(priv))
	copy(buf, priv)
	err := lockPages(buf)
	return &lockedKey{key: ed25519.PrivateKey(buf), locked: err == nil}, err
}

// wipe zeroes the key and releases the lock on its pages.
func (k *lockedKey) wipe() {
	if k == nil || k.key == nil {
		return
	}
	zero(k.key)
	if k.locked {
		_ = unlockPages(k.key)
		k.locked = false
	}
	k.key = nil
}
