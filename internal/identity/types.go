package identity

import (
	"crypto/ed25519"
	"crypto/tls"
)

// DeviceID is base32(SHA-256(identity_public_key)[0:10]), 16 lowercase
// characters. PROTOCOL.md §2.
type DeviceID string

// TrustLevel mirrors the wire enum. Deliberately re-declared rather than
// imported from the generated package, so callers cannot accidentally pass a
// wire value where a domain value belongs -- the two differ by one (D-34).
type TrustLevel int

const (
	LevelUnpaired TrustLevel = iota
	LevelTrusted
	LevelOwned
)

// ProtectionTier is how this device protects its privilege key (D-21).
// A peer at TierNone must never be granted LevelOwned.
type ProtectionTier int

const (
	TierNone ProtectionTier = iota
	TierPassphrase
	TierKeystore
)

// Identity is this device's own keys.
//
// The identity key is always available: it terminates TLS and keeps the device
// reachable. The privilege key is sealed at rest and only usable while an
// unlock session is live, which is why it is reached through a method that can
// fail rather than a field (D-19, D-20).
type Identity interface {
	DeviceID() DeviceID
	IdentityPublic() ed25519.PublicKey
	PrivilegePublic() ed25519.PublicKey

	// TLSConfig returns a config presenting this device's identity key and
	// verifying the peer against pinned, per PROTOCOL.md §1 and §2. A nil
	// pinned key means pairing mode, where no key is pinned yet (§5).
	TLSConfig(pinned ed25519.PublicKey) (*tls.Config, error)

	// SignOwned produces the AuthProof signature for an Owned-level request
	// (PROTOCOL.md §6). Returns ErrLocked when no unlock session is live for
	// this peer -- scope is per peer (D-30).
	SignOwned(target DeviceID, capID byte, msgType uint16) (nonce []byte, issuedAt int64, sig []byte, err error)
}

// Peer is one record in the trust store. Fields are enumerated in D-30.
type Peer struct {
	DeviceID            DeviceID
	IdentityPublicKey   ed25519.PublicKey
	PrivilegePublicKey  ed25519.PublicKey
	DisplayName         string
	Platform            string
	Level               TrustLevel
	GrantedCapabilities []byte // capability IDs, wire values
	AuthPolicy          string // "timed" | "never"; "never" also means always-on (D-20)
	TokenGrantedAt      int64
	ProtectionTier      ProtectionTier
	CreatedAt           int64
	LastSeen            int64
}

// TrustStore persists paired peers. Implementations must be safe for
// concurrent use: the daemon reads it on every inbound connection.
type TrustStore interface {
	Get(DeviceID) (Peer, bool)
	List() []Peer
	Put(Peer) error
	Delete(DeviceID) error
}
