package session

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// EnvelopeVersion is the envelope version this implementation speaks.
// PROTOCOL.md §3 defines exactly one: 1. The envelope is deliberately not
// forward-compatible -- everything else depends on parsing it correctly -- so a
// receiver seeing any other value closes the connection.
const EnvelopeVersion byte = 1

// ErrorCode is a PROTOCOL.md §10 code. It travels in QUIC CONNECTION_CLOSE for
// connection-fatal conditions and STOP_SENDING / RESET_STREAM for
// operation-fatal ones. Close takes one of these.
type ErrorCode uint16

const (
	CodeNoError               ErrorCode = 0x00
	CodeProtocolViolation     ErrorCode = 0x01
	CodeUnknownVersion        ErrorCode = 0x02
	CodeMessageTooLarge       ErrorCode = 0x03
	CodeNotPaired             ErrorCode = 0x04
	CodeKeyMismatch           ErrorCode = 0x05
	CodeUnauthorised          ErrorCode = 0x06
	CodeAuthExpired           ErrorCode = 0x07
	CodeRejected              ErrorCode = 0x08
	CodeCapabilityUnavailable ErrorCode = 0x09
	CodeResourceExhausted     ErrorCode = 0x0a
	CodeIntegrityFailure      ErrorCode = 0x0b
)

var errorCodeNames = map[ErrorCode]string{
	CodeNoError:               "NO_ERROR",
	CodeProtocolViolation:     "PROTOCOL_VIOLATION",
	CodeUnknownVersion:        "UNKNOWN_VERSION",
	CodeMessageTooLarge:       "MESSAGE_TOO_LARGE",
	CodeNotPaired:             "NOT_PAIRED",
	CodeKeyMismatch:           "KEY_MISMATCH",
	CodeUnauthorised:          "UNAUTHORISED",
	CodeAuthExpired:           "AUTH_EXPIRED",
	CodeRejected:              "REJECTED",
	CodeCapabilityUnavailable: "CAPABILITY_UNAVAILABLE",
	CodeResourceExhausted:     "RESOURCE_EXHAUSTED",
	CodeIntegrityFailure:      "INTEGRITY_FAILURE",
}

func (c ErrorCode) String() string {
	if n, ok := errorCodeNames[c]; ok {
		return n
	}
	return fmt.Sprintf("ErrorCode(0x%02x)", uint16(c))
}

// ProtocolError carries the §10 code a condition maps to, so that the layer
// that owns the QUIC connection can close with the right code without
// re-deciding what went wrong.
type ProtocolError struct {
	Code ErrorCode
	Msg  string
	Err  error // optional cause
}

func (e *ProtocolError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Msg, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Msg)
}

func (e *ProtocolError) Unwrap() error { return e.Err }

func protoErr(code ErrorCode, cause error, format string, args ...any) *ProtocolError {
	return &ProtocolError{Code: code, Msg: fmt.Sprintf(format, args...), Err: cause}
}

// ErrorCodeOf reports the §10 code err maps to. Errors that are not
// ProtocolErrors report CodeNoError and false -- they are transport or local
// failures, not protocol violations, and must not be reported to the peer as
// though the peer misbehaved.
func ErrorCodeOf(err error) (ErrorCode, bool) {
	var pe *ProtocolError
	if errors.As(err, &pe) {
		return pe.Code, true
	}
	return CodeNoError, false
}

// ErrUnknownMsgType is what a Handler returns when it recognises neither the
// message type nor any reason to fail: PROTOCOL.md §3.1 makes unknown message
// types non-fatal, and only the capability knows which types it supports, so
// the session layer cannot make that call for it. The control loop swallows
// this error and continues.
var ErrUnknownMsgType = errors.New("session: unknown message type")

// EncodeEnvelope writes the 8-byte header (PROTOCOL.md §3) followed by the
// payload, little-endian per §0, in a single Write so that a concurrent writer
// cannot interleave a partial frame.
//
// It refuses to emit anything a conformant receiver would have to kill the
// connection over: an envelope version this build does not speak, or a payload
// past the cap. Encoding is the last place a local bug can be caught before it
// becomes the peer's PROTOCOL_VIOLATION. Tests that need a rejected frame on
// the wire write the bytes by hand.
func EncodeEnvelope(w io.Writer, e Envelope) error {
	if e.Version != EnvelopeVersion {
		return protoErr(CodeProtocolViolation, nil,
			"refusing to encode envelope version %d, this build speaks %d", e.Version, EnvelopeVersion)
	}
	if len(e.Payload) > MaxMessageSize {
		return protoErr(CodeMessageTooLarge, nil,
			"payload %d bytes exceeds the %d byte cap", len(e.Payload), MaxMessageSize)
	}
	buf := make([]byte, EnvelopeHeaderSize+len(e.Payload))
	buf[0] = e.Version
	buf[1] = e.CapID
	binary.LittleEndian.PutUint16(buf[2:4], e.MsgType)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(e.Payload)))
	copy(buf[EnvelopeHeaderSize:], e.Payload)
	_, err := w.Write(buf)
	return err
}

// DecodeEnvelope reads one framed message.
//
// A clean end of stream at a frame boundary returns io.EOF unwrapped, which is
// how the control loop tells "peer closed" from "peer sent garbage". Anything
// else -- a short header, a short payload, an unknown version, an oversized
// length -- is a ProtocolError carrying the §10 code to close with.
//
// The length field is validated before it is believed: nothing sized by it is
// allocated until it is known to be within MaxMessageSize, so a peer cannot
// make a receiver reserve 16 MiB with an 8-byte write.
func DecodeEnvelope(r io.Reader) (Envelope, error) {
	var hdr [EnvelopeHeaderSize]byte
	n, err := io.ReadFull(r, hdr[:])
	switch {
	case err == io.EOF && n == 0:
		return Envelope{}, io.EOF
	case err != nil && (errors.Is(err, io.ErrUnexpectedEOF) || err == io.EOF):
		return Envelope{}, protoErr(CodeProtocolViolation, io.ErrUnexpectedEOF,
			"truncated envelope header: %d of %d bytes", n, EnvelopeHeaderSize)
	case err != nil:
		return Envelope{}, err
	}

	e := Envelope{
		Version: hdr[0],
		CapID:   hdr[1],
		MsgType: binary.LittleEndian.Uint16(hdr[2:4]),
	}
	length := binary.LittleEndian.Uint32(hdr[4:8])

	// Version first: an unknown version means every other field in this header
	// is a guess, including the length we would otherwise size a buffer from.
	//
	// §3 says close with PROTOCOL_VIOLATION here even though §10 defines
	// UNKNOWN_VERSION (0x02) for exactly this; §3 is the more specific
	// statement and wins. See the report note on that inconsistency.
	if e.Version != EnvelopeVersion {
		return Envelope{}, protoErr(CodeProtocolViolation, nil,
			"unknown envelope version %d, expected %d", e.Version, EnvelopeVersion)
	}
	if length > MaxMessageSize {
		return Envelope{}, protoErr(CodeMessageTooLarge, nil,
			"envelope length %d exceeds the %d byte cap", length, MaxMessageSize)
	}
	if length == 0 {
		return e, nil
	}

	e.Payload = make([]byte, length)
	if n, err := io.ReadFull(r, e.Payload); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || err == io.EOF {
			return Envelope{}, protoErr(CodeProtocolViolation, io.ErrUnexpectedEOF,
				"truncated envelope payload: %d of %d bytes", n, length)
		}
		return Envelope{}, err
	}
	return e, nil
}
