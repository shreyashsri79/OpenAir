package conn

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/apernet/quic-go"

	"github.com/shreyashsri79/openair/internal/congestion"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

// Endpoint is one QUIC transport over one socket, accepting and dialling on it.
//
// It exists because M9 makes a device use a single UDP socket for everything:
// a NAT mapping belongs to a port, so the port that gets punched has to be the
// port that carries the sessions, inbound and outbound alike.
//
// One socket does not mean one quic-go Transport by itself, and that difference
// is not cosmetic. quic.Listen and quic.Dial each build a Transport of their
// own, and each Transport runs its own read loop on the conn it was handed --
// so two of them on one socket race for every packet, and each drops what the
// other should have had. Connections still complete, because QUIC retransmits
// what goes missing, which is exactly what makes the mistake hard to see: it
// looks like an intermittently slow network. A Transport demultiplexes by
// connection ID and hands each packet to the right connection, which is what
// this type exists to keep true.
type Endpoint struct {
	tr          *quic.Transport
	local       identity.Identity
	displayName string
	platform    string
	handlers    map[byte]session.Handler
	ln          Listener
}

// NewEndpoint builds the transport and starts accepting on it.
func NewEndpoint(pc net.PacketConn, local identity.Identity, displayName, platform string, handlers map[byte]session.Handler, opts ListenOptions) (*Endpoint, error) {
	if pc == nil {
		return nil, errors.New("conn: no packet conn for the endpoint")
	}
	tlsConf, err := local.TLSConfig(nil)
	if err != nil {
		return nil, err
	}
	if opts.PathClass == nil {
		opts.PathClass = pathClassOf(pc)
	}

	tr := &quic.Transport{Conn: pc}
	ln, err := tr.Listen(tlsConf, quicConfig())
	if err != nil {
		return nil, err
	}
	return &Endpoint{
		tr:          tr,
		local:       local,
		displayName: displayName,
		platform:    platform,
		handlers:    handlers,
		ln:          newListener(ln, local, displayName, platform, handlers, opts),
	}, nil
}

// Listener accepts inbound sessions on this endpoint.
func (e *Endpoint) Listener() Listener { return e.ln }

// Addr is the bound address, including the port chosen for ":0".
func (e *Endpoint) Addr() string { return e.ln.Addr() }

// Dial opens a session to a peer out of this endpoint's socket.
//
// remote is whatever the packet conn addresses: a *net.UDPAddr for an ordinary
// direct dial, or a path.Addr for a peer reached by DeviceID over a relay or a
// punched path.
func (e *Endpoint) Dial(ctx context.Context, remote net.Addr, pinned identity.Peer) (session.Session, error) {
	tlsConf, err := e.local.TLSConfig(pinned.IdentityPublicKey)
	if err != nil {
		return nil, err
	}
	qc, err := e.tr.Dial(ctx, remote, tlsConf, quicConfig())
	if err != nil {
		return nil, fmt.Errorf("conn: dial %s over %s: %w", remote, remote.Network(), err)
	}
	// After the handshake and before any real traffic: BBR replaces quic-go's
	// CUBIC (D-14, D-16).
	congestion.Use(qc)

	sess, err := session.New(ctx, qc, session.Config{
		Local:       e.local,
		Peer:        pinned,
		DisplayName: e.displayName,
		Platform:    e.platform,
		Handlers:    e.handlers,
		Initiator:   true,
		PathClass:   pathClassOf(e.tr.Conn),
	})
	if err != nil {
		return nil, translateRemoteClose(err)
	}
	return sess, nil
}

// Close stops accepting and releases the transport. The packet conn underneath
// belongs to whoever created it.
func (e *Endpoint) Close() error {
	err := e.ln.Close()
	if trErr := e.tr.Close(); err == nil {
		err = trErr
	}
	return err
}
