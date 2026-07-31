package mobile

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/shreyashsri79/openair/internal/identity"
)

// Identity is this device's key material, loaded from or created in a
// directory. On Android that directory is app-private storage — Context.filesDir
// or a subdirectory of it — which is where the identity key belongs until the
// keystore tier lands (D-21).
type Identity struct {
	impl  *identity.FileIdentity
	store *identity.FileTrustStore
	dir   string
}

// LoadIdentity opens this device's keys in dir, generating them if the
// directory is empty. It is safe to call on every app start; it is not safe to
// call concurrently on the same directory.
//
// The protection tier is read from the directory rather than chosen here
// (D-21): a device that has run Protect holds a sealed privilege key whose
// container says which tier it belongs to, and one that has not is tier 3,
// which pairs and transfers but never reaches Owned. Passing a tier that
// disagrees with the disk is an error rather than a silent downgrade, so
// detecting is the only correct thing to do on a path that runs at every start.
func LoadIdentity(dir string) (*Identity, error) {
	tier, err := identity.DetectTier(dir)
	if err != nil {
		return nil, err
	}
	impl, err := identity.LoadOrCreate(identity.Options{Dir: dir, Tier: tier})
	if err != nil {
		return nil, err
	}
	// The trust store lives beside the keys, as it does for the CLI: they are
	// the same secret in two halves -- the keys this device holds, and the keys
	// it has decided to believe.
	store, err := identity.OpenTrustStore(filepath.Join(dir, trustStoreName))
	if err != nil {
		return nil, err
	}
	return &Identity{impl: impl, store: store, dir: dir}, nil
}

// trustStoreName matches the CLI's, so a device's keys and its pinned peers can
// be moved together and read by either.
const trustStoreName = "trust.json"

// PairedCount reports how many peers this device has paired with. A shell uses
// it to decide whether to show a device list or a "pair your first device"
// prompt.
func (i *Identity) PairedCount() int {
	n := 0
	for _, p := range i.store.List() {
		if p.Level > identity.LevelUnpaired {
			n++
		}
	}
	return n
}

// IsPaired reports whether deviceID is paired with this device.
func (i *Identity) IsPaired(deviceID string) bool {
	p, ok := i.store.Get(identity.DeviceID(deviceID))
	return ok && p.Level > identity.LevelUnpaired
}

// Unpair discards a peer's pinned keys locally.
//
// It does not tell the peer -- that is Pairing.Revoke, which needs a live
// session to send on. Unpairing without notifying is still the right thing when
// the device is gone or was lost: PROTOCOL.md §6.3 makes enforcement local, so
// what matters is that this device stops believing the key.
func (i *Identity) Unpair(deviceID string) error {
	err := i.store.Delete(identity.DeviceID(deviceID))
	if errors.Is(err, identity.ErrUnknownPeer) {
		return nil
	}
	return err
}

// DeviceID returns this device's identifier: base32 of a truncated hash of the
// identity public key, 16 lowercase characters (PROTOCOL.md §2).
func (i *Identity) DeviceID() string { return string(i.impl.DeviceID()) }

// Fingerprint returns DeviceID grouped for a human to read aloud or compare
// against another screen.
func (i *Identity) Fingerprint() string { return FormatFingerprint(i.DeviceID()) }

// FormatFingerprint groups a device ID into dash-separated blocks of four.
//
// The device ID is already a hash truncation, so this adds no information — it
// only makes a 16-character string checkable character by character without
// losing your place, which matters when the comparison is the whole security
// model (M1) and a human is doing it.
func FormatFingerprint(deviceID string) string {
	var b strings.Builder
	for i := 0; i < len(deviceID); i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(deviceID[i:min(i+4, len(deviceID))])
	}
	return b.String()
}
