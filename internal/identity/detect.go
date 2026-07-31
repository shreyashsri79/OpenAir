package identity

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// DetectTier reports the protection tier of the key material already in dir
// (D-21), so a daemon does not have to be told something the disk already knows.
//
// It reads the sealed container's KDF byte and nothing else: kdf = 1 is
// Argon2id, which is tier 2, and kdf = 0 means the key-encryption key comes from
// a platform keystore, which is tier 1. No privilege key at all is tier 3, a
// device that can pair and transfer but never reach Owned.
//
// Passing a tier that disagrees with the disk to LoadOrCreate is ErrTierMismatch
// rather than a silent downgrade, which is why detecting beats configuring: a
// mistyped flag would otherwise take a device out of Owned with no diagnostic.
func DetectTier(dir string) (ProtectionTier, error) {
	b, err := os.ReadFile(filepath.Join(dir, privilegeKeyFile))
	if errors.Is(err, fs.ErrNotExist) {
		return TierNone, nil
	}
	if err != nil {
		return TierNone, fmt.Errorf("identity: read privilege key: %w", err)
	}
	f, err := parseSealedKeyFile(b)
	if err != nil {
		return TierNone, err
	}
	switch f.kdf {
	case kdfNone:
		return TierKeystore, nil
	case kdfArgon2id:
		return TierPassphrase, nil
	default:
		return TierNone, fmt.Errorf("%w: unknown kdf %d", ErrKeyFileFormat, f.kdf)
	}
}

// HasPrivilegeKey reports whether dir holds a sealed privilege key at all.
func HasPrivilegeKey(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, privilegeKeyFile))
	return err == nil
}
