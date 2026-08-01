package mirror

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/caps"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// M15's design is one sentence — a frame that has been overtaken is abandoned
// rather than finished — so these tests are mostly about what does *not* get
// sent, and about the two levers a sink has over a source.

var _ caps.Capability = (*Capability)(nil)

// fakeCapturer produces frames on demand rather than on a clock, so a test
// controls exactly when the next one is ready.
type fakeCapturer struct {
	frames chan Frame

	mu        sync.Mutex
	keyframes int
	bitrate   int
	started   bool
}

func newFakeCapturer() *fakeCapturer {
	return &fakeCapturer{frames: make(chan Frame, 16)}
}

func (f *fakeCapturer) Start(ctx context.Context, opts CaptureOptions) (<-chan Frame, error) {
	f.mu.Lock()
	f.started = true
	f.bitrate = opts.Bitrate
	f.mu.Unlock()
	return f.frames, nil
}

func (f *fakeCapturer) RequestKeyframe() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keyframes++
	return nil
}

func (f *fakeCapturer) SetBitrate(bytesPerSec int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bitrate = bytesPerSec
	return nil
}

func (f *fakeCapturer) state() (keyframes, bitrate int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keyframes, f.bitrate
}

// recordingStream is one frame stream, and remembers whether it was reset.
type recordingStream struct {
	mu      sync.Mutex
	written []byte
	closed  bool
	reset   bool
	code    uint32

	// block, when set, holds the first Write until it is closed, which is how
	// a test produces the "still sending when the next frame is ready" case
	// that §14.2 exists for.
	block chan struct{}
}

func (s *recordingStream) Read([]byte) (int, error) { return 0, io.EOF }

func (s *recordingStream) Write(p []byte) (int, error) {
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reset {
		return 0, errors.New("stream reset")
	}
	s.written = append(s.written, p...)
	return len(p), nil
}

func (s *recordingStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *recordingStream) Reset(code uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reset = true
	s.code = code
	if s.block != nil {
		select {
		case <-s.block:
		default:
			close(s.block)
		}
	}
}

// done reports that the frame was fully written and the stream closed.
func (s *recordingStream) done() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *recordingStream) wasReset() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reset
}

// fakeSession records what a capability sent.
type fakeSession struct {
	peer identity.Peer

	mu       sync.Mutex
	streams  []*recordingStream
	messages []sentMessage
	blockNth int // the (1-based) stream that should block on write
	quiesced bool
}

type sentMessage struct {
	capID   byte
	msgType uint16
	msg     proto.Message
}

func (s *fakeSession) Peer() identity.Peer        { return s.peer }
func (s *fakeSession) SendDatagram([]byte) error  { return errors.New("mirror sends no datagrams") }
func (s *fakeSession) PathInfo() session.PathInfo { return session.PathInfo{Class: "lan"} }
func (s *fakeSession) Close(uint16, string) error { return nil }
func (s *fakeSession) Done() <-chan struct{}      { return nil }

func (s *fakeSession) Quiesce(context.Context, uint32, string) (func(), error) {
	s.mu.Lock()
	s.quiesced = true
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.quiesced = false
		s.mu.Unlock()
	}, nil
}

func (s *fakeSession) Send(_ context.Context, capID byte, msgType uint16, msg proto.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, sentMessage{capID, msgType, msg})
	return nil
}

func (s *fakeSession) OpenStream(context.Context) (session.Stream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := &recordingStream{}
	if s.blockNth == len(s.streams)+1 {
		st.block = make(chan struct{})
	}
	s.streams = append(s.streams, st)
	return st, nil
}

func (s *fakeSession) sentStreams() []*recordingStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*recordingStream(nil), s.streams...)
}

func (s *fakeSession) sentMessages() []sentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sentMessage(nil), s.messages...)
}

func newSource(t *testing.T, capture *fakeCapturer) (*Capability, *fakeSession) {
	t.Helper()
	c := New(Config{
		Capture: func() (Capturer, error) { return capture, nil },
		Logf:    t.Logf,
	})
	t.Cleanup(c.StopAll)
	return c, &fakeSession{peer: identity.Peer{DeviceID: "aaaaaaaaaaaaaaaa"}}
}

func start(t *testing.T, c *Capability, sess *fakeSession, fps int) {
	t.Helper()
	payload, err := proto.Marshal(&openairv1.MirrorStart{
		Width: 1280, Height: 720, Fps: uint32(fps), Codec: "h264", MaxBitrate: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Serve(context.Background(), sess, MsgStart, payload); err != nil {
		t.Fatalf("start: %v", err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestFramesGoOutOneStreamEachWithTheirHeader is §14.2's shape.
func TestFramesGoOutOneStreamEachWithTheirHeader(t *testing.T) {
	capture := newFakeCapturer()
	c, sess := newSource(t, capture)
	start(t, c, sess, 30)

	// One at a time, waiting for each to finish: two frames handed over at once
	// is the overtaking case, which is TestAnOvertakenFrameIsAbandoned's job.
	capture.frames <- Frame{Data: []byte("first"), Keyframe: true, CapturedAt: time.Now()}
	waitFor(t, "the first frame to be sent", func() bool {
		streams := sess.sentStreams()
		return len(streams) == 1 && streams[0].done()
	})
	capture.frames <- Frame{Data: []byte("second"), CapturedAt: time.Now()}
	waitFor(t, "the second frame to be sent", func() bool {
		streams := sess.sentStreams()
		return len(streams) == 2 && streams[1].done()
	})

	for i, st := range sess.sentStreams() {
		st.mu.Lock()
		written := append([]byte(nil), st.written...)
		st.mu.Unlock()

		env, rest, err := decodeEnvelope(written)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if env.CapID != CapID || env.MsgType != MsgFrameHeader {
			t.Fatalf("frame %d opened with capID %d msgType %d", i, env.CapID, env.MsgType)
		}
		var header openairv1.FrameHeader
		if err := proto.Unmarshal(env.Payload, &header); err != nil {
			t.Fatal(err)
		}
		if header.GetSeq() != uint64(i+1) {
			t.Fatalf("frame %d carries seq %d", i, header.GetSeq())
		}
		if i == 0 && !header.GetKeyframe() {
			t.Fatal("the first frame was not marked as a keyframe")
		}
		if header.GetCapturedAt() == 0 {
			t.Fatal("a frame carries no capture time, so no sink can measure latency")
		}
		if len(rest) == 0 {
			t.Fatalf("frame %d has a header and no picture", i)
		}
	}
}

// TestAnOvertakenFrameIsAbandoned is the milestone's whole design. Without it,
// a frame stuck behind flow control delays every frame after it and the viewer
// falls further behind for as long as the session lasts.
func TestAnOvertakenFrameIsAbandoned(t *testing.T) {
	capture := newFakeCapturer()
	c, sess := newSource(t, capture)
	sess.blockNth = 1 // the first frame's stream stalls mid-write
	start(t, c, sess, 30)

	capture.frames <- Frame{Data: []byte("stuck"), Keyframe: true, CapturedAt: time.Now()}
	waitFor(t, "the first frame to start", func() bool { return len(sess.sentStreams()) == 1 })

	capture.frames <- Frame{Data: []byte("newer"), CapturedAt: time.Now()}
	waitFor(t, "the second frame to go out", func() bool { return len(sess.sentStreams()) == 2 })

	streams := sess.sentStreams()
	if !streams[0].wasReset() {
		t.Fatal("a frame still being sent was not abandoned when a newer one was ready")
	}
	if streams[1].wasReset() {
		t.Fatal("the newest frame was abandoned")
	}
}

// TestMirroringQuiescesBulk is §14.1's automatic quiesce: a screen share and a
// file transfer in the same direction compete, and D-24 decided who yields.
func TestMirroringQuiescesBulk(t *testing.T) {
	capture := newFakeCapturer()
	c, sess := newSource(t, capture)
	start(t, c, sess, 30)

	sess.mu.Lock()
	quiesced := sess.quiesced
	sess.mu.Unlock()
	if !quiesced {
		t.Fatal("a mirror session did not quiesce bulk")
	}

	if err := c.Serve(context.Background(), sess, MsgStop, nil); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitFor(t, "the quiesce to be released", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return !sess.quiesced
	})
}

// TestAKeyframeRequestReachesTheEncoder (§14.1's RequestIDR), which is what a
// sink sends when it has lost enough to be unable to decode.
func TestAKeyframeRequestReachesTheEncoder(t *testing.T) {
	capture := newFakeCapturer()
	c, sess := newSource(t, capture)
	start(t, c, sess, 30)

	if err := c.Serve(context.Background(), sess, MsgRequestIDR, nil); err != nil {
		t.Fatalf("request idr: %v", err)
	}
	if keyframes, _ := capture.state(); keyframes != 1 {
		t.Fatalf("%d keyframe requests reached the encoder, want 1", keyframes)
	}
}

// TestABitrateHintIsApplied. D-84 measured what happens when it is not: on a
// relayed path the source abandons more than half its frames.
func TestABitrateHintIsApplied(t *testing.T) {
	capture := newFakeCapturer()
	c, sess := newSource(t, capture)
	start(t, c, sess, 30)

	payload, err := proto.Marshal(&openairv1.BitrateHint{BytesPerSec: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Serve(context.Background(), sess, MsgBitrateHint, payload); err != nil {
		t.Fatalf("bitrate hint: %v", err)
	}
	if _, bitrate := capture.state(); bitrate != 256<<10 {
		t.Fatalf("the encoder is at %d bytes/sec after a hint of %d", bitrate, 256<<10)
	}
}

// TestADeviceThatDoesNotShareRefusesLegibly. Sharing a screen is off by
// default, and the refusal has to say which of the several possible reasons it
// is.
func TestADeviceThatDoesNotShareRefusesLegibly(t *testing.T) {
	c := New(Config{Logf: t.Logf}) // no Capture
	defer c.StopAll()
	sess := &fakeSession{peer: identity.Peer{DeviceID: "aaaaaaaaaaaaaaaa"}}

	payload, _ := proto.Marshal(&openairv1.MirrorStart{Fps: 30})
	err := c.Serve(context.Background(), sess, MsgStart, payload)
	if err == nil {
		t.Fatal("a device with no capture backend started a mirror session")
	}
	code, ok := session.ErrorCodeOf(err)
	if !ok || code != session.CodeCapabilityUnavailable {
		t.Fatalf("the refusal is %v, which is not CAPABILITY_UNAVAILABLE", err)
	}
	if len(sess.sentStreams()) != 0 {
		t.Fatal("frames were sent by a device that refused to share")
	}
}

// TestAPeerWithNoAnnouncedSessionIsRefused, which is D-82's rule applied to the
// capability that needs it most.
func TestAPeerWithNoAnnouncedSessionIsRefused(t *testing.T) {
	capture := newFakeCapturer()
	c := New(Config{
		Capture: func() (Capturer, error) { return capture, nil },
		Allowed: func(identity.DeviceID) bool { return false },
		Logf:    t.Logf,
	})
	defer c.StopAll()
	sess := &fakeSession{peer: identity.Peer{DeviceID: "aaaaaaaaaaaaaaaa"}}

	payload, _ := proto.Marshal(&openairv1.MirrorStart{Fps: 30})
	err := c.Serve(context.Background(), sess, MsgStart, payload)
	code, ok := session.ErrorCodeOf(err)
	if !ok || code != session.CodeUnauthorised {
		t.Fatalf("mirroring without an announced session returned %v", err)
	}
}

// TestTheOwnedCheckIsOnTheAnnouncementRatherThanEachFrame.
//
// The capability's own level is Trusted, which looks wrong for the most
// invasive thing in the protocol and is not: frames flow source-to-sink, so a
// blanket Owned requirement would demand a proof from the source for its own
// frames, and the party that has to prove something is the sink. The Owned
// proof rides the §6.3 announcement, and a MirrorStart with no announcement
// behind it is refused — which is what TestAPeerWithNoAnnouncedSessionIsRefused
// asserts.
func TestTheOwnedCheckIsOnTheAnnouncementRatherThanEachFrame(t *testing.T) {
	c := New(Config{})
	if c.RequiredLevel() != identity.LevelTrusted {
		t.Fatalf("mirror requires %v; the Owned check belongs on the announcement", c.RequiredLevel())
	}
	if c.CapID() != 0x06 {
		t.Fatalf("mirror is capID %d, want 6 (Appendix B)", c.CapID())
	}
}

// TestTheSinkAsksForLessWhenItFallsBehind. The sink's only lever is
// BitrateHint, and a sink that never pulls it watches a slideshow.
func TestTheSinkAsksForLessWhenItFallsBehind(t *testing.T) {
	w := &watchSession{opts: CaptureOptions{FPS: 30, Bitrate: 1 << 20}}

	interval := time.Second / 30
	if hint := w.suggestBitrate(5 * interval); hint == 0 || hint >= 1<<20 {
		t.Fatalf("a sink five frames behind asked for %d, which is not less than %d", hint, 1<<20)
	}

	// And gives it back when the path recovers, without oscillating straight
	// back to where it started.
	w.opts.Bitrate = 256 << 10
	hint := w.suggestBitrate(interval / 2)
	if hint <= 256<<10 || hint > 1<<20 {
		t.Fatalf("a recovered sink asked for %d", hint)
	}
}

// TestStatsCountWhatArrivedAndWhatDidNot: §14.1's MirrorStats is how a source
// learns that its frames are not landing.
func TestStatsCountWhatArrivedAndWhatDidNot(t *testing.T) {
	w := &watchSession{opts: CaptureOptions{FPS: 30, Bitrate: 1 << 20}}

	w.arrived(1, Frame{Keyframe: true, CapturedAt: time.Now().Add(-20 * time.Millisecond)})
	w.arrived(2, Frame{CapturedAt: time.Now().Add(-25 * time.Millisecond)})
	w.drop(3)

	stats, needIDR, _ := w.take()
	if stats.GetFramesDecoded() != 2 || stats.GetFramesDropped() != 1 {
		t.Fatalf("stats say decoded=%d dropped=%d", stats.GetFramesDecoded(), stats.GetFramesDropped())
	}
	if !needIDR {
		t.Fatal("a sink that lost a frame did not ask for a keyframe")
	}
	if stats.GetDecodeMsP95() == 0 {
		t.Fatal("stats carry no latency, which is the number a source would adapt to")
	}

	// Counters reset per interval: §14.1 sends these at 1 Hz, and a cumulative
	// count would make "is it getting worse" unanswerable.
	next, _, _ := w.take()
	if next.GetFramesDecoded() != 0 || next.GetFramesDropped() != 0 {
		t.Fatal("stats are cumulative rather than per interval")
	}
}

// decodeEnvelope reads one §3 envelope out of a byte slice.
func decodeEnvelope(b []byte) (session.Envelope, []byte, error) {
	if len(b) < session.EnvelopeHeaderSize {
		return session.Envelope{}, nil, errors.New("short envelope")
	}
	env, err := session.DecodeEnvelope(readerOf(b))
	if err != nil {
		return session.Envelope{}, nil, err
	}
	return env, b[session.EnvelopeHeaderSize+len(env.Payload):], nil
}

type sliceReader struct {
	b []byte
	i int
}

func readerOf(b []byte) io.Reader { return &sliceReader{b: b} }

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
