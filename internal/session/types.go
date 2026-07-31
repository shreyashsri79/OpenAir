package session

import (
	"context"
	"io"

	"github.com/apernet/quic-go"
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
// CapID is a WIRE value, not a generated-enum value; the two differ by one
// because proto3 reserves zero (D-34). Convert with CapIDToWire /
// CapIDFromWire, never cast.
//
// MsgType is the exception to that rule: PROTOCOL.md never enumerated msgType,
// so the schemas' per-capability *MessageType enums are the original
// definition and their values ARE the wire values, with 0/UNSPECIFIED simply
// invalid. See convert.go.
type Envelope struct {
	Version byte
	CapID   byte
	MsgType uint16
	Payload []byte
}

// EncodeEnvelope and DecodeEnvelope are the framing boundary; they live in
// envelope.go. Golden vectors for them live in internal/session/testdata
// (HLD §5).

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

	// Done is closed when the session ends, however it ended: a local Close, a
	// peer that closed the control stream, or a protocol error that killed it.
	//
	// It exists for M4. A daemon holds many sessions at once and has to know
	// when one is gone -- to drop it from the device list, and to stop holding
	// a reference to a connection that no longer exists. Without this the only
	// way to find out is to send something and see it fail, which turns every
	// stale entry into a failed user action.
	Done() <-chan struct{}
}

// Handler is what the session layer dispatches an inbound message to, once it
// has demultiplexed capID and authorised the message (PROTOCOL.md §3, §6).
//
// It exists so that `session` does not import `caps`, which would be an import
// cycle -- caps.Capability already imports session. A caps.Capability satisfies
// this structurally; registration converts nothing.
type Handler interface {
	CapID() byte
	Serve(ctx context.Context, sess Session, msgType uint16, payload []byte) error
	ServeStream(ctx context.Context, sess Session, st Stream, msgType uint16, payload []byte) error
}

// Config is everything New needs that it cannot read off the QUIC connection.
//
// Peer carries the pinned record for an already-trusted peer. Its DeviceID is
// empty during pairing, where no key is pinned yet (PROTOCOL.md §5); in that
// case New still completes Hello and leaves authorisation to the caller.
type Config struct {
	Local       identity.Identity
	Peer        identity.Peer
	DisplayName string
	Platform    string // "linux" | "windows" | "android" | "darwin"
	Handlers    map[byte]Handler
	Initiator   bool // true opens the control stream, false accepts it (§1.1)

	// Authorize decides whether a peer may proceed, and is called once Hello
	// has completed and the peer record is fully populated -- DeviceID and
	// identity key derived from the TLS certificate, display name and
	// protection tier as claimed -- but before any capability message is
	// dispatched. Returning an error closes the session.
	//
	// This exists because the pinned-key comparison above it only fires when
	// Peer is already populated, which the dialling side can do and the
	// accepting side cannot: a listener does not know who is calling until
	// Hello arrives. Without a hook here, every inbound connection would be
	// admitted unconditionally.
	//
	// A nil Authorize admits any peer. That is deliberate and it is correct
	// only for M1, whose stated scope is an explicit-address dial with the
	// fingerprint shown and accepted interactively (BUILD-PLAN.md §5, M1).
	// M2 replaces the callback with a trust-store lookup, at which point nil
	// should stop being an accepted value on the listening path.
	Authorize func(peer identity.Peer) error

	// PeerLookup returns the stored trust-store record for a DeviceID, and is
	// how the accepting side learns the things Hello cannot tell it: the pinned
	// privilege key that verifies AuthProof, and the trust level the local user
	// granted (§6, D-20).
	//
	// Authorize decides whether a peer may connect at all; this decides what it
	// may then do. They are separate because a peer can be perfectly welcome and
	// still not be Owned.
	//
	// Nil means the session enforces no trust ladder of its own and treats every
	// authorised peer as Trusted. That is correct for a caller that has already
	// made the whole decision in Authorize -- the pairing listener, a one-shot
	// CLI receive -- and wrong for a daemon, which has a trust store and should
	// pass it.
	PeerLookup func(identity.DeviceID) (identity.Peer, bool)

	// OnAuthEvent, if set, receives every authorisation decision made for an
	// inbound message. PRD R4 requires the accessed device to keep a local
	// session log and §6.3 requires authentication events in it; this is the
	// hook that makes that possible without the session layer owning a log
	// format. It is called on the control loop, so it must not block.
	OnAuthEvent func(AuthEvent)

	// PathClass reports which kind of path currently carries this peer's
	// packets -- "lan", "punched" or "relayed" (§7.2). It is a function rather
	// than a value because the answer changes underneath a live session: M9
	// upgrades a relayed path to a direct one without the connection above
	// noticing, and a class captured at Hello would then be wrong for the rest
	// of the session.
	//
	// Returning an empty string means "no better answer than the transport's",
	// which is what a caller with no path layer should do. Nil is the same
	// thing.
	PathClass func(identity.DeviceID) string
}

// New wraps an established QUIC connection in a Session: it opens or accepts
// the control stream, exchanges Hello, verifies that the peer's claimed
// DeviceID matches its TLS key, and starts the control loop (PROTOCOL.md §4).
//
// This is the seam between conn (M1d, which owns dialling and accepting) and
// session (M1a, which owns this implementation). Neither task may change the
// signature alone.
//
// The implementation is newSession in session.go, which takes an internal
// transport interface so the control loop can be tested without real QUIC.
func New(ctx context.Context, qc *quic.Conn, cfg Config) (Session, error) {
	return newSession(ctx, quicTransport{qc}, cfg)
}
