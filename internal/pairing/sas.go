package pairing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
)

// sasContext is the domain separator of PROTOCOL.md §5.2. It is part of the
// hashed transcript, so a digest computed for pairing can never collide with
// one computed for §6's "openair-owned-v1" signing input.
const sasContext = "openair-pair-v1"

const (
	// NonceLen is the pairing nonce: 32 random bytes per side (§5.2).
	NonceLen = 32

	// SASDigits is the length of the short authentication string the two users
	// compare (§5.2).
	SASDigits = 6

	sasModulus = 1_000_000
)

// Party is one device's contribution to the pairing transcript.
//
// PrivilegeKey is nil for a device at protection tier none (D-21 tier 3), which
// holds no privilege key at all. §5.2 assumes both sides have one and gives no
// encoding for its absence; Transcript substitutes 32 zero bytes so the
// transcript stays fixed-width and both sides agree. See the report note.
type Party struct {
	IdentityKey  ed25519.PublicKey
	PrivilegeKey ed25519.PublicKey
	Nonce        []byte
}

// Transcript builds the byte string both devices hash to derive the SAS
// (PROTOCOL.md §5.2):
//
//	"openair-pair-v1"
//	  || min(idA, idB)     || max(idA, idB)
//	  || min(privA, privB) || max(privA, privB)
//	  || nonce(offerer)    || nonce(scanner)
//
// Sorting the two key pairs by value is what makes the transcript
// role-independent: neither side has to agree with the other about who is
// "first", so both derive the same bytes from the same facts. Every field is
// fixed-width -- 32-byte keys, 32-byte nonces -- so no delimiters are needed
// and none are added.
//
// The nonces are the one part §5.2 orders by role rather than by value: offerer
// (the device that displayed the QR or short code, "A" in §5.1) first. The
// spec's gloss says "initiator's nonce first", which is ambiguous because the
// offerer initiates the pairing while the scanner initiates the connection;
// this follows the literal `nonceA || nonceB` with A as §5.1 defines it. Both
// devices know which role they played, so this costs nothing -- but see the
// report: ordering the nonces by their owner's identity key would remove the
// last role dependence and the ambiguity with it.
func Transcript(offerer, scanner Party) ([]byte, error) {
	idA, err := checkKey("offerer identity", offerer.IdentityKey, false)
	if err != nil {
		return nil, err
	}
	idB, err := checkKey("scanner identity", scanner.IdentityKey, false)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(idA, idB) {
		// Both sides sorting to the same value would make the transcript
		// degenerate, and a device pairing with itself is a bug in the caller
		// rather than a state to derive a SAS for.
		return nil, fmt.Errorf("pairing: both parties present the same identity key")
	}
	privA, err := checkKey("offerer privilege", offerer.PrivilegeKey, true)
	if err != nil {
		return nil, err
	}
	privB, err := checkKey("scanner privilege", scanner.PrivilegeKey, true)
	if err != nil {
		return nil, err
	}
	if len(offerer.Nonce) != NonceLen {
		return nil, fmt.Errorf("pairing: offerer nonce is %d bytes, want %d", len(offerer.Nonce), NonceLen)
	}
	if len(scanner.Nonce) != NonceLen {
		return nil, fmt.Errorf("pairing: scanner nonce is %d bytes, want %d", len(scanner.Nonce), NonceLen)
	}

	lowID, highID := order(idA, idB)
	lowPriv, highPriv := order(privA, privB)

	b := make([]byte, 0, len(sasContext)+4*ed25519.PublicKeySize+2*NonceLen)
	b = append(b, sasContext...)
	b = append(b, lowID...)
	b = append(b, highID...)
	b = append(b, lowPriv...)
	b = append(b, highPriv...)
	b = append(b, offerer.Nonce...)
	b = append(b, scanner.Nonce...)
	return b, nil
}

// SAS derives the six-digit short authentication string both users compare
// (PROTOCOL.md §5.2):
//
//	SAS = decimal( SHA-256(transcript)[0:4] ) mod 1000000
//
// The four bytes are read little-endian per §0. The result is always six
// characters, zero-padded: a value that rendered as "4711" on one screen and
// "004711" on the other is a comparison users would get wrong.
func SAS(offerer, scanner Party) (string, error) {
	t, err := Transcript(offerer, scanner)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(t)
	v := binary.LittleEndian.Uint32(sum[:4])
	return fmt.Sprintf("%0*d", SASDigits, v%sasModulus), nil
}

// EqualSAS compares two short authentication strings in constant time.
//
// Constant time matters here even though a SAS is compared by a human on two
// screens: an implementation that also compares them programmatically -- a
// desktop confirming what a phone reported, a test harness, a future
// auto-confirm over a second channel -- must not leak the digits through timing
// to whatever is on the other end of that path.
func EqualSAS(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// FormatSAS groups a SAS for display: "123 456". Grouping is the only thing
// that makes six digits reliably comparable by eye across two devices.
func FormatSAS(sas string) string {
	if len(sas) != SASDigits {
		return sas
	}
	return sas[:3] + " " + sas[3:]
}

// NewNonce returns the 32 random bytes this device contributes to the
// transcript (§5.2). Including both nonces is what stops a previous pairing
// being replayed.
func NewNonce() ([]byte, error) {
	n := make([]byte, NonceLen)
	if _, err := rand.Read(n); err != nil {
		return nil, fmt.Errorf("pairing: nonce: %w", err)
	}
	return n, nil
}

// checkKey validates a public key and, when absent is allowed, substitutes the
// 32 zero bytes that stand in for "this device has no privilege key".
func checkKey(what string, k ed25519.PublicKey, allowAbsent bool) ([]byte, error) {
	if len(k) == 0 && allowAbsent {
		return make([]byte, ed25519.PublicKeySize), nil
	}
	if len(k) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("pairing: %s key is %d bytes, want %d", what, len(k), ed25519.PublicKeySize)
	}
	return k, nil
}

func order(a, b []byte) (low, high []byte) {
	if bytes.Compare(a, b) <= 0 {
		return a, b
	}
	return b, a
}
