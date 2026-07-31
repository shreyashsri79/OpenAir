package conn

import (
	"context"

	"github.com/apernet/quic-go"

	"github.com/shreyashsri79/openair/internal/congestion"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

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
type listener struct {
	ln          *quic.Listener
	local       identity.Identity
	displayName string
	platform    string
	handlers    map[byte]session.Handler
	authorize   func(identity.Peer) error
}

// Listen binds addr (use ":0" for an ephemeral port) and returns a Listener
// that hands accepted sessions the given display name, platform and
// capability handlers.
//
// authorize gates inbound peers once Hello identifies them; passing nil
// accepts every caller (session.Config.Authorize explains when that is
// acceptable).
func Listen(addr string, local identity.Identity, displayName, platform string, handlers map[byte]session.Handler, authorize func(identity.Peer) error) (Listener, error) {
	tlsConf, err := local.TLSConfig(nil)
	if err != nil {
		return nil, err
	}

	ln, err := quic.ListenAddr(addr, tlsConf, quicConfig())
	if err != nil {
		return nil, err
	}

	return &listener{
		ln:          ln,
		local:       local,
		displayName: displayName,
		platform:    platform,
		handlers:    handlers,
		authorize:   authorize,
	}, nil
}

// Accept waits for and returns the next inbound session.
func (l *listener) Accept(ctx context.Context) (session.Session, error) {
	qc, err := l.ln.Accept(ctx)
	if err != nil {
		return nil, err
	}

	// Same requirement as the dial side: install BBR only once the handshake
	// (which Accept already waited for) has completed (D-14, D-16).
	congestion.Use(qc)

	return session.New(ctx, qc, session.Config{
		Local:       l.local,
		DisplayName: l.displayName,
		Platform:    l.platform,
		Handlers:    l.handlers,
		Initiator:   false,
		Authorize:   l.authorize,
	})
}

// Addr reports the actual bound address, including the concrete port chosen
// when Listen was called with ":0".
func (l *listener) Addr() string {
	return l.ln.Addr().String()
}

// Close stops accepting new connections and unblocks any pending Accept.
func (l *listener) Close() error {
	return l.ln.Close()
}
