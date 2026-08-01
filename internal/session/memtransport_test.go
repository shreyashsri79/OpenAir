package session

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"sync"

	"github.com/shreyashsri79/openair/internal/identity"
)

// This file is the in-memory stand-in for QUIC. The Hello exchange and control
// loop are tested over it rather than over a real connection: the over-QUIC
// integration test belongs to M1e, and internal/conn is written concurrently.
//
// net.Pipe is not usable here. It is synchronous and unbuffered, so §4's "both
// peers send Hello" -- each side writing before either reads -- deadlocks on it.
// These pipes buffer without bound instead.

// Several tests deliberately drive the session into conditions it logs about --
// ignored messages, failing handlers. Those warnings are the expected outcome,
// not a signal, so keep them out of the test output.
func init() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
}

// memBuf is a one-directional byte pipe with an unbounded buffer.
type memBuf struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	closed bool
}

func newMemBuf() *memBuf {
	b := &memBuf{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *memBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	n, err := b.buf.Write(p)
	b.cond.Broadcast()
	return n, err
}

func (b *memBuf) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.buf.Len() == 0 {
		if b.closed {
			return 0, io.EOF
		}
		b.cond.Wait()
	}
	return b.buf.Read(p)
}

func (b *memBuf) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.cond.Broadcast()
}

// memStream is one end of a bidirectional in-memory stream.
//
// A reset is delivered to the *other* end, the way RESET_STREAM /
// STOP_SENDING are: resetCode is what this end has been told, not what it sent.
type memStream struct {
	r, w      *memBuf
	closeOnce sync.Once
	resetCode chan uint32 // codes the peer reset us with
	peerReset chan uint32 // where our own Reset is delivered
}

func memStreamPair() (*memStream, *memStream) {
	x, y := newMemBuf(), newMemBuf()
	toA := make(chan uint32, 1)
	toB := make(chan uint32, 1)
	a := &memStream{r: x, w: y, resetCode: toA, peerReset: toB}
	b := &memStream{r: y, w: x, resetCode: toB, peerReset: toA}
	return a, b
}

func (s *memStream) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *memStream) Write(p []byte) (int, error) { return s.w.Write(p) }

func (s *memStream) Close() error {
	s.closeOnce.Do(func() { s.w.close() })
	return nil
}

func (s *memStream) Reset(code uint32) {
	select {
	case s.peerReset <- code:
	default:
	}
	s.w.close()
	s.r.close()
}

// memTransport is one side of a connected pair.
type memTransport struct {
	peerKey ed25519.PublicKey
	keyErr  error

	incoming chan Stream // streams the peer opened towards us
	outgoing chan Stream // the peer's incoming, so OpenStream can deliver

	datagramsIn  chan []byte // datagrams the peer sent us
	datagramsOut chan []byte // the peer's datagramsIn, so SendDatagram can deliver

	path PathInfo

	mu        sync.Mutex
	closed    bool
	closeCode ErrorCode
	closeMsg  string
}

func memTransportPair(aKey, bKey ed25519.PublicKey) (*memTransport, *memTransport) {
	toA := make(chan Stream, 8)
	toB := make(chan Stream, 8)
	dgA := make(chan []byte, 64)
	dgB := make(chan []byte, 64)
	// A sees B's key as the peer key, and vice versa.
	a := &memTransport{peerKey: bKey, incoming: toA, outgoing: toB, datagramsIn: dgA, datagramsOut: dgB, path: PathInfo{RTTMillis: 3, Class: "lan"}}
	b := &memTransport{peerKey: aKey, incoming: toB, outgoing: toA, datagramsIn: dgB, datagramsOut: dgA, path: PathInfo{RTTMillis: 3, Class: "lan"}}
	return a, b
}

func (t *memTransport) OpenStream(ctx context.Context) (Stream, error) {
	mine, theirs := memStreamPair()
	select {
	case t.outgoing <- theirs:
		return mine, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *memTransport) AcceptStream(ctx context.Context) (Stream, error) {
	select {
	case st := <-t.incoming:
		return st, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *memTransport) SendDatagram(b []byte) error {
	if t.datagramsOut == nil {
		return nil
	}
	// Datagrams are droppable by definition, and a full queue is the one
	// behaviour a test of drop-stale semantics needs to be able to produce.
	select {
	case t.datagramsOut <- append([]byte(nil), b...):
	default:
	}
	return nil
}

func (t *memTransport) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	if t.datagramsIn == nil {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	select {
	case b := <-t.datagramsIn:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *memTransport) PeerPublicKey() (ed25519.PublicKey, error) {
	if t.keyErr != nil {
		return nil, t.keyErr
	}
	return t.peerKey, nil
}

func (t *memTransport) PathInfo() PathInfo { return t.path }

func (t *memTransport) CloseWithError(code ErrorCode, reason string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	t.closeCode = code
	t.closeMsg = reason
	return nil
}

func (t *memTransport) closedWith() (bool, ErrorCode, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed, t.closeCode, t.closeMsg
}

// --- a stub identity ---------------------------------------------------------

type stubIdentity struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	tier identity.ProtectionTier
}

func newStubIdentity(seed byte) *stubIdentity {
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = seed
	}
	priv := ed25519.NewKeyFromSeed(s)
	return &stubIdentity{
		pub:  priv.Public().(ed25519.PublicKey),
		priv: priv,
		tier: identity.TierKeystore,
	}
}

// publicKey is how the test harness reads a local identity's TLS key without
// asserting on the concrete type, so a wrapper that adds a privilege key (see
// auth_test.go) still works with pair.
func (i *stubIdentity) publicKey() ed25519.PublicKey { return i.pub }

func (i *stubIdentity) DeviceID() identity.DeviceID             { return identity.DeriveDeviceID(i.pub) }
func (i *stubIdentity) IdentityPublic() ed25519.PublicKey       { return i.pub }
func (i *stubIdentity) PrivilegePublic() ed25519.PublicKey      { return i.pub }
func (i *stubIdentity) ProtectionTier() identity.ProtectionTier { return i.tier }

func (i *stubIdentity) TLSConfig(pinned ed25519.PublicKey) (*tls.Config, error) {
	return nil, errors.New("stubIdentity: no TLS")
}

func (i *stubIdentity) SignOwned(target identity.DeviceID, capID byte, msgType uint16) ([]byte, int64, []byte, error) {
	return nil, 0, nil, errors.New("stubIdentity: no privilege key")
}

var _ identity.Identity = (*stubIdentity)(nil)

// liarIdentity claims a DeviceID that its key does not derive -- §4's
// "claiming an identity it cannot prove".
type liarIdentity struct {
	*stubIdentity
	claim identity.DeviceID
}

func (i *liarIdentity) DeviceID() identity.DeviceID { return i.claim }

// --- a recording handler -----------------------------------------------------

type recordedMsg struct {
	MsgType uint16
	Payload []byte
	Stream  bool
}

type recordingHandler struct {
	capID byte

	mu   sync.Mutex
	msgs []recordedMsg
	got  chan recordedMsg

	// serveErr, if set, is returned by Serve for msgType == errOnType.
	serveErr  error
	errOnType uint16

	// streamHook runs inside ServeStream, after the opening envelope.
	streamHook func(st Stream)
}

func newRecordingHandler(capID byte) *recordingHandler {
	return &recordingHandler{capID: capID, got: make(chan recordedMsg, 16)}
}

func (h *recordingHandler) CapID() byte { return h.capID }

func (h *recordingHandler) RequiredLevel() identity.TrustLevel { return identity.LevelTrusted }

func (h *recordingHandler) Serve(ctx context.Context, sess Session, msgType uint16, payload []byte) error {
	h.record(recordedMsg{MsgType: msgType, Payload: payload})
	if h.serveErr != nil && msgType == h.errOnType {
		return h.serveErr
	}
	return nil
}

func (h *recordingHandler) ServeStream(ctx context.Context, sess Session, st Stream, msgType uint16, payload []byte) error {
	h.record(recordedMsg{MsgType: msgType, Payload: payload, Stream: true})
	if h.streamHook != nil {
		h.streamHook(st)
	}
	return nil
}

func (h *recordingHandler) record(m recordedMsg) {
	h.mu.Lock()
	h.msgs = append(h.msgs, m)
	h.mu.Unlock()
	select {
	case h.got <- m:
	default:
	}
}

func (h *recordingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.msgs)
}
