package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// At-rest container for the privilege key, PROTOCOL.md Appendix A:
//
//	magic        "OAKEY\0"          6 bytes
//	version      u8                 1 = this format
//	kdf          u8                 0 = none (platform keystore), 1 = Argon2id
//	argon_time   u32                iterations       (0 when kdf = 0)
//	argon_memory u32                KiB              (0 when kdf = 0)
//	argon_lanes  u8                 parallelism      (0 when kdf = 0)
//	salt_len     u8
//	salt         salt_len bytes
//	nonce        24 bytes           XChaCha20-Poly1305
//	ct_len       u32
//	ciphertext   ct_len bytes       sealed Ed25519 private key
//
// Integers are little-endian per PROTOCOL.md §0. The header from magic through
// salt is the AEAD's associated data, so KDF parameters cannot be downgraded by
// an attacker who can edit the file.
const (
	keyFileVersion = 1

	kdfNone     = 0 // key-encryption key comes from a platform keystore
	kdfArgon2id = 1

	saltLen  = 16
	nonceLen = chacha20poly1305.NonceSizeX // 24
	kekLen   = chacha20poly1305.KeySize    // 32

	headerFixedLen = 6 + 1 + 1 + 4 + 4 + 1 + 1 // magic..salt_len
)

var keyFileMagic = [6]byte{'O', 'A', 'K', 'E', 'Y', 0}

// Argon2Params are the stored, versioned Argon2id costs. They live in the file
// rather than in a constant so cost can be raised later without stranding
// existing installs (D-19, D-21).
type Argon2Params struct {
	Time   uint32 // iterations
	Memory uint32 // KiB
	Lanes  uint8  // parallelism
}

// DefaultArgon2Params targets roughly a second per attempt on a laptop, which
// is what makes the ~51 bits of a four-word passphrase computationally out of
// reach offline (D-21 tier 2).
var DefaultArgon2Params = Argon2Params{Time: 3, Memory: 64 * 1024, Lanes: 4}

func (p Argon2Params) valid() bool {
	return p.Time > 0 && p.Memory > 0 && p.Lanes > 0
}

// sealedKeyFile is a parsed Appendix A container. The private key stays sealed
// until open is called: parsing gives the KDF parameters, nothing more.
type sealedKeyFile struct {
	kdf    uint8
	params Argon2Params
	salt   []byte
	nonce  []byte
	ct     []byte

	// ad is the header from magic through salt, retained verbatim so that
	// opening authenticates exactly the bytes on disk rather than a
	// re-serialisation of them.
	ad []byte
}

// argonHeader serialises magic..salt, the AEAD associated data.
func argonHeader(kdf uint8, p Argon2Params, salt []byte) []byte {
	b := make([]byte, 0, headerFixedLen+len(salt))
	b = append(b, keyFileMagic[:]...)
	b = append(b, keyFileVersion, kdf)
	b = binary.LittleEndian.AppendUint32(b, p.Time)
	b = binary.LittleEndian.AppendUint32(b, p.Memory)
	b = append(b, p.Lanes, uint8(len(salt)))
	b = append(b, salt...)
	return b
}

// deriveKEK runs Argon2id over the passphrase with the stored parameters.
func deriveKEK(passphrase, salt []byte, p Argon2Params) []byte {
	return argon2.IDKey(passphrase, salt, p.Time, p.Memory, p.Lanes, kekLen)
}

// sealPrivilegeKey produces an Appendix A container holding priv.
//
// With kdf = kdfArgon2id the key-encryption key is derived from passphrase;
// with kdf = kdfNone the caller supplies it from a platform keystore and
// passphrase is ignored.
func sealPrivilegeKey(priv ed25519.PrivateKey, kdf uint8, p Argon2Params, passphrase, keystoreKEK []byte) ([]byte, error) {
	var (
		salt []byte
		kek  []byte
	)
	switch kdf {
	case kdfArgon2id:
		if len(passphrase) == 0 {
			return nil, errors.New("identity: passphrase required to seal the privilege key")
		}
		if !p.valid() {
			return nil, fmt.Errorf("identity: invalid Argon2id parameters %+v", p)
		}
		salt = make([]byte, saltLen)
		if _, err := rand.Read(salt); err != nil {
			return nil, fmt.Errorf("identity: salt: %w", err)
		}
		kek = deriveKEK(passphrase, salt, p)
	case kdfNone:
		if len(keystoreKEK) != kekLen {
			return nil, fmt.Errorf("%w: keystore key-encryption key is %d bytes, want %d",
				ErrNoKeystore, len(keystoreKEK), kekLen)
		}
		p = Argon2Params{} // zero when kdf = 0, per Appendix A
		kek = keystoreKEK
	default:
		return nil, fmt.Errorf("%w: unknown kdf %d", ErrKeyFileFormat, kdf)
	}

	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, fmt.Errorf("identity: aead: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("identity: nonce: %w", err)
	}

	ad := argonHeader(kdf, p, salt)
	ct := aead.Seal(nil, nonce, priv, ad)

	out := make([]byte, 0, len(ad)+nonceLen+4+len(ct))
	out = append(out, ad...)
	out = append(out, nonce...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(ct)))
	out = append(out, ct...)
	return out, nil
}

// parseSealedKeyFile validates the container framing and returns the KDF
// parameters. It does not decrypt.
func parseSealedKeyFile(b []byte) (*sealedKeyFile, error) {
	if len(b) < headerFixedLen {
		return nil, fmt.Errorf("%w: %d bytes is shorter than the header", ErrKeyFileFormat, len(b))
	}
	if string(b[0:6]) != string(keyFileMagic[:]) {
		return nil, fmt.Errorf("%w: bad magic", ErrKeyFileFormat)
	}
	if b[6] != keyFileVersion {
		return nil, fmt.Errorf("%w: version %d, want %d", ErrKeyFileFormat, b[6], keyFileVersion)
	}
	f := &sealedKeyFile{kdf: b[7]}
	f.params = Argon2Params{
		Time:   binary.LittleEndian.Uint32(b[8:12]),
		Memory: binary.LittleEndian.Uint32(b[12:16]),
		Lanes:  b[16],
	}
	sl := int(b[17])
	off := headerFixedLen + sl
	if len(b) < off+nonceLen+4 {
		return nil, fmt.Errorf("%w: truncated before ciphertext", ErrKeyFileFormat)
	}
	f.salt = b[headerFixedLen:off]
	f.ad = b[:off]
	f.nonce = b[off : off+nonceLen]
	off += nonceLen
	ctLen := int(binary.LittleEndian.Uint32(b[off : off+4]))
	off += 4
	if ctLen < 0 || len(b)-off != ctLen {
		return nil, fmt.Errorf("%w: ct_len is %d, %d bytes remain", ErrKeyFileFormat, ctLen, len(b)-off)
	}
	f.ct = b[off:]

	switch f.kdf {
	case kdfArgon2id:
		if !f.params.valid() || sl == 0 {
			return nil, fmt.Errorf("%w: kdf=argon2id with parameters %+v and %d-byte salt",
				ErrKeyFileFormat, f.params, sl)
		}
	case kdfNone:
		if f.params != (Argon2Params{}) {
			return nil, fmt.Errorf("%w: kdf=none must carry zero Argon2id parameters", ErrKeyFileFormat)
		}
	default:
		return nil, fmt.Errorf("%w: unknown kdf %d", ErrKeyFileFormat, f.kdf)
	}
	return f, nil
}

// open decrypts the container with a key-encryption key derived per its own
// stored parameters. Any failure -- wrong passphrase, edited header, corrupt
// ciphertext -- is ErrPassphrase, because the AEAD cannot distinguish them.
func (f *sealedKeyFile) open(passphrase, keystoreKEK []byte) (ed25519.PrivateKey, error) {
	var kek []byte
	switch f.kdf {
	case kdfArgon2id:
		kek = deriveKEK(passphrase, f.salt, f.params)
	case kdfNone:
		if len(keystoreKEK) != kekLen {
			return nil, ErrNoKeystore
		}
		kek = keystoreKEK
	default:
		return nil, fmt.Errorf("%w: unknown kdf %d", ErrKeyFileFormat, f.kdf)
	}
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, fmt.Errorf("identity: aead: %w", err)
	}
	pt, err := aead.Open(nil, f.nonce, f.ct, f.ad)
	if err != nil {
		return nil, ErrPassphrase
	}
	switch len(pt) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(pt), nil
	case ed25519.SeedSize:
		// Tolerated on read: "sealed Ed25519 private key" in Appendix A does not
		// say whether that is the 32-byte seed or the 64-byte expanded form.
		// We write the expanded form; we accept either.
		return ed25519.NewKeyFromSeed(pt), nil
	default:
		return nil, fmt.Errorf("%w: sealed key is %d bytes", ErrKeyFileFormat, len(pt))
	}
}

// matchesPublic reports whether priv's public half is pub, in constant time.
//
// This is the cross-check for storing the privilege public key in a separate
// plaintext file: the container itself has nowhere to put it (see the note in
// identity.go), so the sibling file is unauthenticated and must be proved
// against the sealed key before either is used.
func matchesPublic(priv ed25519.PrivateKey, pub ed25519.PublicKey) bool {
	got, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare(got, pub) == 1
}
