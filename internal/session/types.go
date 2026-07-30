package session

import (
	"context"
	"io"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
)

// EnvelopeHeaderSize is the fixed 8-byte header: version, capID, msgType,
// length. PROTOCOL.md §3. Not protobuf -- demultiplexing costs no allocation.
const EnvelopeHeaderSize = 8

// MaxMessageSize caps an envelope payload. Bulk data does not travel this way,
// so no legitimate control message approaches it. PROTOCOL.md §3.
const MaxMessageSize = 16 << 20

// Envelope is one framed control message.
//
// CapID and MsgType are WIRE values, not generated-enum values; the two differ
// by one because proto3 reserves zero (D-34). Convert, never cast.
type Envelope struct {
	Version byte
	CapID   byte
	MsgType uint16
	Payload []byte
}

// EncodeEnvelope and DecodeEnvelope are the framing boundary. Golden vectors
// for these live in internal/session/testdata (HLD §5).
func EncodeEnvelope(w io.Writer, e Envelope) error   { panic("M1a: unimplemented") }
func DecodeEnvelope(r io.Reader) (Envelope, error)   { panic("M1a: unimplemented") }

// Stream is one QUIC stream. Capabilities receive these; they never touch
// quic-go directly, which is what keeps them path-agnostic (D-6).
type Stream interface {
	io.ReadWriteCloser
	// Reset abandons the stream without delivering what remains. Used by the
	// media plane to drop stale frames (PROTOCOL.md §14, provisional).
	Reset(code uint32)
}

// PathInfo is an advisory quality hint. Capabilities may adapt to it and must
// function without it (PRD R8).
type PathInfo struct {
	RTTMillis      uint32
	BandwidthBytes uint64
	Class          string // "lan" | "punched" | "relayed"
}

// Session is what a capability sees. It cannot tell whether the underlying
// path is LAN, hole-punched or relayed.
type Session interface {
	Peer() identity.Peer
	OpenStream(ctx context.Context) (Stream, error)
	SendDatagram(b []byte) error
	PathInfo() PathInfo

	// Send frames and writes one control message on the control stream.
	Send(ctx context.Context, capID byte, msgType uint16, msg proto.Message) error

	// Quiesce asks the peer to throttle bulk while a high-bandwidth capability
	// runs, and returns a release func. Priority lives here rather than in the
	// transport because quic-go has no stream prioritisation (D-24).
	Quiesce(ctx context.Context, floorBytesPerSec uint32, reason string) (release func(), err error)

	Close(code uint16, reason string) error
}
