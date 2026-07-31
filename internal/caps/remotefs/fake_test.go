package remotefs

import (
	"context"
	"errors"
	"io"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/caps"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

// This package is written against session.Session and never touches quic-go,
// so the whole capability can be exercised over in-memory pipes. What that
// cannot cover is §6's gate, which lives in the session layer -- the daemon
// tests carry that half.

var _ caps.Capability = (*Capability)(nil)

// pipeStream is one end of a bidirectional stream, over a pair of io.Pipes.
//
// io.Pipe rather than net.Pipe for one reason: CloseWithError. A reset carries
// a §10 code, and the client is supposed to be able to tell UNAUTHORISED from
// NOT_FOUND -- a fake that closed without one would let a test pass while the
// real refusal was unreadable.
type pipeStream struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (s pipeStream) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s pipeStream) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s pipeStream) Close() error                { return s.w.Close() }

func (s pipeStream) Reset(code uint32) {
	err := &session.ProtocolError{Code: session.ErrorCode(code), Msg: "stream reset"}
	s.w.CloseWithError(err)
	s.r.CloseWithError(err)
}

// newStreamPair returns the two ends of one bidirectional stream.
func newStreamPair() (pipeStream, pipeStream) {
	ar, aw := io.Pipe()
	br, bw := io.Pipe()
	return pipeStream{r: ar, w: bw}, pipeStream{r: br, w: aw}
}

// fakeSession is the client end. OpenStream hands the far end to the source's
// ServeStream on its own goroutine, which is what the session layer does in
// production once it has read the opening envelope.
type fakeSession struct {
	t      *testing.T
	source *Capability
	peer   identity.Peer

	// resetWith, if set, resets every inbound stream with this code instead of
	// serving it: the §6 refusal, as a client sees it.
	resetWith session.ErrorCode
}

func (s *fakeSession) Peer() identity.Peer        { return s.peer }
func (s *fakeSession) SendDatagram([]byte) error  { return errors.New("fake: no datagrams") }
func (s *fakeSession) PathInfo() session.PathInfo { return session.PathInfo{Class: "lan"} }
func (s *fakeSession) Close(uint16, string) error { return nil }
func (s *fakeSession) Done() <-chan struct{}      { return nil }
func (s *fakeSession) Quiesce(context.Context, uint32, string) (func(), error) {
	return func() {}, nil
}

func (s *fakeSession) Send(context.Context, byte, uint16, proto.Message) error {
	return errors.New("fake: remotefs sends nothing on the control stream")
}

func (s *fakeSession) OpenStream(ctx context.Context) (session.Stream, error) {
	near, far := newStreamPair()
	go func() {
		st := far
		env, err := session.DecodeEnvelope(st)
		if err != nil {
			st.Reset(uint32(session.CodeProtocolViolation))
			return
		}
		if s.resetWith != 0 {
			st.Reset(uint32(s.resetWith))
			return
		}
		if err := s.source.ServeStream(ctx, s, st, env.MsgType, env.Payload); err != nil {
			code, ok := session.ErrorCodeOf(err)
			if !ok {
				code = session.CodeResourceExhausted
			}
			st.Reset(uint32(code))
		}
	}()
	return near, nil
}

// newPair returns a client capability and a session pointed at a source
// sharing roots.
func newPair(t *testing.T, cfg Config) (*Capability, session.Session) {
	t.Helper()
	source := New(cfg)
	client := New(Config{})
	return client, &fakeSession{t: t, source: source, peer: identity.Peer{DeviceID: "aaaaaaaaaaaaaaaa"}}
}
