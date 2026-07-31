package rendezvous

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/infra"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// maxEndpoints bounds one registration. A device advertises its LAN addresses,
// its reflexive address and its relay home; a client sending hundreds is either
// broken or trying to make the server's memory its problem.
const maxEndpoints = 16

// idleTimeout closes a connection that has stopped speaking. Registrations
// heartbeat every few minutes, so a connection idle for longer than the
// registration lifetime is not a client that is quiet, it is a client that is
// gone.
const idleTimeout = 15 * time.Minute

// Server is a rendezvous server (§16). It is self-hostable by construction:
// one listener, an in-memory map and no dependencies.
//
// State is deliberately not persisted. Every entry expires within ten minutes
// and a client that matters is heartbeating, so a restart costs one heartbeat
// interval and buys the operator a service with no database to secure, back up
// or leak.
type Server struct {
	local identity.Identity
	now   func() time.Time
	logf  func(string, ...any)

	mu      sync.RWMutex
	entries map[identity.DeviceID]entry
}

type entry struct {
	reg     *openairv1.Registration
	expires time.Time
}

// Config configures a Server.
type Config struct {
	// Local is the server's own identity. Its key terminates TLS, and clients
	// pin the DeviceID it derives — the same trust model peers use with each
	// other (D-7), so there is no certificate authority here either.
	Local identity.Identity

	// Now overrides the clock, for tests that need to cross an expiry.
	Now func() time.Time

	Logf func(format string, args ...any)
}

// NewServer builds a server. It binds nothing; call Serve with a listener.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Local == nil {
		return nil, errors.New("rendezvous: Config.Local is required")
	}
	s := &Server{
		local:   cfg.Local,
		now:     cfg.Now,
		logf:    cfg.Logf,
		entries: map[identity.DeviceID]entry{},
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.logf == nil {
		s.logf = func(string, ...any) {}
	}
	return s, nil
}

// DeviceID is the server's own identifier, which clients pin.
func (s *Server) DeviceID() identity.DeviceID { return s.local.DeviceID() }

// Serve accepts connections until ctx is cancelled or the listener fails.
//
// The listener is a plain TCP one: TLS is applied per connection here, because
// each connection needs its own observer to learn the client's key from the
// handshake.
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

	hsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := conn.HandshakeContext(hsCtx); err != nil {
		s.logf("handshake with %s failed: %v", nc.RemoteAddr(), err)
		return
	}

	// Who is calling. The client's certificate key is the identity key (D-7),
	// so this is also how a registration is bound to its author: the server
	// never takes the DeviceID in the message at face value.
	clientKey := observed.Key()
	if len(clientKey) != ed25519.PublicKeySize {
		s.logf("client %s presented no usable key", nc.RemoteAddr())
		return
	}
	caller := identity.DeriveDeviceID(clientKey)

	for {
		_ = conn.SetReadDeadline(s.now().Add(idleTimeout))
		msgType, payload, err := infra.ReadMessage(conn)
		if err != nil {
			return
		}
		if err := s.handle(conn, caller, clientKey, msgType, payload); err != nil {
			s.logf("%s: %v", caller, err)
			return
		}
	}
}

func (s *Server) handle(conn *tls.Conn, caller identity.DeviceID, callerKey ed25519.PublicKey, msgType uint16, payload []byte) error {
	switch msgType {
	case infra.MsgRegister:
		var reg openairv1.Registration
		if err := proto.Unmarshal(payload, &reg); err != nil {
			return s.refuse(conn, "malformed registration")
		}
		return s.onRegister(conn, caller, callerKey, &reg)

	case infra.MsgLookupRequest:
		var req openairv1.LookupRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return s.refuse(conn, "malformed lookup")
		}
		return s.onLookup(conn, &req)

	default:
		// Unknown message types are refused rather than ignored. §3.1's
		// ignore-and-continue rule is about capability negotiation between
		// peers; there is no negotiation here, so an unknown type is a client
		// speaking a protocol this server does not have.
		return s.refuse(conn, fmt.Sprintf("unknown message type %d", msgType))
	}
}

// onRegister stores a device's endpoints, having checked that the device asking
// is the device being registered (§16).
func (s *Server) onRegister(conn *tls.Conn, caller identity.DeviceID, callerKey ed25519.PublicKey, reg *openairv1.Registration) error {
	id := identity.DeviceID(reg.GetDeviceId())

	// The registration must be for the device on the other end of this
	// connection. Without this a device could authenticate as itself and then
	// register endpoints for somebody else — the signature check below would
	// catch it only because the signature is over the DeviceID, so this is the
	// cheaper and clearer refusal.
	if id != caller {
		return s.refuse(conn, "a device may only register itself")
	}
	if n := len(reg.GetEndpoints()); n > maxEndpoints {
		return s.refuse(conn, fmt.Sprintf("%d endpoints, at most %d", n, maxEndpoints))
	}

	if err := identity.VerifyRegistration(callerKey, id, reg.GetEndpoints(), reg.GetRelayHome(),
		reg.GetIssuedAt(), reg.GetExpiresAt(), reg.GetSignature()); err != nil {
		return s.refuse(conn, "signature does not verify")
	}

	now := s.now()
	expires := time.UnixMilli(reg.GetExpiresAt())
	switch {
	case !expires.After(now):
		return s.refuse(conn, "registration has already expired")
	case expires.Sub(now) > identity.MaxRegistrationLifetime:
		// §16 caps this at ten minutes so entries stay short-lived and a device
		// that vanishes stops being advertised quickly.
		return s.refuse(conn, "expiry is more than ten minutes out")
	}

	s.mu.Lock()
	s.entries[id] = entry{reg: proto.Clone(reg).(*openairv1.Registration), expires: expires}
	s.mu.Unlock()

	return infra.WriteMessage(conn, infra.MsgRegisterAck, &openairv1.RegistrationAck{ExpiresAt: expires.UnixMilli()})
}

// onLookup answers where a device is, if it has said so recently.
//
// Lookups are not restricted to devices that are paired with the target: the
// server has no way to know who is paired with whom, and inventing one would
// mean telling it. What it returns is a signed registration the caller still
// has to verify against a key it already pinned, so learning an endpoint is
// worth nothing on its own.
func (s *Server) onLookup(conn *tls.Conn, req *openairv1.LookupRequest) error {
	id := identity.DeviceID(req.GetDeviceId())

	s.mu.RLock()
	e, ok := s.entries[id]
	s.mu.RUnlock()

	if !ok || !e.expires.After(s.now()) {
		if ok {
			s.mu.Lock()
			if cur, still := s.entries[id]; still && cur.expires.Equal(e.expires) {
				delete(s.entries, id)
			}
			s.mu.Unlock()
		}
		return infra.WriteMessage(conn, infra.MsgLookupResponse, &openairv1.LookupResponse{Found: false})
	}
	return infra.WriteMessage(conn, infra.MsgLookupResponse, &openairv1.LookupResponse{
		Registration: e.reg,
		Found:        true,
	})
}

func (s *Server) refuse(conn *tls.Conn, msg string) error {
	if err := infra.WriteMessage(conn, infra.MsgError, &openairv1.InfraError{Message: msg}); err != nil {
		return err
	}
	return fmt.Errorf("refused: %s", msg)
}

// Entries reports how many live registrations the server holds, for a status
// line and for tests. Expired ones are swept first, so this is the honest count
// rather than the map's size.
func (s *Server) Entries() int {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.entries {
		if !e.expires.After(now) {
			delete(s.entries, id)
		}
	}
	return len(s.entries)
}
