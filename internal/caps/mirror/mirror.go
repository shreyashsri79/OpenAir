// Package mirror is PROTOCOL.md §14: showing one device's screen on another
// (capID 6, PRD R24, R26).
//
// The framing is stream-per-frame with `RESET_STREAM`, which stopped being
// provisional when the ADR-4 spike measured it (D-84). Each encoded frame gets
// its own stream, opening with a FrameHeader and followed by the raw bytes; a
// frame that has not finished sending when a newer one is ready is reset rather
// than completed. That reset is the whole design in one sentence: realtime
// video wants the *current* frame, and a frame that arrives late is worse than
// one that never arrives, because it also delayed everything behind it.
//
// # What this package does and does not do
//
// It does the protocol: session control, framing, the stale-frame reset, the
// keyframe request after loss, and the bitrate the sink asks for. It does not
// encode video and it does not decode it. Capture and encoding are an external
// program (D-85) — this package holds a Capturer, and what a Capturer produces
// is already-encoded frames.
//
// # Authorisation
//
// Mirroring is Owned, and it goes further than that: §6.3 requires an
// announcement before any use of `mirror`, the accessed device shows an
// indicator for as long as it stands, and this implementation will not start
// without the local device having opted in. Someone watching your screen is the
// most invasive thing this protocol does, and it is the one capability where
// the answer to "was that on by default?" has to be no.
package mirror

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// CapID is the mirror capability's wire ID (Appendix B).
const CapID byte = 0x06

// Message types (§14.1).
const (
	MsgStart       = uint16(openairv1.MirrorMessageType_MIRROR_MESSAGE_TYPE_START)
	MsgStop        = uint16(openairv1.MirrorMessageType_MIRROR_MESSAGE_TYPE_STOP)
	MsgRequestIDR  = uint16(openairv1.MirrorMessageType_MIRROR_MESSAGE_TYPE_REQUEST_IDR)
	MsgBitrateHint = uint16(openairv1.MirrorMessageType_MIRROR_MESSAGE_TYPE_BITRATE_HINT)
	MsgStats       = uint16(openairv1.MirrorMessageType_MIRROR_MESSAGE_TYPE_STATS)
	MsgFrameHeader = uint16(openairv1.MirrorMessageType_MIRROR_MESSAGE_TYPE_FRAME_HEADER)
)

const (
	// DefaultFPS is what a source captures at when the sink does not say.
	DefaultFPS = 30

	// DefaultBitrate is the encoder's target when the sink does not say.
	// 8 Mb/s is a comfortable 1080p screen share; the sink lowers it with
	// BitrateHint, and §14.2 records that the hint is load-bearing rather than
	// advisory (D-84).
	DefaultBitrate = 1 << 20 // bytes per second

	// MaxFrameBytes bounds one frame. A keyframe at a sane bitrate is a few
	// hundred kilobytes; this is the point past which a sink stops believing
	// a header rather than allocating what it asks for.
	MaxFrameBytes = 8 << 20

	// staleAfter is how long a frame may still be going out before a newer one
	// abandons it, expressed in frame intervals. Two intervals is enough that
	// ordinary jitter does not throw frames away, and short enough that a
	// stalled frame does not hold up the one behind it.
	staleAfter = 2

	// statsInterval is §14.1's "~1 Hz" for MirrorStats.
	statsInterval = time.Second
)

// ErrNotSharing reports a device that has not agreed to be mirrored.
var ErrNotSharing = errors.New("mirror: that device is not sharing its screen")

// Frame is one encoded frame from a Capturer.
type Frame struct {
	// Data is the encoded frame, in whatever the codec's own framing is. For
	// H.264 that is Annex-B with start codes, which is what every decoder this
	// is likely to meet will accept.
	Data []byte

	// Keyframe says this frame can be decoded without the ones before it,
	// which is what a sink that just joined -- or just lost something -- needs.
	Keyframe bool

	// CapturedAt is when the pixels were on screen, not when encoding
	// finished. It is what the sink's latency figure is measured against.
	CapturedAt time.Time
}

// CaptureOptions is what a sink asked for, as a source's capturer sees it.
type CaptureOptions struct {
	Width, Height int
	FPS           int
	Bitrate       int // bytes per second
	DisplayID     int
}

// Capturer produces encoded frames. Implementations live outside this package
// because capture and encoding are platform work; see internal/screen.
type Capturer interface {
	// Start begins capture and returns a channel of frames, closed when
	// capture ends. Cancelling ctx stops it.
	Start(ctx context.Context, opts CaptureOptions) (<-chan Frame, error)

	// RequestKeyframe asks the encoder for an IDR at the next opportunity
	// (§14.1's RequestIDR). Implementations that cannot may return an error;
	// the session continues, because the next scheduled keyframe still comes.
	RequestKeyframe() error

	// SetBitrate applies a sink's BitrateHint. §14.2 makes this load-bearing:
	// a source that ignores it shows a viewer a fraction of the frames it
	// believes it is sending (D-84).
	SetBitrate(bytesPerSec int) error
}

// Config configures the capability.
type Config struct {
	// Capture builds the capturer for one session. Nil means this device does
	// not share its screen, and every MirrorStart is refused -- which is the
	// default, deliberately.
	Capture func() (Capturer, error)

	// Allowed reports whether this peer may start a session right now. It is
	// how the daemon enforces §6.3's announcement, exactly as input does
	// (D-82).
	Allowed func(identity.DeviceID) bool

	// OnFrame is called on the sink for each complete frame.
	OnFrame func(peer identity.DeviceID, f Frame)

	// OnStart and OnStop let the daemon raise and lower its indicator.
	OnStart func(peer identity.DeviceID, opts CaptureOptions)
	OnStop  func(peer identity.DeviceID)

	Logf func(format string, args ...any)
}

// Capability is both halves of §14: the source that sends frames and the sink
// that receives them.
type Capability struct {
	cfg Config

	mu       sync.Mutex
	sending  map[identity.DeviceID]*sendSession
	watching map[identity.DeviceID]*watchSession
}

// sendSession is one screen this device is sharing.
type sendSession struct {
	peer    identity.DeviceID
	cancel  context.CancelFunc
	capture Capturer
	opts    CaptureOptions
	release func() // the quiesce release (§7.1, §14.1)

	mu      sync.Mutex
	sent    uint64
	stale   uint64
	bitrate int
}

// watchSession is one screen this device is watching.
type watchSession struct {
	peer     identity.DeviceID
	sess     session.Session
	cancel   context.CancelFunc
	opts     CaptureOptions
	lastSeq  uint64
	decoded  uint32
	dropped  uint32
	latency  []time.Duration
	needsIDR bool

	mu sync.Mutex
}

// New builds the capability.
func New(cfg Config) *Capability {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Capability{
		cfg:      cfg,
		sending:  map[identity.DeviceID]*sendSession{},
		watching: map[identity.DeviceID]*watchSession{},
	}
}

// CapID is §14's capability ID.
func (c *Capability) CapID() byte { return CapID }

// RequiredLevel is Trusted, and the Owned check happens elsewhere — which needs
// explaining, because watching a screen is exactly what Owned is for.
//
// The session layer's level check applies to every message of a capability in
// both directions. Frames travel source-to-sink, and the party who has to prove
// anything is the *sink*: it is the one asking to watch. A blanket Owned
// requirement would demand a proof from the source for each of its own frames,
// which is both meaningless and impossible — the source is not the one that
// unlocked.
//
// So the gate is where the asking happens. The sink's request to start rides
// the §6.3 announcement, which carries the Owned proof and is verified by §6's
// ordinary machinery, and `Allowed` refuses a MirrorStart with no live
// announcement behind it (D-82). What remains at this level is what every
// capability needs: the peer is paired and Trusted.
func (c *Capability) RequiredLevel() identity.TrustLevel { return identity.LevelTrusted }

// Serve handles §14.1's control messages.
func (c *Capability) Serve(ctx context.Context, sess session.Session, msgType uint16, payload []byte) error {
	switch msgType {
	case MsgStart:
		var msg openairv1.MirrorStart
		if err := proto.Unmarshal(payload, &msg); err != nil {
			return err
		}
		return c.startSharing(ctx, sess, &msg)

	case MsgStop:
		c.stopSharing(sess.Peer().DeviceID)
		return nil

	case MsgRequestIDR:
		return c.requestIDR(sess.Peer().DeviceID)

	case MsgBitrateHint:
		var msg openairv1.BitrateHint
		if err := proto.Unmarshal(payload, &msg); err != nil {
			return err
		}
		return c.setBitrate(sess.Peer().DeviceID, int(msg.GetBytesPerSec()))

	case MsgStats:
		var msg openairv1.MirrorStats
		if err := proto.Unmarshal(payload, &msg); err != nil {
			return err
		}
		c.cfg.Logf("mirror: %s decoded %d, dropped %d, jitter %dms",
			sess.Peer().DeviceID.Fingerprint(), msg.GetFramesDecoded(), msg.GetFramesDropped(), msg.GetJitterMs())
		return nil
	}
	return session.ErrUnknownMsgType
}

// ServeStream receives one frame (§14.2).
func (c *Capability) ServeStream(ctx context.Context, sess session.Session, st session.Stream, msgType uint16, payload []byte) error {
	if msgType != MsgFrameHeader {
		st.Reset(uint32(session.CodeProtocolViolation))
		return nil
	}
	var header openairv1.FrameHeader
	if err := proto.Unmarshal(payload, &header); err != nil {
		st.Reset(uint32(session.CodeProtocolViolation))
		return nil
	}

	peer := sess.Peer().DeviceID
	c.mu.Lock()
	w := c.watching[peer]
	c.mu.Unlock()
	if w == nil {
		// A frame from a device we are not watching. Not an error worth
		// closing anything over -- a stop and a frame in flight cross.
		st.Reset(uint32(session.CodeCapabilityUnavailable))
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(st, MaxFrameBytes+1))
	if err != nil {
		// The source abandoned it: §14.2's reset, which is the design working
		// rather than a failure. The sink counts it and asks for a keyframe if
		// it has lost its footing.
		w.drop(header.GetSeq())
		return nil
	}
	if len(body) > MaxFrameBytes {
		st.Reset(uint32(session.CodeResourceExhausted))
		return nil
	}

	f := Frame{
		Data:       body,
		Keyframe:   header.GetKeyframe(),
		CapturedAt: time.UnixMicro(header.GetCapturedAt()),
	}
	w.arrived(header.GetSeq(), f)

	if c.cfg.OnFrame != nil {
		c.cfg.OnFrame(peer, f)
	}
	return nil
}

// drop records a frame that never completed.
func (w *watchSession) drop(seq uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dropped++
	// A gap matters only if it left us without a decodable picture; the sink
	// asks for a keyframe once rather than once per lost frame, which would be
	// a request storm exactly when the path is already struggling.
	w.needsIDR = true
}

// arrived records a complete frame.
func (w *watchSession) arrived(seq uint64, f Frame) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.decoded++
	if seq > w.lastSeq {
		w.lastSeq = seq
	}
	if f.Keyframe {
		w.needsIDR = false
	}
	if !f.CapturedAt.IsZero() {
		w.latency = append(w.latency, time.Since(f.CapturedAt))
		if len(w.latency) > 600 {
			w.latency = w.latency[len(w.latency)-600:]
		}
	}
}
