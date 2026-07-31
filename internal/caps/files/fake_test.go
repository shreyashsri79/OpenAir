package files

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
)

// This package is written against session.Session and session.Stream and never
// touches quic-go, which is what keeps it path-agnostic (D-6). The consequence
// is that everything here can be exercised over an in-memory link.

var _ caps.Capability = (*Capability)(nil)

// ---------------------------------------------------------------- pipe

// halfPipe carries bytes one way with a bounded buffer, so a sender that
// outruns its receiver blocks rather than allocating the whole transfer.
//
// chunkRead and chunkWrite make it return fewer bytes than asked for. A
// session.Stream is an interface any transport may implement, and a chunk
// engine that assumes full reads and writes corrupts files under exactly the
// conditions that are hardest to reproduce -- so the tests make them the
// default case, not the exotic one.
type halfPipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
	err    error

	cap        int
	chunkRead  int
	chunkWrite int

	limit   int64 // bytes accepted before the pipe breaks; 0 = unlimited
	written int64
}

func newHalfPipe() *halfPipe {
	p := &halfPipe{cap: 1 << 20}
	p.cond = sync.NewCond(&p.mu)
	return p
}

var errPipeBroken = errors.New("fake: pipe broken")

func (p *halfPipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		if p.err != nil {
			return 0, p.err
		}
		if p.closed {
			return 0, io.ErrClosedPipe
		}
		if len(p.buf) < p.cap {
			break
		}
		p.cond.Wait()
	}
	n := len(b)
	if room := p.cap - len(p.buf); n > room {
		n = room
	}
	if p.chunkWrite > 0 && n > p.chunkWrite {
		n = p.chunkWrite
	}
	if p.limit > 0 && p.written+int64(n) > p.limit {
		n = int(p.limit - p.written)
		p.buf = append(p.buf, b[:n]...)
		p.written += int64(n)
		p.err = errPipeBroken
		p.cond.Broadcast()
		return n, errPipeBroken
	}
	p.buf = append(p.buf, b[:n]...)
	p.written += int64(n)
	p.cond.Broadcast()
	// A short return with a nil error is legal for io.Writer only in the
	// company of an error, but nothing stops an implementation doing it and
	// writeFull must survive it.
	return n, nil
}

func (p *halfPipe) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.buf) == 0 {
		if p.err != nil {
			return 0, p.err
		}
		if p.closed {
			return 0, io.EOF
		}
		p.cond.Wait()
	}
	n := len(p.buf)
	if n > len(b) {
		n = len(b)
	}
	if p.chunkRead > 0 && n > p.chunkRead {
		n = p.chunkRead
	}
	copy(b[:n], p.buf[:n])
	p.buf = p.buf[n:]
	if len(p.buf) == 0 {
		p.buf = p.buf[:0:0]
	}
	p.cond.Broadcast()
	return n, nil
}

func (p *halfPipe) closeWrite() {
	p.mu.Lock()
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *halfPipe) fail(err error) {
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	p.cond.Broadcast()
	p.mu.Unlock()
}

// ---------------------------------------------------------------- stream

type fakeStream struct {
	out *halfPipe
	in  *halfPipe
}

func (s *fakeStream) Read(b []byte) (int, error)  { return s.in.Read(b) }
func (s *fakeStream) Write(b []byte) (int, error) { return s.out.Write(b) }
func (s *fakeStream) Close() error                { s.out.closeWrite(); return nil }
func (s *fakeStream) Reset(uint32) {
	s.out.fail(errPipeBroken)
	s.in.fail(errPipeBroken)
}

// newStreamPair returns the two ends of one bidirectional stream.
func newStreamPair() (*fakeStream, *fakeStream) {
	a, b := newHalfPipe(), newHalfPipe()
	return &fakeStream{out: a, in: b}, &fakeStream{out: b, in: a}
}

// ---------------------------------------------------------------- session

type sentMsg struct {
	capID   byte
	msgType uint16
	payload []byte
}

// fakeSession is one end of a link. Send hands the message to the peer's
// inbox, which a single goroutine drains -- the real control stream is a single
// ordered channel served by one loop, and modelling that is what keeps the
// tests honest about reentrancy.
type fakeSession struct {
	t     *testing.T
	peer  identity.Peer
	local *Capability

	remote *fakeSession
	inbox  chan sentMsg

	// tuning applied to every data stream this session opens
	chunkRead  int
	chunkWrite int
	limit      int64

	mu     sync.Mutex
	sent   []sentMsg
	closed bool
	wg     sync.WaitGroup
	seen   chan struct{}
}

func (s *fakeSession) Peer() identity.Peer       { return s.peer }
func (s *fakeSession) SendDatagram([]byte) error { return errors.New("fake: no datagrams") }
func (s *fakeSession) PathInfo() session.PathInfo {
	return session.PathInfo{Class: "lan"}
}
func (s *fakeSession) Quiesce(context.Context, uint32, string) (func(), error) {
	return func() {}, nil
}
func (s *fakeSession) Close(uint16, string) error { return nil }

// Done never fires: a fake session ends when the test does.
func (s *fakeSession) Done() <-chan struct{} { return nil }

func (s *fakeSession) Send(ctx context.Context, capID byte, msgType uint16, msg proto.Message) error {
	b, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	m := sentMsg{capID: capID, msgType: msgType, payload: b}

	s.mu.Lock()
	s.sent = append(s.sent, m)
	closed := s.closed
	if s.seen != nil {
		close(s.seen)
		s.seen = nil
	}
	s.mu.Unlock()
	if closed {
		return errors.New("fake: session closed")
	}
	if s.remote == nil {
		return nil
	}
	select {
	case s.remote.inbox <- m:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *fakeSession) OpenStream(ctx context.Context) (session.Stream, error) {
	near, far := newStreamPair()
	near.out.chunkWrite = s.chunkWrite
	far.in.chunkRead = s.chunkRead
	near.out.limit = s.limit
	if s.remote == nil {
		return near, nil
	}
	s.remote.wg.Add(1)
	go func() {
		defer s.remote.wg.Done()
		s.remote.serveStream(far)
	}()
	return near, nil
}

// serveStream is the session layer's job in production: read the opening
// envelope off a capability stream, then hand the stream to the capability
// (PROTOCOL.md §3, §8.2).
func (s *fakeSession) serveStream(st *fakeStream) {
	env, err := session.DecodeEnvelope(st)
	if err != nil {
		return
	}
	if env.CapID != CapID {
		return
	}
	_ = s.local.ServeStream(context.Background(), s, st, env.MsgType, env.Payload)
}

// run drains the inbox into the local capability, one message at a time.
func (s *fakeSession) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-s.inbox:
			if !ok {
				return
			}
			if m.capID != CapID {
				continue
			}
			if err := s.local.Serve(ctx, s, m.msgType, m.payload); err != nil {
				s.t.Logf("serve msgType %d: %v", m.msgType, err)
			}
		}
	}
}

// awaitSent blocks until this session has sent a message of the given type,
// and returns the first one.
func (s *fakeSession) awaitSent(t *testing.T, msgType uint16, within time.Duration) sentMsg {
	t.Helper()
	return s.awaitSentNth(t, msgType, 1, within)
}

// awaitSentNth waits for the nth (1-based) message of a type. Transfers that
// are re-offered send more than one of several message types, and taking the
// first would silently assert about the wrong one.
func (s *fakeSession) awaitSentNth(t *testing.T, msgType uint16, n int, within time.Duration) sentMsg {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		seen := 0
		for _, m := range s.sent {
			if m.msgType != msgType {
				continue
			}
			seen++
			if seen == n {
				s.mu.Unlock()
				return m
			}
		}
		s.seen = make(chan struct{})
		ch := s.seen
		s.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("fewer than %d messages of type %d within %s", n, msgType, within)
	return sentMsg{}
}

func (s *fakeSession) sentCount(msgType uint16) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.sent {
		if m.msgType == msgType {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------- link

type link struct {
	sendCap  *Capability
	recvCap  *Capability
	sendSess *fakeSession
	recvSess *fakeSession
	cancel   context.CancelFunc
}

type linkOpts struct {
	sendCfg    Config
	recvCfg    Config
	chunkRead  int
	chunkWrite int
	limit      int64
}

func newLink(t *testing.T, o linkOpts) *link {
	t.Helper()
	sendCap := New(o.sendCfg)
	recvCap := New(o.recvCfg)

	owned := identity.Peer{DeviceID: "peer0000000000aa", Level: identity.LevelOwned}
	a := &fakeSession{t: t, peer: owned, local: sendCap, inbox: make(chan sentMsg, 64)}
	b := &fakeSession{t: t, peer: owned, local: recvCap, inbox: make(chan sentMsg, 64)}
	a.remote, b.remote = b, a
	a.chunkRead, a.chunkWrite, a.limit = o.chunkRead, o.chunkWrite, o.limit
	b.chunkRead, b.chunkWrite = o.chunkRead, o.chunkWrite

	ctx, cancel := context.WithCancel(context.Background())
	go a.run(ctx)
	go b.run(ctx)

	l := &link{sendCap: sendCap, recvCap: recvCap, sendSess: a, recvSess: b, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		a.wg.Wait()
		b.wg.Wait()
	})
	return l
}
