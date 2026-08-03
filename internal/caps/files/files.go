package files

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// CapID is the wire capability byte for files (PROTOCOL.md Appendix B).
//
// The generated enum CAPABILITY_ID_FILES is 2, because proto3 reserves zero.
// The wire byte is 1. Convert, never cast (D-34) -- capIDWire below is the only
// place that conversion is written.
const CapID byte = 0x01

// capIDWire converts the generated capability enum to its wire byte.
func capIDWire(e openairv1.CapabilityId) byte { return byte(e) - 1 }

// Message types. Unlike capID and the domain enums in §3, msgType has no
// document numbering that predates the schema: §3 says values "are enumerated
// per capability in the schemas", so the wire msgType is the generated
// FilesMessageType value itself, with no offset. Zero is never sent.
const (
	MsgTransferOffer    uint16 = uint16(openairv1.FilesMessageType_FILES_MESSAGE_TYPE_TRANSFER_OFFER)
	MsgTransferAccept   uint16 = uint16(openairv1.FilesMessageType_FILES_MESSAGE_TYPE_TRANSFER_ACCEPT)
	MsgStreamInit       uint16 = uint16(openairv1.FilesMessageType_FILES_MESSAGE_TYPE_STREAM_INIT)
	MsgChunkManifest    uint16 = uint16(openairv1.FilesMessageType_FILES_MESSAGE_TYPE_CHUNK_MANIFEST)
	MsgTransferComplete uint16 = uint16(openairv1.FilesMessageType_FILES_MESSAGE_TYPE_TRANSFER_COMPLETE)
	MsgTransferCancel   uint16 = uint16(openairv1.FilesMessageType_FILES_MESSAGE_TYPE_TRANSFER_CANCEL)
	MsgTransferProgress uint16 = uint16(openairv1.FilesMessageType_FILES_MESSAGE_TYPE_TRANSFER_PROGRESS)
)

// DefaultStreams is the data-stream count.
//
// Two, not eight. D-13 measured QUIC peaking at two streams and declining past
// them; D-33 measured Windows falling 2.2x from one stream to four. v1.0's
// eight workers are actively harmful on QUIC. Config.Streams is a tuning escape
// hatch, not a knob to turn up.
const DefaultStreams = 2

// MaxStreams caps both what a sender will open and what a receiver will accept
// for one transfer. Well past anything measurement supports; it exists to bound
// a hostile peer, not to be reached.
const MaxStreams = 8

// ProgressInterval is how often a receiver reports (PROTOCOL.md §8.5: "roughly
// 1 Hz"). Speed and ETA are derived by the sender and deliberately not on the
// wire, because they are presentation.
const ProgressInterval = time.Second

var (
	// ErrRejected reports that the peer declined the offer.
	ErrRejected = errors.New("files: transfer rejected by peer")
	// ErrCancelled reports a TransferCancel from either side (§8.5).
	ErrCancelled = errors.New("files: transfer cancelled")
	// ErrUnknownTransfer reports a message for a transfer_id we know nothing
	// about. Ignored rather than fatal, per §3.1's spirit.
	ErrUnknownTransfer = errors.New("files: unknown transfer")
	// ErrVerification reports chunks that failed their manifest digest (§8.4).
	ErrVerification = errors.New("files: chunk verification failed")
	// ErrSessionClosed reports that the session ended while a transfer was
	// waiting on the peer. A receiver that refuses the sender closes the
	// connection instead of answering (§10), so every wait on a peer reply has
	// to watch the session as well or it waits for a reply nobody will send.
	ErrSessionClosed = errors.New("files: session closed before the peer replied")
)

// encodeEnvelope and decodeEnvelope indirect over the session layer's framing
// (PROTOCOL.md §3). The first message on a capability stream is an envelope,
// and this package must not own a second implementation of that format --
// production always uses session's. The indirection exists so this package's
// tests can run while M1a is still being written; nothing else should replace
// them.
var (
	encodeEnvelope = session.EncodeEnvelope
	decodeEnvelope = session.DecodeEnvelope
)

// Progress is a receiver-side progress report, delivered to both ends.
type Progress struct {
	TransferID     string
	BytesReceived  uint64
	ChunksVerified uint64
	TotalBytes     uint64
}

// Offer is the inbound offer handed to Config.Accept.
type Offer struct {
	TransferID string
	Files      []*openairv1.FileMeta
	TotalBytes uint64
	ChunkSize  uint64
	Streams    uint32
}

// Config configures the capability. The zero value is usable except for
// DestRoot, which must be set before any inbound transfer is accepted.
type Config struct {
	// DestRoot is where inbound files land. Every offered path is resolved
	// under it and anything escaping is refused (§8.1).
	DestRoot string

	// Streams is the data-stream count a sender opens. Zero means
	// DefaultStreams. See D-13 and D-33 before raising it.
	Streams int

	// ChunkSize is the offered chunk size. Zero means DefaultChunkSize.
	ChunkSize uint64

	// Accept decides an inbound offer. Nil auto-accepts from Owned peers and
	// refuses everyone else, which is PRD R11's rule and the safe default.
	Accept func(ctx context.Context, peer identity.Peer, offer Offer) (bool, error)

	// OnProgress, if set, is called for progress on either side.
	OnProgress func(Progress)

	// OnComplete, if set, is called on the receiving side once a transfer has
	// finished, successfully or not, immediately before TransferComplete goes
	// back to the sender (§8.4).
	//
	// The sender learns the outcome as Send's return value; the receiver has no
	// such return, because inbound transfers arrive on the session's control
	// loop rather than through a call. Without this, a receiving process can
	// only infer completion from progress reaching the total, which is not the
	// same thing -- it cannot distinguish a verified commit from a transfer
	// that delivered every byte and then failed its digest check.
	OnComplete func(transferID string, ok bool)

	// StateDir holds resume bookkeeping (the verified-chunk set for partial
	// transfers). Empty means DestRoot/.openair-state.
	StateDir string
}

func (c Config) streams() int {
	n := c.Streams
	if n <= 0 {
		n = DefaultStreams
	}
	if n > MaxStreams {
		n = MaxStreams
	}
	return n
}

func (c Config) chunkSize() uint64 {
	if c.ChunkSize == 0 {
		return DefaultChunkSize
	}
	return c.ChunkSize
}

// Capability is the files capability (capID 1). It implements caps.Capability
// and talks to the network only through session.Session and session.Stream,
// which is what keeps it path-agnostic (D-6).
type Capability struct {
	cfg Config

	mu  sync.Mutex
	out map[string]*sendState // transfers we are sending
	in  map[string]*recvState // transfers we are receiving
}

// New returns a files capability.
func New(cfg Config) *Capability {
	return &Capability{
		cfg: cfg,
		out: make(map[string]*sendState),
		in:  make(map[string]*recvState),
	}
}

// CapID implements caps.Capability.
func (c *Capability) CapID() byte { return CapID }

// RequiredLevel implements caps.Capability. Trusted is the floor; Trusted peers
// need explicit consent per offer, which Config.Accept decides (§8.1).
func (c *Capability) RequiredLevel() identity.TrustLevel { return identity.LevelTrusted }

// Serve handles one inbound control message. The session layer has already
// demultiplexed capID and authorised the message (§3, §6); this must not repeat
// that check.
func (c *Capability) Serve(ctx context.Context, sess session.Session, msgType uint16, payload []byte) error {
	switch msgType {
	case MsgTransferOffer:
		var m openairv1.TransferOffer
		if err := proto.Unmarshal(payload, &m); err != nil {
			return err
		}
		return c.onOffer(ctx, sess, &m)

	case MsgChunkManifest:
		var m openairv1.ChunkManifest
		if err := proto.Unmarshal(payload, &m); err != nil {
			return err
		}
		return c.onManifest(&m)

	case MsgTransferAccept:
		var m openairv1.TransferAccept
		if err := proto.Unmarshal(payload, &m); err != nil {
			return err
		}
		return c.deliverSend(m.GetTransferId(), func(s *sendState) { s.accept(&m) })

	case MsgTransferComplete:
		var m openairv1.TransferComplete
		if err := proto.Unmarshal(payload, &m); err != nil {
			return err
		}
		return c.deliverSend(m.GetTransferId(), func(s *sendState) { s.complete(&m) })

	case MsgTransferProgress:
		var m openairv1.TransferProgress
		if err := proto.Unmarshal(payload, &m); err != nil {
			return err
		}
		return c.onProgress(&m)

	case MsgTransferCancel:
		var m openairv1.TransferCancel
		if err := proto.Unmarshal(payload, &m); err != nil {
			return err
		}
		return c.onCancel(&m)

	default:
		// §3.1: an unrecognised msgType is ignored, never fatal. That is what
		// makes mixed-version fleets viable. The session layer's dispatcher
		// treats this sentinel as "ignore and continue" rather than an error.
		return fmt.Errorf("files: msgType %d: %w", msgType, session.ErrUnknownMsgType)
	}
}

// ServeStream handles a data stream after its opening envelope (§8.2). The
// stream then carries raw chunk frames only -- no protobuf on the hot path.
func (c *Capability) ServeStream(ctx context.Context, sess session.Session, st session.Stream, msgType uint16, payload []byte) error {
	if msgType != MsgStreamInit {
		return fmt.Errorf("files: stream opened with msgType %d, want StreamInit: %w",
			msgType, session.ErrUnknownMsgType)
	}
	var m openairv1.StreamInit
	if err := proto.Unmarshal(payload, &m); err != nil {
		return err
	}
	rs := c.lookupRecv(m.GetTransferId())
	if rs == nil {
		return fmt.Errorf("%w: %q", ErrUnknownTransfer, m.GetTransferId())
	}
	return rs.serveStream(ctx, st)
}

func (c *Capability) lookupRecv(id string) *recvState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.in[id]
}

func (c *Capability) lookupSend(id string) *sendState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out[id]
}

// deliverSend routes a receiver-to-sender message to its transfer.
//
// An unknown transfer_id is not an error here. A TransferComplete or a final
// TransferProgress routinely arrives after the sender has already given up and
// torn the transfer down, and treating that normal race as a fault would fill
// the log with warnings about correct behaviour.
func (c *Capability) deliverSend(id string, fn func(*sendState)) error {
	if s := c.lookupSend(id); s != nil {
		fn(s)
	}
	return nil
}

func (c *Capability) onProgress(m *openairv1.TransferProgress) error {
	if c.cfg.OnProgress == nil {
		return nil
	}
	var total uint64
	if s := c.lookupSend(m.GetTransferId()); s != nil {
		total = s.plan.TotalBytes()
	}
	c.cfg.OnProgress(Progress{
		TransferID:     m.GetTransferId(),
		BytesReceived:  m.GetBytesReceived(),
		ChunksVerified: m.GetChunksVerified(),
		TotalBytes:     total,
	})
	return nil
}

func (c *Capability) onCancel(m *openairv1.TransferCancel) error {
	id := m.GetTransferId()
	if s := c.lookupSend(id); s != nil {
		s.cancel(m)
	}
	if r := c.lookupRecv(id); r != nil {
		r.cancel(m)
	}
	return nil
}

// Cancel sends a TransferCancel for an in-flight transfer in either direction
// (§8.5). discardPartial defaults to false at the protocol level because a
// cancel on a flaky link is usually a prelude to retrying; senders SHOULD offer
// discard explicitly rather than defaulting to it.
func (c *Capability) Cancel(ctx context.Context, sess session.Session, transferID, reason string, discardPartial bool) error {
	m := &openairv1.TransferCancel{
		TransferId:     transferID,
		Reason:         reason,
		DiscardPartial: discardPartial,
	}
	if err := sess.Send(ctx, CapID, MsgTransferCancel, m); err != nil {
		return err
	}
	return c.onCancel(m)
}
