package conn

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/apernet/quic-go"

	"github.com/shreyashsri79/openair/internal/congestion"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

// maxPendingHandshakes bounds how many inbound connections may be between the
// QUIC handshake and a completed Hello at once.
//
// It is a memory bound, not a throughput one: each pending handshake holds an
// accepted connection and a goroutine, and an attacker who can complete QUIC
// handshakes and then say nothing would otherwise accumulate both without
// limit. Past the bound, new connections wait in the kernel until a slot frees
// or their own idle timeout fires.
const maxPendingHandshakes = 32

// handshakeTimeout bounds one inbound peer's Hello exchange (PROTOCOL.md §4).
//
// A peer that completes the QUIC handshake and then sends nothing is either
// broken or probing; either way it must not hold a slot until quic-go's much
// longer idle timeout expires.
const handshakeTimeout = 10 * time.Second

// HandshakeError is one inbound connection that did not become a session --
// a refused peer, a key mismatch, a Hello that never arrived.
//
// It is a distinct type because it is not fatal to the listener: a daemon that
// stops accepting because one peer was refused has been denied service by any
// stranger who can reach its port. Callers that treat every Accept error as
// terminal are the reason this is worth naming.
type HandshakeError struct {
	Remote string
	Err    error
}

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("handshake with %s: %v", e.Remote, e.Err)
}

func (e *HandshakeError) Unwrap() error { return e.Err }

// listener is the Phase 1 implementation of Listener: it accepts inbound
// QUIC connections on one bound address and hands each to session.New.
//
// Accept does not know in advance which peer is dialling in, so the TLS
// config is built once at Listen time with pinned = nil (pairing mode,
// identity.Identity.TLSConfig's documented meaning for "no key pinned yet",
// PROTOCOL.md §5). Nothing at the TLS layer therefore restricts who may
// connect, and the only gate on an inbound peer is the authorize callback,
// which session.New invokes once Hello has revealed the caller's DeviceID.
//
// A nil authorize admits every caller. See session.Config.Authorize: that is
// M1's stated scope and not a permanent arrangement.
//
// Hello runs on its own goroutine per connection, not on the accept path. That
// is M4's requirement rather than a refinement: the daemon has no second
// process to fall back on, and with an inline Hello one peer that connects and
// stays silent stops every other device from reaching this one.
type listener struct {
	ln          *quic.Listener
	local       identity.Identity
	displayName string
	platform    string
	handlers    map[byte]session.Handler
	opts        ListenOptions

	start   sync.Once
	results chan acceptResult
	ctx     context.Context
	cancel  context.CancelFunc
	slots   chan struct{}
}

type acceptResult struct {
	sess session.Session
	err  error
}

// ListenOptions carries the decisions a listener cannot make for itself.
//
// It is a struct rather than three more parameters because two of the three
// arrived with M6 and a fourth is foreseeable: an accepting side has to be told
// who may connect, what the trust store says about them once they have, and
// where to log the answer.
type ListenOptions struct {
	// Authorize gates inbound peers once Hello identifies them; nil accepts
	// every caller (session.Config.Authorize explains when that is acceptable).
	Authorize func(identity.Peer) error

	// PeerLookup supplies the stored trust-store record, which is where the
	// pinned privilege key and the granted trust level come from (§6, D-20).
	// Nil means the session enforces no trust ladder of its own; see
	// session.Config.PeerLookup, which explains when that is right.
	PeerLookup func(identity.DeviceID) (identity.Peer, bool)

	// OnAuthEvent receives every authorisation decision made for an inbound
	// message, for the local session log PRD R4 requires.
	OnAuthEvent func(session.AuthEvent)

	// PathClass reports the class of path a peer is currently reached on
	// (§7.2). ListenPacketConn fills it in from the packet conn when that conn
	// knows; nil leaves the transport's own answer.
	PathClass func(identity.DeviceID) string
}

// Listen binds addr (use ":0" for an ephemeral port) and returns a Listener
// that hands accepted sessions the given display name, platform and
// capability handlers.
func Listen(addr string, local identity.Identity, displayName, platform string, handlers map[byte]session.Handler, opts ListenOptions) (Listener, error) {
	tlsConf, err := local.TLSConfig(nil)
	if err != nil {
		return nil, err
	}

	ln, err := quic.ListenAddr(addr, tlsConf, quicConfig())
	if err != nil {
		return nil, err
	}

	return newListener(ln, local, displayName, platform, handlers, opts), nil
}

// newListener wraps an already-bound quic.Listener. It exists so that a
// listener over a relay's packet conn (relayed.go) is the same listener as one
// over UDP rather than a second implementation of the same accept loop.
func newListener(ln *quic.Listener, local identity.Identity, displayName, platform string, handlers map[byte]session.Handler, opts ListenOptions) Listener {
	ctx, cancel := context.WithCancel(context.Background())
	return &listener{
		ln:          ln,
		local:       local,
		displayName: displayName,
		platform:    platform,
		handlers:    handlers,
		opts:        opts,
		results:     make(chan acceptResult),
		ctx:         ctx,
		cancel:      cancel,
		slots:       make(chan struct{}, maxPendingHandshakes),
	}
}

// Accept waits for and returns the next inbound session.
//
// An error that is a *HandshakeError concerns one peer and leaves the listener
// running; anything else is the listener itself failing and will repeat.
func (l *listener) Accept(ctx context.Context) (session.Session, error) {
	l.start.Do(func() { go l.pump() })

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-l.results:
		return r.sess, r.err
	case <-l.ctx.Done():
		// Closed. The pump may be trying to deliver its own error at this
		// moment; racing it would make Close's effect on a blocked Accept
		// depend on which select won, so the closed listener answers here.
		return nil, ErrListenerClosed
	}
}

// ErrListenerClosed is what a blocked Accept returns once Close has been
// called.
var ErrListenerClosed = errors.New("conn: listener closed")

// pump owns the QUIC accept loop for the life of the listener. It runs
// independently of any caller's context so that a cancelled Accept does not
// abandon a handshake already in progress.
func (l *listener) pump() {
	for {
		qc, err := l.ln.Accept(l.ctx)
		if err != nil {
			l.deliver(acceptResult{err: err})
			return
		}

		select {
		case l.slots <- struct{}{}:
		case <-l.ctx.Done():
			qc.CloseWithError(quic.ApplicationErrorCode(session.CodeResourceExhausted), "shutting down")
			return
		}

		go func() {
			defer func() { <-l.slots }()
			l.handshake(qc)
		}()
	}
}

// handshake completes Hello for one inbound connection and delivers the result.
func (l *listener) handshake(qc *quic.Conn) {
	// Same requirement as the dial side: install BBR only once the handshake
	// (which Accept already waited for) has completed (D-14, D-16).
	congestion.Use(qc)

	ctx, cancel := context.WithTimeout(l.ctx, handshakeTimeout)
	defer cancel()

	sess, err := session.New(ctx, qc, session.Config{
		Local:       l.local,
		DisplayName: l.displayName,
		Platform:    l.platform,
		Handlers:    l.handlers,
		Initiator:   false,
		Authorize:   l.opts.Authorize,
		PeerLookup:  l.opts.PeerLookup,
		OnAuthEvent: l.opts.OnAuthEvent,
		PathClass:   l.opts.PathClass,
	})
	if err != nil {
		// A refusal that is not a protocol error is the authorize callback
		// turning an unpaired peer away (M2), and NOT_PAIRED is what §10 has
		// for that. Reporting PROTOCOL_VIOLATION instead would tell the peer it
		// had malformed something, and send its user looking in the wrong
		// place.
		code := session.CodeNotPaired
		if c, ok := session.ErrorCodeOf(err); ok {
			code = c
		}
		qc.CloseWithError(quic.ApplicationErrorCode(code), code.String())
		l.deliver(acceptResult{err: &HandshakeError{Remote: qc.RemoteAddr().String(), Err: err}})
		return
	}
	l.deliver(acceptResult{sess: sess})
}

// deliver hands a result to whoever is in Accept, or drops it if the listener
// is closing. A session nobody accepts is closed rather than leaked.
func (l *listener) deliver(r acceptResult) {
	select {
	case l.results <- r:
	case <-l.ctx.Done():
		if r.sess != nil {
			_ = r.sess.Close(uint16(session.CodeNoError), "listener closed")
		}
	}
}

// Addr reports the actual bound address, including the concrete port chosen
// when Listen was called with ":0".
func (l *listener) Addr() string {
	return l.ln.Addr().String()
}

// Close stops accepting new connections and unblocks any pending Accept.
func (l *listener) Close() error {
	l.cancel()
	return l.ln.Close()
}
