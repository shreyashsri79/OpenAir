package conn

import (
	"context"
	"errors"

	"github.com/apernet/quic-go"

	"github.com/shreyashsri79/openair/internal/congestion"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

// ErrDialNotImplemented is returned by Dial: discovery/rendezvous dialling by
// DeviceID is Phase 2 (PROTOCOL.md §18). Phase 1 callers use DialAddr.
var ErrDialNotImplemented = errors.New("conn: Dial (discovery/rendezvous) is not implemented in Phase 1; use DialAddr")

// dialer is the Phase 1 implementation of Dialer: a plain LAN dial to an
// explicit address, verified against a pinned identity key.
type dialer struct {
	local       identity.Identity
	displayName string
	platform    string
	handlers    map[byte]session.Handler
}

// NewDialer builds a Dialer that dials with local's identity and hands
// accepted sessions the given display name, platform and capability
// handlers (session.Config's non-connection fields).
func NewDialer(local identity.Identity, displayName, platform string, handlers map[byte]session.Handler) Dialer {
	return &dialer{
		local:       local,
		displayName: displayName,
		platform:    platform,
		handlers:    handlers,
	}
}

// DialAddr connects to an explicit host:port, verifying the peer's TLS
// identity key against pinned. See Dialer for the interface contract.
func (d *dialer) DialAddr(ctx context.Context, addr string, pinned identity.Peer) (session.Session, error) {
	tlsConf, err := d.local.TLSConfig(pinned.IdentityPublicKey)
	if err != nil {
		return nil, err
	}

	qc, err := quic.DialAddr(ctx, addr, tlsConf, quicConfig())
	if err != nil {
		return nil, err
	}

	// Must run after the handshake completes (DialAddr already waited for
	// it) and before the connection carries any real traffic: congestion.Use
	// replaces quic-go's default CUBIC sender with OpenAir's BBR (D-14, D-16).
	congestion.Use(qc)

	sess, err := session.New(ctx, qc, session.Config{
		Local:       d.local,
		Peer:        pinned,
		DisplayName: d.displayName,
		Platform:    d.platform,
		Handlers:    d.handlers,
		Initiator:   true,
	})
	if err != nil {
		return nil, translateRemoteClose(err)
	}
	return sess, nil
}

// translateRemoteClose turns "the peer closed the connection with application
// error 0x04" into an error carrying that §10 code.
//
// Without this, a peer's refusal reaches the caller as quic-go's own error
// string and every layer above has to parse prose to find out what happened.
// With it, session.ErrorCodeOf works on a dial failure exactly as it works on a
// local one, and the CLI can say "that device has not paired with this one"
// instead of quoting a transport error at the user.
func translateRemoteClose(err error) error {
	var appErr *quic.ApplicationError
	if !errors.As(err, &appErr) || !appErr.Remote {
		return err
	}
	code := session.ErrorCode(appErr.ErrorCode)
	if code == session.CodeNoError {
		return err
	}
	return &session.ProtocolError{
		Code: code,
		Msg:  "the peer closed the connection",
		Err:  err,
	}
}

// Dial resolves a peer by DeviceID via discovery or rendezvous. Phase 2
// (PROTOCOL.md §18); Phase 1 has no discovery path yet.
func (d *dialer) Dial(ctx context.Context, id identity.DeviceID) (session.Session, error) {
	return nil, ErrDialNotImplemented
}
