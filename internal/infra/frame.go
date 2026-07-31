// Package infra holds what the rendezvous server (PROTOCOL.md §16) and the
// relay (§17) have in common: the control-message framing D-62 defines, and the
// TLS arrangement both use.
//
// It exists because the two services are spoken to the same way and are
// otherwise unrelated. Without it the framing would be written twice and would
// drift, which for a wire format means two implementations that stop
// interoperating without either being wrong on its own terms.
package infra

import (
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// Framing, which PROTOCOL.md §16 does not specify (D-62).
//
//	msgType  u16   InfraMessageType
//	length   u32   payload bytes
//	payload  protobuf
//
// Little-endian per §0. It is not the §3 envelope: that frame carries a capID
// and a protocol version negotiated in a Hello, and none of those exist here —
// this connection is spoken to a server, not to a peer, and reusing the
// envelope would mean inventing values for three fields that mean nothing.
const (
	// HeaderSize is msgType u16 + length u32.
	HeaderSize = 6

	// MaxMessageSize bounds one control message. Registrations are a few
	// hundred bytes; the cap exists so a hostile client cannot make the server
	// allocate by claiming a large length.
	MaxMessageSize = 64 << 10
)

// Message types, from the schema rather than from a second list here.
const (
	MsgRegister       = uint16(openairv1.InfraMessageType_INFRA_MESSAGE_TYPE_REGISTER)
	MsgRegisterAck    = uint16(openairv1.InfraMessageType_INFRA_MESSAGE_TYPE_REGISTER_ACK)
	MsgLookupRequest  = uint16(openairv1.InfraMessageType_INFRA_MESSAGE_TYPE_LOOKUP_REQUEST)
	MsgLookupResponse = uint16(openairv1.InfraMessageType_INFRA_MESSAGE_TYPE_LOOKUP_RESPONSE)
	MsgError          = uint16(openairv1.InfraMessageType_INFRA_MESSAGE_TYPE_ERROR)

	MsgRelayHello     = uint16(openairv1.InfraMessageType_INFRA_MESSAGE_TYPE_RELAY_HELLO)
	MsgRelayChallenge = uint16(openairv1.InfraMessageType_INFRA_MESSAGE_TYPE_RELAY_CHALLENGE)
	MsgRelayAuth      = uint16(openairv1.InfraMessageType_INFRA_MESSAGE_TYPE_RELAY_AUTH)
	MsgRelayAuthOK    = uint16(openairv1.InfraMessageType_INFRA_MESSAGE_TYPE_RELAY_AUTH_OK)
)

// writeMessage frames and writes one message.
func WriteMessage(w io.Writer, msgType uint16, msg proto.Message) error {
	payload, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("infra: marshal type %d: %w", msgType, err)
	}
	if len(payload) > MaxMessageSize {
		return fmt.Errorf("infra: message type %d is %d bytes, over the %d cap",
			msgType, len(payload), MaxMessageSize)
	}
	buf := make([]byte, 0, HeaderSize+len(payload))
	buf = binary.LittleEndian.AppendUint16(buf, msgType)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(payload)))
	buf = append(buf, payload...)
	_, err = w.Write(buf)
	return err
}

// readMessage reads one frame. The length is validated before anything is
// allocated, so a six-byte header claiming 4 GiB costs the reader nothing.
func ReadMessage(r io.Reader) (uint16, []byte, error) {
	var hdr [HeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	msgType := binary.LittleEndian.Uint16(hdr[0:2])
	length := binary.LittleEndian.Uint32(hdr[2:6])
	if length > MaxMessageSize {
		return 0, nil, fmt.Errorf("infra: frame claims %d bytes, over the %d cap", length, MaxMessageSize)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return msgType, payload, nil
}

// readInto reads one frame and unmarshals it, checking the type is the one
// expected. An InfraError arriving in place of the expected reply is returned
// as a Go error, so a caller never has to check for it separately.
func ReadInto(r io.Reader, want uint16, out proto.Message) error {
	msgType, payload, err := ReadMessage(r)
	if err != nil {
		return err
	}
	if msgType == MsgError && want != MsgError {
		var e openairv1.InfraError
		if err := proto.Unmarshal(payload, &e); err != nil {
			return fmt.Errorf("infra: malformed error reply: %w", err)
		}
		return &ServerError{Message: e.GetMessage()}
	}
	if msgType != want {
		return fmt.Errorf("infra: expected message type %d, got %d", want, msgType)
	}
	return proto.Unmarshal(payload, out)
}

// ServerError is a refusal from the server, as opposed to a transport failure.
// It is a distinct type because the two call for different responses: a refusal
// will repeat until something changes, and a dropped connection will not.
type ServerError struct{ Message string }

func (e *ServerError) Error() string { return "infra: " + e.Message }

// PairingTLS builds a per-connection TLS config that presents this identity and
// observes whatever key the other end presents.
//
// "Pairing mode" is the identity package's name for it and the meaning carries
// over: nothing is pinned in advance. A server does not know which devices will
// connect, and a client identifies its server by the DeviceID that key derives
// (D-62) rather than by a certificate authority, of which this design has none.
func PairingTLS(id identity.Identity) (*tls.Config, *identity.ObservedKey, error) {
	type pairingConfigurer interface {
		TLSConfigPairing() (*tls.Config, *identity.ObservedKey, error)
	}
	pc, ok := id.(pairingConfigurer)
	if !ok {
		return nil, nil, errors.New("infra: identity cannot observe peer keys")
	}
	return pc.TLSConfigPairing()
}
