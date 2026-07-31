package identity

import (
	"crypto/ed25519"
	"errors"
	"fmt"
)

// Sentinel errors for the identity layer. Callers classify with errors.Is.
var (
	// ErrKeyMismatch reports that a peer presented a TLS certificate whose raw
	// public key is not the pinned one. PROTOCOL.md §2: a mismatch MUST fail the
	// connection and MUST surface as a re-pair prompt, never as a retryable
	// error. Nothing in the stack may back off and dial again on this.
	ErrKeyMismatch = errors.New("identity: peer key does not match the pinned key, re-pair required")

	// ErrNoPeerCertificate reports a handshake where the peer sent no
	// certificate at all. Treated the same as a mismatch by callers, but kept
	// distinct so logs can tell a broken peer from a rotated key.
	ErrNoPeerCertificate = errors.New("identity: peer presented no certificate")

	// ErrALPNMismatch reports that the connection did not negotiate the OpenAir
	// ALPN. PROTOCOL.md §1: a peer offering no matching ALPN MUST be rejected.
	ErrALPNMismatch = errors.New("identity: peer did not negotiate ALPN " + ALPN)

	// ErrTLSVersion reports a handshake that settled below TLS 1.3.
	// PROTOCOL.md §1: earlier versions MUST be refused.
	ErrTLSVersion = errors.New("identity: TLS 1.3 required")

	// ErrLocked reports that the privilege key is sealed and no unlock session
	// is live for the target peer (D-19, D-20, D-30). M1 always returns this
	// from SignOwned; the unlock session lands in M6.
	ErrLocked = errors.New("identity: privilege key is locked, unlock required")

	// ErrNoPrivilegeKey reports a device at protection tier "none" (D-21 tier
	// 3), which holds no privilege key and can therefore never reach Owned.
	ErrNoPrivilegeKey = errors.New("identity: device holds no privilege key (protection tier none)")

	// ErrPassphrase reports that the supplied passphrase did not open the sealed
	// privilege key, or that the file was tampered with -- the AEAD cannot tell
	// those apart, and deliberately so.
	ErrPassphrase = errors.New("identity: passphrase does not open the sealed privilege key")

	// ErrNoKeystore reports that protection tier "keystore" was requested but no
	// platform keystore binding was supplied. M1 ships none; see Options.
	ErrNoKeystore = errors.New("identity: no platform keystore binding available")

	// ErrKeyFileFormat reports a malformed at-rest key container (Appendix A).
	ErrKeyFileFormat = errors.New("identity: malformed key file")

	// ErrTierMismatch reports that on-disk key material disagrees with the
	// requested protection tier. Refusing is deliberate: silently honouring the
	// request would downgrade a device out of Owned without telling anyone.
	ErrTierMismatch = errors.New("identity: on-disk key material disagrees with requested protection tier")
)

// KeyMismatchError carries which key was expected and which arrived. It wraps
// ErrKeyMismatch, so errors.Is(err, ErrKeyMismatch) is the classification test
// and this type is only needed when the two DeviceIDs are worth showing.
type KeyMismatchError struct {
	Pinned ed25519.PublicKey
	Got    ed25519.PublicKey
}

func (e *KeyMismatchError) Error() string {
	return fmt.Sprintf("identity: peer key mismatch: pinned %s, got %s, re-pair required",
		DeriveDeviceID(e.Pinned), DeriveDeviceID(e.Got))
}

func (e *KeyMismatchError) Unwrap() error { return ErrKeyMismatch }

// Retryable reports false, always. It exists so that generic retry logic can
// ask an error whether to back off without special-casing this package: a
// pinned key that changed will not change back, and dialling again only burns
// the user's battery while hiding a re-pair prompt.
func (e *KeyMismatchError) Retryable() bool { return false }
