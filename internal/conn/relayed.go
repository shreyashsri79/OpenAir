package conn

import (
	"context"
	"fmt"
	"net"

	"github.com/apernet/quic-go"

	"github.com/shreyashsri79/openair/internal/congestion"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

// Relayed paths, M8.
//
// A relayed session is a real QUIC connection, not a tunnel with its own
// semantics. quic-go takes a net.PacketConn and whatever addresses that conn
// hands back, so a relay.PacketConn — which sends frames to a DeviceID instead
// of datagrams to an ip:port — drops in underneath everything above it
// unchanged: the same Hello, the same pinned-key check, the same capabilities.
//
// Two consequences worth stating. Nothing above this layer can tell whether a
// path is relayed, which is what PROTOCOL.md means by the relay being a network
// element rather than a participant. And when M9 adds a direct path (§18), the
// upgrade is QUIC's own connection migration rather than a reconnection, so a
// transfer in progress does not restart.

// DialPacketConn dials a peer over an arbitrary packet conn — in practice a
// relay.PacketConn, addressed by relay.Addr.
//
// It is DialAddr's sibling and does the same three things: TLS pinned to the
// peer's identity key, BBR installed once the handshake is done (D-14, D-16),
// and a session on top.
func DialPacketConn(ctx context.Context, pc net.PacketConn, remote net.Addr, local identity.Identity, displayName, platform string, handlers map[byte]session.Handler, pinned identity.Peer) (session.Session, error) {
	if pc == nil {
		return nil, fmt.Errorf("conn: no packet conn to dial over")
	}
	tlsConf, err := local.TLSConfig(pinned.IdentityPublicKey)
	if err != nil {
		return nil, err
	}

	qc, err := quic.Dial(ctx, pc, remote, tlsConf, quicConfig())
	if err != nil {
		return nil, fmt.Errorf("conn: dial %s over %s: %w", remote, remote.Network(), err)
	}
	congestion.Use(qc)

	sess, err := session.New(ctx, qc, session.Config{
		Local:       local,
		Peer:        pinned,
		DisplayName: displayName,
		Platform:    platform,
		Handlers:    handlers,
		Initiator:   true,
		PathClass:   pathClassOf(pc),
	})
	if err != nil {
		return nil, translateRemoteClose(err)
	}
	return sess, nil
}

// ListenPacketConn accepts inbound sessions arriving over a packet conn.
//
// A device reachable through a relay has to listen on it as well as dial over
// it: the relay is a two-way path, and a device that only dialled would be
// reachable by nobody. The returned Listener behaves exactly like the one over
// UDP, including the off-the-accept-path Hello of D-52.
func ListenPacketConn(pc net.PacketConn, local identity.Identity, displayName, platform string, handlers map[byte]session.Handler, opts ListenOptions) (Listener, error) {
	if pc == nil {
		return nil, fmt.Errorf("conn: no packet conn to listen on")
	}
	tlsConf, err := local.TLSConfig(nil)
	if err != nil {
		return nil, err
	}

	ln, err := quic.Listen(pc, tlsConf, quicConfig())
	if err != nil {
		return nil, err
	}
	if opts.PathClass == nil {
		opts.PathClass = pathClassOf(pc)
	}
	return newListener(ln, local, displayName, platform, handlers, opts), nil
}

// pathClassOf asks a packet conn which class of path it is carrying a peer on
// (§7.2), if it is the kind of packet conn that knows.
//
// A relay conn or an M9 path conn does know: it chose the path. A UDP socket
// does not, and gets nil, which leaves the transport's own answer in place.
func pathClassOf(pc net.PacketConn) func(identity.DeviceID) string {
	classifier, ok := pc.(interface {
		Class(identity.DeviceID) string
	})
	if !ok {
		return nil
	}
	return classifier.Class
}
