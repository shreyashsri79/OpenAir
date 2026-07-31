package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

// DeviceIDLen is the character length of a DeviceID: 10 bytes of digest encoded
// base32 without padding is exactly 16 characters.
const DeviceIDLen = 16

// deviceIDEncoding is RFC 4648 base32, no padding (PROTOCOL.md §0). The
// lowercasing that §2 requires is applied separately, since encoding/base32 has
// no lowercase alphabet.
var deviceIDEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// DeriveDeviceID computes base32(SHA-256(identity_public_key)[0:10]),
// RFC 4648 lowercase without padding, 16 characters (PROTOCOL.md §2).
//
// A nil or wrong-length key still hashes to something; callers that care about
// well-formedness check the key length before calling.
func DeriveDeviceID(pub ed25519.PublicKey) DeviceID {
	sum := sha256.Sum256(pub)
	return DeviceID(strings.ToLower(deviceIDEncoding.EncodeToString(sum[:10])))
}

// Valid reports whether d is syntactically a DeviceID: 16 characters drawn from
// the lowercase RFC 4648 base32 alphabet. It says nothing about whether any
// device holds the key that produces it.
func (d DeviceID) Valid() bool {
	if len(d) != DeviceIDLen {
		return false
	}
	for i := 0; i < len(d); i++ {
		c := d[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '2' && c <= '7':
		default:
			return false
		}
	}
	return true
}

func (d DeviceID) String() string { return string(d) }
