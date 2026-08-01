package mirror

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// The source half of §14: the device whose screen is being watched.
//
// One goroutine reads frames from the capturer and puts each on its own stream.
// The only interesting rule is the one §14.2 exists for: if the previous
// frame's stream has not finished writing when the next frame arrives, the
// previous one is reset. Everything else about realtime video follows from
// that — the sink never waits for a frame nobody wants any more, and a stalled
// path costs the frames it stalled rather than every frame after them.

// startSharing answers §14.1's MirrorStart.
func (c *Capability) startSharing(ctx context.Context, sess session.Session, msg *openairv1.MirrorStart) error {
	peer := sess.Peer().DeviceID

	if c.cfg.Capture == nil {
		return &session.ProtocolError{
			Code: session.CodeCapabilityUnavailable,
			Msg:  "this device is not sharing its screen",
		}
	}
	if c.cfg.Allowed != nil && !c.cfg.Allowed(peer) {
		return &session.ProtocolError{
			Code: session.CodeUnauthorised,
			Msg:  "no announced session permits mirroring",
		}
	}

	c.mu.Lock()
	if _, already := c.sending[peer]; already {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	opts := CaptureOptions{
		Width:     int(msg.GetWidth()),
		Height:    int(msg.GetHeight()),
		FPS:       int(msg.GetFps()),
		Bitrate:   int(msg.GetMaxBitrate()),
		DisplayID: int(msg.GetDisplayId()),
	}
	if opts.FPS <= 0 {
		opts.FPS = DefaultFPS
	}
	if opts.Bitrate <= 0 {
		opts.Bitrate = DefaultBitrate
	}

	capture, err := c.cfg.Capture()
	if err != nil {
		return &session.ProtocolError{Code: session.CodeCapabilityUnavailable, Msg: err.Error(), Err: err}
	}

	// §14.1: a mirror session quiesces bulk for its duration. The release is
	// held until the session ends, and D-24 throttles rather than stops, so a
	// transfer running underneath keeps making progress.
	sendCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	release, err := sess.Quiesce(sendCtx, uint32(opts.Bitrate/4), "mirror session")
	if err != nil {
		// A peer that does not understand quiesce is not a reason to refuse to
		// mirror; it only means bulk will compete.
		c.cfg.Logf("mirror: quiesce refused, continuing: %v", err)
		release = func() {}
	}

	frames, err := capture.Start(sendCtx, opts)
	if err != nil {
		cancel()
		release()
		return &session.ProtocolError{Code: session.CodeCapabilityUnavailable, Msg: err.Error(), Err: err}
	}

	s := &sendSession{
		peer:    peer,
		cancel:  cancel,
		capture: capture,
		opts:    opts,
		release: release,
		bitrate: opts.Bitrate,
	}
	c.mu.Lock()
	c.sending[peer] = s
	c.mu.Unlock()

	c.cfg.Logf("mirror: sharing this screen with %s at %dx%d %dfps",
		peer.Fingerprint(), opts.Width, opts.Height, opts.FPS)
	if c.cfg.OnStart != nil {
		c.cfg.OnStart(peer, opts)
	}

	go c.pump(sendCtx, sess, s, frames)
	return nil
}

// pump sends frames until capture ends or the session stops.
func (c *Capability) pump(ctx context.Context, sess session.Session, s *sendSession, frames <-chan Frame) {
	defer c.stopSharing(s.peer)

	interval := time.Second / time.Duration(s.opts.FPS)
	deadline := time.Duration(staleAfter) * interval

	// inFlight is the previous frame, still being written. §14.2 abandons it
	// when a newer frame is ready: the sink wants the current picture, and a
	// frame that has been overtaken is only in the way.
	var (
		inFlight session.Stream
		done     chan struct{}
	)

	var seq uint64
	for {
		select {
		case <-ctx.Done():
			if inFlight != nil {
				inFlight.Reset(uint32(session.CodeNoError))
			}
			return
		case f, ok := <-frames:
			if !ok {
				if inFlight != nil {
					<-done
				}
				return
			}
			seq++

			if inFlight != nil {
				select {
				case <-done:
				default:
					inFlight.Reset(uint32(session.CodeNoError))
					s.mu.Lock()
					s.stale++
					s.mu.Unlock()
				}
				inFlight, done = nil, nil
			}

			st, err := sess.OpenStream(ctx)
			if err != nil {
				return
			}
			finished := make(chan struct{})
			inFlight, done = st, finished

			go func(st session.Stream, f Frame, seq uint64, finished chan struct{}) {
				defer close(finished)
				if err := writeFrame(st, f, seq, deadline); err != nil {
					st.Reset(uint32(session.CodeNoError))
					return
				}
				st.Close()
			}(st, f, seq, finished)

			s.mu.Lock()
			s.sent++
			s.mu.Unlock()
		}
	}
}

// writeFrame puts one frame on its stream: the §3 envelope carrying a
// FrameHeader, then the raw encoded bytes (§14.2).
func writeFrame(st session.Stream, f Frame, seq uint64, deadline time.Duration) error {
	header := &openairv1.FrameHeader{
		Seq:        seq,
		CapturedAt: f.CapturedAt.UnixMicro(),
		Keyframe:   f.Keyframe,
	}
	payload, err := proto.Marshal(header)
	if err != nil {
		return err
	}
	if err := session.EncodeEnvelope(st, session.Envelope{
		Version: session.EnvelopeVersion,
		CapID:   CapID,
		MsgType: MsgFrameHeader,
		Payload: payload,
	}); err != nil {
		return err
	}
	_, err = st.Write(f.Data)
	return err
}

// stopSharing ends a session this device was sourcing.
func (c *Capability) stopSharing(peer identity.DeviceID) {
	c.mu.Lock()
	s := c.sending[peer]
	delete(c.sending, peer)
	c.mu.Unlock()
	if s == nil {
		return
	}

	s.cancel()
	if s.release != nil {
		s.release()
	}

	s.mu.Lock()
	sent, stale := s.sent, s.stale
	s.mu.Unlock()
	c.cfg.Logf("mirror: stopped sharing with %s (%d frames, %d abandoned)",
		peer.Fingerprint(), sent, stale)
	if c.cfg.OnStop != nil {
		c.cfg.OnStop(peer)
	}
}

// StopSharingWith ends a session this device was sourcing, from the outside.
// It is what a local user's kill calls: §6.3 puts the enforcement here rather
// than in the message.
func (c *Capability) StopSharingWith(peer identity.DeviceID) { c.stopSharing(peer) }

// requestIDR answers §14.1's RequestIDR: a sink that lost frames needs a
// picture it can decode on its own.
func (c *Capability) requestIDR(peer identity.DeviceID) error {
	c.mu.Lock()
	s := c.sending[peer]
	c.mu.Unlock()
	if s == nil {
		return nil
	}
	if err := s.capture.RequestKeyframe(); err != nil {
		// Not fatal: the next scheduled keyframe still arrives, and refusing
		// the session over it would turn a recoverable stutter into a stop.
		c.cfg.Logf("mirror: keyframe request refused: %v", err)
	}
	return nil
}

// setBitrate applies §14.1's BitrateHint. D-84 makes this load-bearing rather
// than advisory: a source that ignores it abandons more than half its frames on
// a relayed path.
func (c *Capability) setBitrate(peer identity.DeviceID, bytesPerSec int) error {
	if bytesPerSec <= 0 {
		return nil
	}
	c.mu.Lock()
	s := c.sending[peer]
	c.mu.Unlock()
	if s == nil {
		return nil
	}

	s.mu.Lock()
	previous := s.bitrate
	s.bitrate = bytesPerSec
	s.mu.Unlock()

	if err := s.capture.SetBitrate(bytesPerSec); err != nil {
		return fmt.Errorf("mirror: setting bitrate: %w", err)
	}
	c.cfg.Logf("mirror: bitrate %d -> %d bytes/sec at %s's request",
		previous, bytesPerSec, peer.Fingerprint())
	return nil
}

// Sharing reports which peers this device is currently sharing its screen with,
// which is what an indicator shows.
func (c *Capability) Sharing() []identity.DeviceID {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]identity.DeviceID, 0, len(c.sending))
	for peer := range c.sending {
		out = append(out, peer)
	}
	return out
}

// StopAll ends every session in both directions, which is what a local user's
// kill and a daemon shutdown both need.
func (c *Capability) StopAll() {
	c.mu.Lock()
	sources := make([]identity.DeviceID, 0, len(c.sending))
	for peer := range c.sending {
		sources = append(sources, peer)
	}
	watchers := make([]*watchSession, 0, len(c.watching))
	for _, w := range c.watching {
		watchers = append(watchers, w)
	}
	c.mu.Unlock()

	for _, peer := range sources {
		c.stopSharing(peer)
	}
	for _, w := range watchers {
		c.StopWatching(context.Background(), w.sess)
	}
}

var errNoCapturer = errors.New("mirror: this device has no screen capture backend")
