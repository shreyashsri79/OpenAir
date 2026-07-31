package mobile

import (
	"fmt"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
)

// Owned access, as an Android shell sees it (M6).
//
// The split of responsibility is the whole design here. Go holds the sealed
// privilege key, the unlock session and the six-hour timer; it cannot run a
// biometric prompt and must never be able to obtain a credential without one.
// Kotlin holds the Android Keystore and BiometricPrompt; it cannot decrypt the
// privilege key and never sees it. The only thing crossing between them is a
// 32-byte key-encryption key that the Keystore releases exclusively after the
// user has authenticated (D-19, D-21 tier 1).
//
// That is why Protect and Unlock take raw bytes rather than a callback: a
// callback would let this side ask for the credential whenever it liked, and
// the user-presence requirement would become a convention rather than a rule
// the platform enforces.

// TierNone, TierPassphrase and TierKeystore are D-21's protection tiers, as
// integers because gomobile binds no Go enums.
const (
	TierNone       = 0
	TierPassphrase = 1
	TierKeystore   = 2
)

// ProtectionTier reports how this device protects its privilege key.
//
// TierNone means it holds none: the device pairs, transfers and syncs the
// clipboard, and can never initiate Owned-level work. A shell must say so
// plainly rather than offering an unlock button that cannot work (D-21).
func (i *Identity) ProtectionTier() int {
	switch i.impl.ProtectionTier() {
	case identity.TierKeystore:
		return TierKeystore
	case identity.TierPassphrase:
		return TierPassphrase
	default:
		return TierNone
	}
}

// HasPrivilegeKey reports whether this device has been protected yet.
func (i *Identity) HasPrivilegeKey() bool { return identity.HasPrivilegeKey(i.dir) }

// Protect creates this device's privilege key, sealed under a key-encryption
// key the platform keystore holds (D-21 tier 1).
//
// kek must be 32 bytes and must come from a Keystore entry that requires user
// authentication. Call it once; a device that already has a privilege key gets
// an error rather than a silently replaced key, because replacing it would
// invalidate every pairing that pinned the old one.
//
// The returned Identity replaces the receiver's key state, so a shell should
// reload its own references afterwards -- or simply restart the receiver, which
// is what the service does.
func (i *Identity) Protect(kek []byte) error {
	if i.HasPrivilegeKey() {
		return fmt.Errorf("mobile: this device already has a privilege key")
	}
	if len(kek) != keystoreKEKLen {
		return fmt.Errorf("mobile: key-encryption key is %d bytes, want %d", len(kek), keystoreKEKLen)
	}
	impl, err := identity.LoadOrCreate(identity.Options{
		Dir:         i.dir,
		Tier:        identity.TierKeystore,
		KeystoreKEK: func() ([]byte, error) { return kek, nil },
	})
	if err != nil {
		return err
	}
	i.impl = impl
	return nil
}

// ProtectWithPassphrase is the tier 2 path, for a device with no usable
// keystore. It exists because refusing to protect at all would leave such a
// device on tier 3, which is strictly worse: a passphrase that resists offline
// attack is better than no privilege key (D-21).
func (i *Identity) ProtectWithPassphrase(passphrase string) error {
	if i.HasPrivilegeKey() {
		return fmt.Errorf("mobile: this device already has a privilege key")
	}
	impl, err := identity.LoadOrCreate(identity.Options{
		Dir:        i.dir,
		Tier:       identity.TierPassphrase,
		Passphrase: []byte(passphrase),
	})
	if err != nil {
		return err
	}
	i.impl = impl
	return nil
}

// keystoreKEKLen is XChaCha20-Poly1305's key size, which is what Appendix A's
// kdf = 0 container is sealed under.
const keystoreKEKLen = 32

// Unlock starts an Owned session for one peer (D-18, D-30).
//
// Scope is per device on purpose: the prompt the shell shows can then name what
// it grants -- "unlock to reach desktop-home" rather than "unlock OpenAir" --
// and a prompt that names nothing trains people to approve reflexively.
//
// Returns the expiry as unix milliseconds, or 0 under the never-expire policy.
// Pass the credential that matches this device's tier: the keystore KEK at tier
// 1, the passphrase at tier 2.
func (i *Identity) Unlock(deviceID string, kek []byte, passphrase string, neverExpire bool, lifetimeMillis int64) (int64, error) {
	target := identity.DeviceID(deviceID)
	if _, ok := i.store.Get(target); !ok {
		return 0, fmt.Errorf("mobile: %s is not paired with this device; unlock authorises one paired device", deviceID)
	}
	policy := identity.PolicyTimed
	if neverExpire {
		policy = identity.PolicyNever
	}
	expiry, err := i.impl.Unlock(target, identity.UnlockOptions{
		KeystoreKEK: kek,
		Passphrase:  []byte(passphrase),
		Policy:      policy,
		Lifetime:    time.Duration(lifetimeMillis) * time.Millisecond,
	})
	if err != nil {
		return 0, err
	}
	if expiry.IsZero() {
		return 0, nil
	}
	return expiry.UnixMilli(), nil
}

// Lock ends every unlock session and wipes the decrypted key. A shell should
// call it when the user asks and when the device is about to be handed over --
// there is no automatic hook for the latter, because Android does not tell an
// app that its owner just gave the phone to somebody.
func (i *Identity) Lock() { i.impl.Lock() }

// LockPeer ends one peer's session.
func (i *Identity) LockPeer(deviceID string) { i.impl.LockPeer(identity.DeviceID(deviceID)) }

// UnlockedUntil reports when a peer's session expires: 0 when nothing is
// unlocked, -1 under the never-expire policy, and a unix millisecond timestamp
// otherwise.
func (i *Identity) UnlockedUntil(deviceID string) int64 {
	expiry, ok := i.impl.UnlockedUntil(identity.DeviceID(deviceID))
	switch {
	case !ok:
		return 0
	case expiry.IsZero():
		return -1
	default:
		return expiry.UnixMilli()
	}
}

// KeySwappable reports that the decrypted privilege key sits in pages the
// kernel may write to swap, because locking them was refused. False in the
// normal case; a shell that shows it is telling the truth about a real
// difference in exposure.
func (i *Identity) KeySwappable() bool { return i.impl.Swappable() }

// TrustLevel reports what a paired peer is allowed to do here: 0 unpaired,
// 1 trusted, 2 owned.
func (i *Identity) TrustLevel(deviceID string) int {
	peer, ok := i.store.Get(identity.DeviceID(deviceID))
	if !ok {
		return int(identity.LevelUnpaired)
	}
	return int(peer.Level)
}

// SetOwned grants or withdraws a paired device's unattended access (§6.4).
//
// It is a local act on this device and never a response to a peer's request, so
// there is no wire message that reaches it. Promotion is refused for a peer with
// no pinned privilege key, which is what a device paired before either side ran
// Protect looks like: those pairings have to be made again.
func (i *Identity) SetOwned(deviceID string, owned bool) error {
	peer, ok := i.store.Get(identity.DeviceID(deviceID))
	if !ok {
		return fmt.Errorf("mobile: %s is not paired with this device", deviceID)
	}
	if owned {
		if len(peer.PrivilegePublicKey) == 0 {
			return fmt.Errorf("mobile: %s has no privilege key pinned here; pair the two devices again, then grant owned access",
				FormatFingerprint(deviceID))
		}
		if peer.ProtectionTier == identity.TierNone {
			return fmt.Errorf("mobile: %s protects no privilege key, so it cannot hold owned access",
				FormatFingerprint(deviceID))
		}
		peer.Level = identity.LevelOwned
	} else {
		peer.Level = identity.LevelTrusted
		i.impl.LockPeer(peer.DeviceID)
	}
	return i.store.Put(peer)
}
