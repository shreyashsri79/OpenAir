package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/infra"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// mailboxQueue is how many frames may be waiting for one connected client
// before further frames for it are dropped.
//
// Dropping is correct here and not a compromise: the payload is a QUIC packet,
// QUIC is designed for a lossy path, and it will retransmit. Blocking instead
// would let one slow client stall the relay for everyone, which is the failure
// that actually matters.
const mailboxQueue = 256

// authTimeout bounds the §17 handshake. A connection that opens and does not
// authenticate holds a slot and a goroutine, so it does not get to hold them
// indefinitely.
const authTimeout = 10 * time.Second

// Server is a relay (§17). It forwards ciphertext between authenticated clients
// and holds no keys of theirs.
type Server struct {
	local identity.Identity
	logf  func(string, ...any)

	mu      sync.RWMutex
	clients map[identity.DeviceID]*mailbox
}

type mailbox struct {
	out    chan outbound
	closed chan struct{}
	once   sync.Once
}

type outbound struct {
	from    identity.DeviceID
	payload []byte
}

func (m *mailbox) close() { m.once.Do(func() { close(m.closed) }) }

// Config configures a relay Server.
type Config struct {
	// Local is the relay's own identity: its key terminates TLS and clients pin
	// the DeviceID it derives (D-62). The relay holds no key belonging to any
	// client and could not read their traffic if it wanted to.
	Local identity.Identity

	Logf func(format string, args ...any)
}

// NewServer builds a relay. It binds nothing; call Serve with a listener.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Local == nil {
		return nil, errors.New("relay: Config.Local is required")
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Server{local: cfg.Local, logf: logf, clients: map[identity.DeviceID]*mailbox{}}, nil
}

// DeviceID is the relay's own identifier, which clients pin.
func (s *Server) DeviceID() identity.DeviceID { return s.local.DeviceID() }

// Connected reports how many clients are authenticated right now.
func (s *Server) Connected() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// Serve accepts connections until ctx is cancelled or the listener fails.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.serveConn(ctx, nc)
	}
}

func (s *Server) serveConn(ctx context.Context, nc net.Conn) {
	defer nc.Close()

	tlsConf, observed, err := infra.PairingTLS(s.local)
	if err != nil {
		s.logf("tls config: %v", err)
		return
	}
	conn := tls.Server(nc, tlsConf)

	hsCtx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()
	if err := conn.HandshakeContext(hsCtx); err != nil {
		s.logf("handshake with %s: %v", nc.RemoteAddr(), err)
		return
	}
	clientKey := observed.Key()
	if len(clientKey) != ed25519.PublicKeySize {
		return
	}

	id, err := s.authenticate(conn, clientKey)
	if err != nil {
		s.logf("authentication from %s: %v", nc.RemoteAddr(), err)
		return
	}

	box := s.register(id)
	defer s.unregister(id, box)
	s.logf("%s connected", id.Fingerprint())

	// One goroutine per direction. The reader forwards to other mailboxes; the
	// writer drains this client's own.
	go s.pump(conn, box)

	for {
		_ = conn.SetReadDeadline(time.Time{})
		dst, payload, err := readFrame(conn)
		if err != nil {
			return
		}
		s.deliver(id, dst, payload)
	}
}

// authenticate runs §17's three-message exchange.
//
// The TLS handshake has already proved possession of the same key, so this is
// belt and braces — but it is what §17 specifies, and it is the part that does
// not depend on the transport being TLS. Keeping it means a future relay
// speaking something else authenticates identically.
func (s *Server) authenticate(conn *tls.Conn, clientKey ed25519.PublicKey) (identity.DeviceID, error) {
	_ = conn.SetDeadline(time.Now().Add(authTimeout))
	defer conn.SetDeadline(time.Time{})

	var hello openairv1.RelayHello
	if err := infra.ReadInto(conn, infra.MsgRelayHello, &hello); err != nil {
		return "", err
	}
	claimed := identity.DeviceID(hello.GetDeviceId())
	if got := identity.DeriveDeviceID(clientKey); got != claimed {
		_ = infra.WriteMessage(conn, infra.MsgError,
			&openairv1.InfraError{Message: "the device id does not match the key on this connection"})
		return "", fmt.Errorf("claimed %s, key derives %s", claimed, got)
	}
	if len(hello.GetNonce()) != identity.RelayNonceLen {
		_ = infra.WriteMessage(conn, infra.MsgError, &openairv1.InfraError{Message: "bad nonce length"})
		return "", fmt.Errorf("client nonce is %d bytes", len(hello.GetNonce()))
	}

	serverNonce := make([]byte, identity.RelayNonceLen)
	if _, err := rand.Read(serverNonce); err != nil {
		return "", err
	}
	if err := infra.WriteMessage(conn, infra.MsgRelayChallenge,
		&openairv1.RelayChallenge{ServerNonce: serverNonce}); err != nil {
		return "", err
	}

	var auth openairv1.RelayAuth
	if err := infra.ReadInto(conn, infra.MsgRelayAuth, &auth); err != nil {
		return "", err
	}
	if err := identity.VerifyRelayAuth(clientKey, claimed, hello.GetNonce(), serverNonce, auth.GetSignature()); err != nil {
		_ = infra.WriteMessage(conn, infra.MsgError, &openairv1.InfraError{Message: "signature does not verify"})
		return "", err
	}
	if err := infra.WriteMessage(conn, infra.MsgRelayAuthOK, &openairv1.RelayChallenge{}); err != nil {
		return "", err
	}
	return claimed, nil
}

func (s *Server) register(id identity.DeviceID) *mailbox {
	box := &mailbox{out: make(chan outbound, mailboxQueue), closed: make(chan struct{})}

	s.mu.Lock()
	if prev, ok := s.clients[id]; ok {
		// A device reconnecting replaces itself. The old connection is almost
		// certainly a socket that died without a FIN — a phone that changed
		// networks — and keeping it would mean delivering to a mailbox nobody
		// is reading.
		prev.close()
	}
	s.clients[id] = box
	s.mu.Unlock()
	return box
}

func (s *Server) unregister(id identity.DeviceID, box *mailbox) {
	box.close()
	s.mu.Lock()
	if cur, ok := s.clients[id]; ok && cur == box {
		delete(s.clients, id)
	}
	s.mu.Unlock()
	s.logf("%s disconnected", id.Fingerprint())
}

// deliver forwards one frame, or drops it.
//
// §17: the relay MUST NOT deliver to a DeviceID that has not authenticated.
// There is no queue for an absent device and no error back to the sender —
// which is right, because the payload is a QUIC packet and a packet that does
// not arrive is a case QUIC already handles.
func (s *Server) deliver(from, to identity.DeviceID, payload []byte) {
	s.mu.RLock()
	box, ok := s.clients[to]
	s.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case box.out <- outbound{from: from, payload: payload}:
	case <-box.closed:
	default:
		// The client is not keeping up. Dropping beats blocking: QUIC
		// retransmits, and one slow client must not stall the relay.
	}
}

// pump writes this client's mailbox to its connection. The DeviceID in an
// outbound frame is the *source* (D-64) — the destination would tell the client
// only what it already knows, which is that the frame is for it.
func (s *Server) pump(conn *tls.Conn, box *mailbox) {
	for {
		select {
		case <-box.closed:
			_ = conn.Close()
			return
		case msg := <-box.out:
			if err := writeFrame(conn, msg.from, msg.payload); err != nil {
				box.close()
				_ = conn.Close()
				return
			}
		}
	}
}
