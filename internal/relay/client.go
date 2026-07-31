package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/infra"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// ErrServerKeyMismatch reports that the relay presented a key deriving a
// DeviceID other than the pinned one (§2, D-62).
var ErrServerKeyMismatch = errors.New("relay: server key does not match the pinned DeviceID")

// ErrClosed reports use of a PacketConn whose relay connection has ended.
var ErrClosed = errors.New("relay: connection closed")

// inboundQueue bounds packets received from the relay and not yet read by the
// QUIC stack. Same reasoning as the server's mailbox: these are QUIC packets,
// and dropping one is a case QUIC handles.
const inboundQueue = 256

// ClientConfig configures a relay client.
type ClientConfig struct {
	// Local is this device's identity: it terminates TLS and signs the §17
	// challenge. The identity key, not the privilege key (D-20).
	Local identity.Identity

	// Addr is the relay's "host:port".
	Addr string

	// ServerID is the DeviceID the relay's key must derive. Required, for the
	// same reason as the rendezvous server's: an address is not an identity.
	// A relay cannot read what it carries, but one that is not the relay you
	// meant is one that can drop everything you send.
	ServerID identity.DeviceID

	DialTimeout time.Duration

	Logf func(format string, args ...any)
}

// PacketConn is a net.PacketConn whose packets travel through a relay.
//
// This is what makes a relayed path a genuine QUIC connection rather than a
// tunnel with its own rules: quic-go is handed this instead of a UDP socket,
// addresses peers by relay.Addr instead of by ip:port, and is otherwise
// unchanged. Migration off the relay onto a direct path later (M9, §18) is then
// QUIC's own connection migration rather than a reconnection.
//
// It satisfies net.PacketConn exactly, including the deadline methods, which
// quic-go uses.
type PacketConn struct {
	conn  *tls.Conn
	local identity.DeviceID
	logf  func(string, ...any)

	in     chan inbound
	closed chan struct{}
	once   sync.Once

	wmu sync.Mutex

	dmu           sync.Mutex
	readDeadline  time.Time
	deadlineTimer *time.Timer
	deadlineCh    chan struct{}
}

type inbound struct {
	from    identity.DeviceID
	payload []byte
}

// Dial connects to the relay, authenticates (§17) and returns a PacketConn.
func Dial(ctx context.Context, cfg ClientConfig) (*PacketConn, error) {
	switch {
	case cfg.Local == nil:
		return nil, errors.New("relay: ClientConfig.Local is required")
	case cfg.Addr == "":
		return nil, errors.New("relay: ClientConfig.Addr is required")
	case !cfg.ServerID.Valid():
		return nil, fmt.Errorf("relay: ClientConfig.ServerID %q is not a DeviceID", cfg.ServerID)
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}

	dialCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()

	var d net.Dialer
	nc, err := d.DialContext(dialCtx, "tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("relay: dial %s: %w", cfg.Addr, err)
	}

	tlsConf, observed, err := infra.PairingTLS(cfg.Local)
	if err != nil {
		nc.Close()
		return nil, err
	}
	conn := tls.Client(nc, tlsConf)
	if err := conn.HandshakeContext(dialCtx); err != nil {
		nc.Close()
		return nil, fmt.Errorf("relay: handshake with %s: %w", cfg.Addr, err)
	}
	serverKey := observed.Key()
	if len(serverKey) != ed25519.PublicKeySize {
		conn.Close()
		return nil, fmt.Errorf("relay: %s presented no usable key", cfg.Addr)
	}
	if got := identity.DeriveDeviceID(serverKey); got != cfg.ServerID {
		conn.Close()
		return nil, fmt.Errorf("%w: pinned %s, got %s", ErrServerKeyMismatch, cfg.ServerID, got)
	}

	if err := authenticate(conn, cfg.Local); err != nil {
		conn.Close()
		return nil, err
	}

	pc := &PacketConn{
		conn:       conn,
		local:      cfg.Local.DeviceID(),
		logf:       cfg.Logf,
		in:         make(chan inbound, inboundQueue),
		closed:     make(chan struct{}),
		deadlineCh: make(chan struct{}),
	}
	go pc.readLoop()
	return pc, nil
}

// authenticate runs the client half of §17's exchange.
func authenticate(conn *tls.Conn, local identity.Identity) error {
	_ = conn.SetDeadline(time.Now().Add(authTimeout))
	defer conn.SetDeadline(time.Time{})

	nonce := make([]byte, identity.RelayNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	if err := infra.WriteMessage(conn, infra.MsgRelayHello, &openairv1.RelayHello{
		DeviceId: string(local.DeviceID()),
		Nonce:    nonce,
	}); err != nil {
		return err
	}

	var challenge openairv1.RelayChallenge
	if err := infra.ReadInto(conn, infra.MsgRelayChallenge, &challenge); err != nil {
		return err
	}

	signer, ok := local.(interface {
		SignRelayAuth(clientNonce, serverNonce []byte) ([]byte, error)
	})
	if !ok {
		return errors.New("relay: identity cannot sign a relay challenge")
	}
	sig, err := signer.SignRelayAuth(nonce, challenge.GetServerNonce())
	if err != nil {
		return err
	}
	if err := infra.WriteMessage(conn, infra.MsgRelayAuth, &openairv1.RelayAuth{Signature: sig}); err != nil {
		return err
	}

	var ok2 openairv1.RelayChallenge
	return infra.ReadInto(conn, infra.MsgRelayAuthOK, &ok2)
}

// readLoop turns inbound frames into packets waiting to be read.
func (pc *PacketConn) readLoop() {
	defer pc.Close()
	for {
		from, payload, err := readFrame(pc.conn)
		if err != nil {
			return
		}
		select {
		case pc.in <- inbound{from: from, payload: payload}:
		case <-pc.closed:
			return
		default:
			// Nobody is reading fast enough. QUIC retransmits.
		}
	}
}

// ReadFrom returns the next relayed packet and the peer it came from.
func (pc *PacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		pc.dmu.Lock()
		deadlineCh := pc.deadlineCh
		pc.dmu.Unlock()

		select {
		case msg := <-pc.in:
			n := copy(p, msg.payload)
			return n, Addr{DeviceID: msg.from}, nil
		case <-pc.closed:
			return 0, nil, ErrClosed
		case <-deadlineCh:
			// A deadline fired, or was replaced. If it has genuinely passed,
			// report a timeout; otherwise loop and wait on the new one.
			pc.dmu.Lock()
			expired := !pc.readDeadline.IsZero() && !time.Now().Before(pc.readDeadline)
			pc.dmu.Unlock()
			if expired {
				return 0, nil, os.ErrDeadlineExceeded
			}
		}
	}
}

// WriteTo sends a packet to a peer through the relay. addr must be a
// relay.Addr: this connection reaches DeviceIDs, not IP addresses.
func (pc *PacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	target, ok := addr.(Addr)
	if !ok {
		return 0, fmt.Errorf("relay: cannot send to %T (%s); a relayed path addresses a DeviceID", addr, addr)
	}
	select {
	case <-pc.closed:
		return 0, ErrClosed
	default:
	}

	pc.wmu.Lock()
	defer pc.wmu.Unlock()
	if err := writeFrame(pc.conn, target.DeviceID, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// LocalAddr is this device, as the relay knows it.
func (pc *PacketConn) LocalAddr() net.Addr { return Addr{DeviceID: pc.local} }

// Close ends the relay connection.
func (pc *PacketConn) Close() error {
	pc.once.Do(func() {
		close(pc.closed)
		_ = pc.conn.Close()
	})
	return nil
}

// Done is closed when the relay connection ends, so a caller can notice a relay
// that went away without waiting for a read to fail.
func (pc *PacketConn) Done() <-chan struct{} { return pc.closed }

// SetDeadline sets both deadlines. quic-go uses the read one.
func (pc *PacketConn) SetDeadline(t time.Time) error {
	if err := pc.SetReadDeadline(t); err != nil {
		return err
	}
	return pc.SetWriteDeadline(t)
}

// SetReadDeadline makes a blocked ReadFrom return after t.
func (pc *PacketConn) SetReadDeadline(t time.Time) error {
	pc.dmu.Lock()
	defer pc.dmu.Unlock()

	if pc.deadlineTimer != nil {
		pc.deadlineTimer.Stop()
		pc.deadlineTimer = nil
	}
	// Replacing the channel wakes anything waiting on the old one, which then
	// re-reads the deadline rather than assuming it fired.
	close(pc.deadlineCh)
	pc.deadlineCh = make(chan struct{})
	pc.readDeadline = t

	if !t.IsZero() {
		ch := pc.deadlineCh
		pc.deadlineTimer = time.AfterFunc(time.Until(t), func() {
			pc.dmu.Lock()
			defer pc.dmu.Unlock()
			if pc.deadlineCh == ch {
				close(pc.deadlineCh)
				pc.deadlineCh = make(chan struct{})
			}
		})
	}
	return nil
}

// SetWriteDeadline is a no-op: writes here are a framed copy onto an already
// connected socket and do not block on the peer.
func (pc *PacketConn) SetWriteDeadline(t time.Time) error { return nil }
