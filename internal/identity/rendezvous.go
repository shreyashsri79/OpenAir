package identity

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// Registration signing, PROTOCOL.md §16.
//
// A rendezvous server stores where a device can be reached, so the one thing it
// must not accept is somebody else moving that device's endpoints. The
// signature is what stops it, and it is made by the identity key — the warm one
// (D-20), because registration has to keep working with the privilege key
// sealed or a locked laptop would drop off the network.

// rendezvousContext is the domain separator from §16. It differs from §6's, so
// an Owned proof cannot be replayed as a registration and vice versa.
const rendezvousContext = "openair-rendezvous-v1"

// MaxRegistrationLifetime is §16's cap: a server MUST reject a registration
// whose expiry is more than ten minutes out. It forces heartbeats, which is
// what keeps a stale entry from advertising an address a device no longer has.
const MaxRegistrationLifetime = 10 * time.Minute

// ErrRegistrationSignature reports a registration whose signature does not
// verify against the key its DeviceID derives from.
var ErrRegistrationSignature = errors.New("identity: registration signature does not verify")

// RendezvousSigningInput builds the byte string a Registration signs (§16):
//
//	"openair-rendezvous-v1" || device_id || endpoints (in order)
//	                        || relay_home || issued_at || expires_at
//
// The spec gives no encoding for the variable-length parts, and concatenating
// them raw would be ambiguous: endpoints ["a", "bc"] and ["ab", "c"] would sign
// identically, so a server could be talked into accepting a re-split endpoint
// list as a valid signature. Each variable-length field is therefore
// length-prefixed with a u32 (little-endian, §0). Both the signer and the
// verifier use this function rather than re-deriving the layout.
func RendezvousSigningInput(deviceID DeviceID, endpoints []string, relayHome string, issuedAt, expiresAt int64) ([]byte, error) {
	if !deviceID.Valid() {
		return nil, fmt.Errorf("identity: %q is not a DeviceID", deviceID)
	}
	b := make([]byte, 0, 128)
	b = append(b, rendezvousContext...)
	b = append(b, deviceID...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(endpoints)))
	for _, e := range endpoints {
		b = binary.LittleEndian.AppendUint32(b, uint32(len(e)))
		b = append(b, e...)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(relayHome)))
	b = append(b, relayHome...)
	b = binary.LittleEndian.AppendUint64(b, uint64(issuedAt))
	b = binary.LittleEndian.AppendUint64(b, uint64(expiresAt))
	return b, nil
}

// SignRegistration signs a registration with this device's identity key.
func (i *FileIdentity) SignRegistration(endpoints []string, relayHome string, issuedAt, expiresAt int64) ([]byte, error) {
	input, err := RendezvousSigningInput(i.deviceID, endpoints, relayHome, issuedAt, expiresAt)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(i.identityPriv, input), nil
}

// VerifyRegistration checks a registration against the public key its DeviceID
// derives from, which is the whole trust model here: the server does not need
// to know the device, only that the DeviceID and the key agree (§2) and that
// the signature is theirs.
//
// It deliberately does not check expiry or freshness. Those are policy the
// server applies with its own clock, and mixing them in would make one function
// answer two questions with one error.
func VerifyRegistration(pub ed25519.PublicKey, deviceID DeviceID, endpoints []string, relayHome string, issuedAt, expiresAt int64, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("identity: registration key is %d bytes, want %d", len(pub), ed25519.PublicKeySize)
	}
	if got := DeriveDeviceID(pub); got != deviceID {
		return fmt.Errorf("%w: key derives %s, registration claims %s", ErrRegistrationSignature, got, deviceID)
	}
	input, err := RendezvousSigningInput(deviceID, endpoints, relayHome, issuedAt, expiresAt)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, input, sig) {
		return ErrRegistrationSignature
	}
	return nil
}
