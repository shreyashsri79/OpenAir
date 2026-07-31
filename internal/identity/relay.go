package identity

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
)

// Relay authentication, PROTOCOL.md §17.
//
// §17 says a client authenticates by signing "both nonces" and stops there.
// Two things it does not say, and both matter:
//
//   - **No domain separation.** D-43 raised this as one of four open security
//     questions and it is answered here: the signed bytes begin with a context
//     string of their own, so a signature made for a relay cannot be presented
//     as an Owned AuthProof (§6) or as a rendezvous registration (§16), and
//     none of theirs can be presented here. Without it, three protocols would
//     be signing bare byte strings with the same key and the only thing keeping
//     them apart would be luck about their lengths.
//   - **No length framing.** Two nonces concatenated raw are re-splittable in
//     the same way §16's endpoint list was (D-63), so each is length-prefixed.
//
// Both are recorded in D-65.

// relayContext is the domain separator for §17.
const relayContext = "openair-relay-v1"

// RelayNonceLen is the length of each side's nonce. §17 does not give one; 32
// bytes matches §6's AuthProof nonce, which is the only other random challenge
// in this protocol.
const RelayNonceLen = 32

// ErrRelaySignature reports a relay authentication whose signature does not
// verify against the key its DeviceID derives from.
var ErrRelaySignature = errors.New("identity: relay authentication signature does not verify")

// RelaySigningInput builds the byte string a RelayAuth signs (§17):
//
//	"openair-relay-v1" || device_id || client_nonce || server_nonce
//
// The server nonce is what makes this exchange fresh: a signature captured from
// one session does not authenticate the next, because the server chose
// different bytes for it.
func RelaySigningInput(deviceID DeviceID, clientNonce, serverNonce []byte) ([]byte, error) {
	if !deviceID.Valid() {
		return nil, fmt.Errorf("identity: %q is not a DeviceID", deviceID)
	}
	if len(clientNonce) != RelayNonceLen || len(serverNonce) != RelayNonceLen {
		return nil, fmt.Errorf("identity: relay nonces are %d and %d bytes, want %d each",
			len(clientNonce), len(serverNonce), RelayNonceLen)
	}
	b := make([]byte, 0, len(relayContext)+DeviceIDLen+8+2*RelayNonceLen)
	b = append(b, relayContext...)
	b = append(b, deviceID...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(clientNonce)))
	b = append(b, clientNonce...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(serverNonce)))
	b = append(b, serverNonce...)
	return b, nil
}

// SignRelayAuth signs a relay challenge with this device's identity key. The
// identity key, not the privilege key: a device whose relay path died when its
// unlock expired would be unreachable exactly when D-20 says it must not be.
func (i *FileIdentity) SignRelayAuth(clientNonce, serverNonce []byte) ([]byte, error) {
	input, err := RelaySigningInput(i.deviceID, clientNonce, serverNonce)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(i.identityPriv, input), nil
}

// VerifyRelayAuth checks a relay authentication against the key its DeviceID
// derives from (§2).
func VerifyRelayAuth(pub ed25519.PublicKey, deviceID DeviceID, clientNonce, serverNonce, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("identity: relay key is %d bytes, want %d", len(pub), ed25519.PublicKeySize)
	}
	if got := DeriveDeviceID(pub); got != deviceID {
		return fmt.Errorf("%w: key derives %s, the client claims %s", ErrRelaySignature, got, deviceID)
	}
	input, err := RelaySigningInput(deviceID, clientNonce, serverNonce)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, input, sig) {
		return ErrRelaySignature
	}
	return nil
}
