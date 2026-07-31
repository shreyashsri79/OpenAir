package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// On-disk layout under the identity directory.
//
//	identity.key   PKCS#8 DER, 0600. Never sealed: D-20 makes this key warm at
//	               all times so the device stays reachable with nobody present.
//	privilege.key  Appendix A container, 0600. Sealed at rest (D-19).
//	privilege.pub  32 raw bytes, 0600.
//
// privilege.pub exists because Appendix A has no field for the public key, and
// a device needs its own privilege public key while the private half is sealed
// -- pairing sends it (§5.2) and the UI shows it. It is unauthenticated on its
// own, so anything that unseals the container MUST check the two agree
// (matchesPublic). This is worth a spec fix rather than a permanent workaround;
// see the report accompanying M1b.
const (
	identityKeyFile  = "identity.key"
	privilegeKeyFile = "privilege.key"
	privilegePubFile = "privilege.pub"

	keyFileMode = fs.FileMode(0o600)
	keyDirMode  = fs.FileMode(0o700)
)

// Options configures LoadOrCreate.
type Options struct {
	// Dir is the directory holding this device's key material. Created with
	// mode 0700 if absent.
	Dir string

	// Tier is the protection tier this device can actually offer (D-21).
	// TierNone means no privilege key is generated at all and the device can
	// never reach Owned.
	Tier ProtectionTier

	// Passphrase seals the privilege key at TierPassphrase. It is consulted
	// only when the key must be created: loading does not unseal, because a
	// sealed key at rest is the entire point of D-19.
	Passphrase []byte

	// Argon2 overrides the sealing cost at TierPassphrase. Zero means
	// DefaultArgon2Params. Stored in the file, so raising it later does not
	// strand existing installs.
	Argon2 Argon2Params

	// KeystoreKEK supplies the 32-byte key-encryption key for TierKeystore,
	// which Appendix A models as kdf = 0. It is called only when the privilege
	// key must be created; unlocking asks the caller for the KEK again through
	// UnlockOptions, because that path must run behind a user-presence
	// challenge and this one runs at first start. Nil at TierKeystore is
	// ErrNoKeystore rather than a silent downgrade to a weaker tier.
	KeystoreKEK func() ([]byte, error)

	// OnExpiryWarning, if set, fires DefaultExpiryWarning before an unlock
	// session lapses, so a long transfer can be extended deliberately rather
	// than discovered broken (PROTOCOL.md §6.5). It runs on its own goroutine.
	OnExpiryWarning func(target DeviceID, expires time.Time)

	// WarnBefore overrides DefaultExpiryWarning. Zero means the default; a
	// negative value disables the warning. Tests use it to keep lifetimes short.
	WarnBefore time.Duration

	// Now overrides the clock the unlock session uses, for tests that need to
	// cross a six-hour boundary without waiting six hours. Nil means time.Now.
	// It does not affect the sweeper, which runs on real time because what it
	// governs is how long a key stays in memory.
	Now func() time.Time

	// Logf, if set, receives operational notes that are neither errors returned
	// to the caller nor silent -- notably a failure to lock the key's pages
	// into RAM.
	Logf func(format string, args ...any)
}

// FileIdentity is an Identity backed by key files in a directory.
//
// The identity private key is held in memory for the process lifetime: D-20
// requires it warm. The privilege private key is held only while an unlock
// session is live, in pages locked out of swap, and wiped the moment the last
// session lapses -- see unlock.go, which is where the six-hour token of D-18
// becomes a concrete key lifetime rather than a policy flag.
type FileIdentity struct {
	dir string

	identityPriv ed25519.PrivateKey
	identityPub  ed25519.PublicKey
	deviceID     DeviceID
	cert         *tls.Certificate

	tier          ProtectionTier
	privilegePub  ed25519.PublicKey // nil at TierNone
	sealedPrivKey []byte            // Appendix A container, nil at TierNone

	// unlock holds the decrypted privilege key while a session is live, and the
	// per-peer grants that justify holding it (D-18, D-19, D-30). See unlock.go.
	unlock          unlockState
	clock           func() time.Time
	logf            func(string, ...any)
	warnBefore      time.Duration
	onExpiryWarning func(DeviceID, time.Time)
}

var _ Identity = (*FileIdentity)(nil)

// LoadOrCreate loads this device's keys from opts.Dir, generating whatever is
// missing. It is the only constructor: a device that cannot persist its keys
// would be re-paired on every restart, which D-7's pinning makes fatal rather
// than annoying.
func LoadOrCreate(opts Options) (*FileIdentity, error) {
	if opts.Dir == "" {
		return nil, errors.New("identity: Options.Dir is required")
	}
	if err := os.MkdirAll(opts.Dir, keyDirMode); err != nil {
		return nil, fmt.Errorf("identity: create key directory: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone, and the identity key
	// inside is unsealed by design (D-20). Tighten it rather than trusting
	// whatever created it -- a 0755 key directory is a quiet local exposure.
	if err := os.Chmod(opts.Dir, keyDirMode); err != nil {
		return nil, fmt.Errorf("identity: tighten key directory permissions: %w", err)
	}

	priv, err := loadOrCreateIdentityKey(filepath.Join(opts.Dir, identityKeyFile))
	if err != nil {
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)

	cert, err := SelfSignedCertificate(priv)
	if err != nil {
		return nil, err
	}

	id := &FileIdentity{
		dir:             opts.Dir,
		identityPriv:    priv,
		identityPub:     pub,
		deviceID:        DeriveDeviceID(pub),
		cert:            cert,
		tier:            opts.Tier,
		clock:           opts.Now,
		logf:            opts.Logf,
		warnBefore:      opts.WarnBefore,
		onExpiryWarning: opts.OnExpiryWarning,
	}
	if id.warnBefore == 0 {
		id.warnBefore = DefaultExpiryWarning
	}
	id.unlock.grants = make(map[DeviceID]*grant)
	if err := id.loadOrCreatePrivilegeKey(opts); err != nil {
		return nil, err
	}
	return id, nil
}

func loadOrCreateIdentityKey(path string) (ed25519.PrivateKey, error) {
	der, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, err := x509.ParsePKCS8PrivateKey(der)
		if err != nil {
			return nil, fmt.Errorf("identity: parse %s: %w", path, err)
		}
		priv, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("identity: %s holds a %T, want an Ed25519 key", path, key)
		}
		return priv, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("identity: read %s: %w", path, err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("identity: generate identity key: %w", err)
	}
	der, err = x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("identity: marshal identity key: %w", err)
	}
	if err := writeFileAtomic(path, der, keyFileMode); err != nil {
		return nil, err
	}
	return priv, nil
}

func (i *FileIdentity) loadOrCreatePrivilegeKey(opts Options) error {
	keyPath := filepath.Join(i.dir, privilegeKeyFile)
	pubPath := filepath.Join(i.dir, privilegePubFile)

	sealed, err := os.ReadFile(keyPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("identity: read %s: %w", keyPath, err)
	}
	exists := err == nil

	if opts.Tier == TierNone {
		if exists {
			// Refusing beats honouring: a device that quietly forgets it has a
			// privilege key drops out of Owned with no diagnostic (D-21).
			return fmt.Errorf("%w: %s exists but tier none was requested", ErrTierMismatch, keyPath)
		}
		return nil
	}

	if exists {
		f, err := parseSealedKeyFile(sealed)
		if err != nil {
			return fmt.Errorf("identity: %s: %w", keyPath, err)
		}
		wantKDF := kdfForTier(opts.Tier)
		if f.kdf != wantKDF {
			return fmt.Errorf("%w: %s was sealed with kdf %d, tier %d expects kdf %d",
				ErrTierMismatch, keyPath, f.kdf, opts.Tier, wantKDF)
		}
		pub, err := os.ReadFile(pubPath)
		if err != nil {
			return fmt.Errorf("identity: read %s: %w", pubPath, err)
		}
		if len(pub) != ed25519.PublicKeySize {
			return fmt.Errorf("identity: %s is %d bytes, want %d", pubPath, len(pub), ed25519.PublicKeySize)
		}
		i.privilegePub = ed25519.PublicKey(pub)
		i.sealedPrivKey = sealed
		return nil
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("identity: generate privilege key: %w", err)
	}
	params := opts.Argon2
	if params == (Argon2Params{}) {
		params = DefaultArgon2Params
	}
	var keystoreKEK []byte
	if opts.Tier == TierKeystore {
		if opts.KeystoreKEK == nil {
			return ErrNoKeystore
		}
		if keystoreKEK, err = opts.KeystoreKEK(); err != nil {
			return fmt.Errorf("%w: %v", ErrNoKeystore, err)
		}
	}
	sealed, err = sealPrivilegeKey(priv, kdfForTier(opts.Tier), params, opts.Passphrase, keystoreKEK)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(keyPath, sealed, keyFileMode); err != nil {
		return err
	}
	if err := writeFileAtomic(pubPath, pub, keyFileMode); err != nil {
		return err
	}
	i.privilegePub = pub
	i.sealedPrivKey = sealed
	return nil
}

func kdfForTier(t ProtectionTier) uint8 {
	if t == TierKeystore {
		return kdfNone
	}
	return kdfArgon2id
}

// DeviceID returns base32(SHA-256(identity_public_key)[0:10]) (PROTOCOL.md §2).
func (i *FileIdentity) DeviceID() DeviceID { return i.deviceID }

// IdentityPublic returns the always-warm key peers pin (D-7, D-20).
func (i *FileIdentity) IdentityPublic() ed25519.PublicKey { return i.identityPub }

// PrivilegePublic returns the public half of the sealed privilege key, or nil
// at protection tier none (D-21 tier 3). It is available while the private half
// is sealed, because pairing has to send it (§5.2).
func (i *FileIdentity) PrivilegePublic() ed25519.PublicKey { return i.privilegePub }

// ProtectionTier reports how this device protects its privilege key (D-21).
// A peer at TierNone must never be granted LevelOwned.
func (i *FileIdentity) ProtectionTier() ProtectionTier { return i.tier }

// TLSConfig returns a config presenting this device's identity key and
// verifying the peer against pinned (PROTOCOL.md §1, §2). A nil pinned key is
// pairing mode; the peer key is then readable from the completed handshake with
// PeerPublicKey, or via TLSConfigPairing for callers that never see the
// connection state.
//
// The returned config is fresh on every call and safe to mutate.
func (i *FileIdentity) TLSConfig(pinned ed25519.PublicKey) (*tls.Config, error) {
	if pinned != nil && len(pinned) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("identity: pinned key is %d bytes, want %d", len(pinned), ed25519.PublicKeySize)
	}
	return pinnedTLSConfig(i.cert, pinned, nil), nil
}

// TLSConfigPairing returns a pairing-mode config together with the observer
// that will hold the peer's public key once the handshake reaches its
// certificate (§5). Use one config and one observer per connection.
func (i *FileIdentity) TLSConfigPairing() (*tls.Config, *ObservedKey, error) {
	observed := &ObservedKey{}
	return pinnedTLSConfig(i.cert, nil, observed), observed, nil
}

// Certificate returns the self-signed certificate whose key is the identity key.
func (i *FileIdentity) Certificate() *tls.Certificate { return i.cert }

// SignOwned lives in unlock.go: it needs the unlock session, which is the
// thing that makes a signature possible at all (D-18, D-19, D-30).

// openPrivilege unseals the privilege key. It is deliberately unexported: the
// unlock session in unlock.go is the only caller that may hold the result, and
// handing the raw key to anyone else would put a copy outside the locked pages
// that exist to bound it. Tests use it to prove the container round-trips.
func (i *FileIdentity) openPrivilege(passphrase, keystoreKEK []byte) (ed25519.PrivateKey, error) {
	if i.sealedPrivKey == nil {
		return nil, ErrNoPrivilegeKey
	}
	f, err := parseSealedKeyFile(i.sealedPrivKey)
	if err != nil {
		return nil, err
	}
	priv, err := f.open(passphrase, keystoreKEK)
	if err != nil {
		return nil, err
	}
	if !matchesPublic(priv, i.privilegePub) {
		return nil, fmt.Errorf("%w: sealed privilege key does not match %s",
			ErrKeyFileFormat, privilegePubFile)
	}
	return priv, nil
}

// ownedContext is the domain separator from PROTOCOL.md §6.
const ownedContext = "openair-owned-v1"

// OwnedNonceLen is the AuthProof nonce length: 32 random bytes, fresh per
// request (PROTOCOL.md §6).
const OwnedNonceLen = 32

// OwnedSigningInput builds the byte string an Owned-level AuthProof signs
// (PROTOCOL.md §6):
//
//	"openair-owned-v1" || target_device_id || capID || msgType || nonce || issued_at
//
// The spec gives no encodings for the integers, so §0 applies: little-endian,
// msgType as u16 and issued_at as i64. Every field is fixed-width -- 16-byte
// DeviceID, 32-byte nonce -- so no delimiters are needed and none are added.
// Both the signer (M6) and the verifying session layer must use this function
// rather than re-deriving the layout.
func OwnedSigningInput(target DeviceID, capID byte, msgType uint16, nonce []byte, issuedAt int64) ([]byte, error) {
	if !target.Valid() {
		return nil, fmt.Errorf("identity: target %q is not a DeviceID", target)
	}
	if len(nonce) != OwnedNonceLen {
		return nil, fmt.Errorf("identity: nonce is %d bytes, want %d", len(nonce), OwnedNonceLen)
	}
	b := make([]byte, 0, len(ownedContext)+DeviceIDLen+1+2+OwnedNonceLen+8)
	b = append(b, ownedContext...)
	b = append(b, target...)
	b = append(b, capID)
	b = binary.LittleEndian.AppendUint16(b, msgType)
	b = append(b, nonce...)
	b = binary.LittleEndian.AppendUint64(b, uint64(issuedAt))
	return b, nil
}

// writeFileAtomic writes via a temporary file in the same directory and renames
// over the target, so a crash mid-write leaves the previous key file intact
// rather than a truncated one. A truncated identity key is unrecoverable: every
// peer has pinned it.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("identity: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("identity: chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("identity: write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("identity: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("identity: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("identity: rename %s to %s: %w", tmpName, path, err)
	}
	return nil
}
