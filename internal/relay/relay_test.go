package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/infra"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

func newIdentity(t *testing.T) *identity.FileIdentity {
	t.Helper()
	id, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type harness struct {
	server   *Server
	serverID identity.DeviceID
	addr     string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	id := newIdentity(t)
	srv, err := NewServer(Config{
		Local: id,
		Logf:  func(format string, args ...any) { t.Logf("relay: "+format, args...) },
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ctx, ln); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return &harness{server: srv, serverID: id.DeviceID(), addr: ln.Addr().String()}
}

func (h *harness) dial(t *testing.T, local identity.Identity) *PacketConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pc, err := Dial(ctx, ClientConfig{Local: local, Addr: h.addr, ServerID: h.serverID})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { pc.Close() })
	return pc
}

// TestRelayForwardsBetweenAuthenticatedClients is §17's core: two clients, one
// packet, delivered with the sender named.
func TestRelayForwardsBetweenAuthenticatedClients(t *testing.T) {
	h := newHarness(t)
	a, b := newIdentity(t), newIdentity(t)
	pcA, pcB := h.dial(t, a), h.dial(t, b)

	payload := []byte("an opaque QUIC datagram")
	if _, err := pcA.WriteTo(payload, Addr{DeviceID: b.DeviceID()}); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	buf := make([]byte, 1500)
	_ = pcB.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, from, err := pcB.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Fatalf("received %q, want %q", buf[:n], payload)
	}

	// The address is the *source* (D-64). Without that a receiver could not
	// tell which peer a packet belongs to, and one QUIC connection per peer
	// would be unattributable.
	got, ok := from.(Addr)
	if !ok {
		t.Fatalf("ReadFrom returned a %T, want relay.Addr", from)
	}
	if got.DeviceID != a.DeviceID() {
		t.Fatalf("packet came from %s, want %s", got.DeviceID, a.DeviceID())
	}
}

// TestRelayRefusesDeliveryToAnUnauthenticatedDevice is §17's rule stated
// exactly: the relay MUST NOT deliver to a DeviceID that has not authenticated.
func TestRelayRefusesDeliveryToAnUnauthenticatedDevice(t *testing.T) {
	h := newHarness(t)
	a := newIdentity(t)
	absent := newIdentity(t)
	pcA := h.dial(t, a)

	// Writing succeeds -- the sender's connection is fine -- and nothing is
	// delivered anywhere. There is deliberately no error back: the payload is a
	// QUIC packet, and a packet that does not arrive is a case QUIC handles.
	if _, err := pcA.WriteTo([]byte("nobody is there"), Addr{DeviceID: absent.DeviceID()}); err != nil {
		t.Fatalf("WriteTo an absent device: %v", err)
	}
	if got := h.server.Connected(); got != 1 {
		t.Fatalf("relay reports %d connected clients, want 1", got)
	}

	// Now the device connects. The earlier packet must not have been queued for
	// it: a relay that spooled traffic for absent devices would be storing what
	// it is not supposed to hold.
	pcAbsent := h.dial(t, absent)
	buf := make([]byte, 1500)
	_ = pcAbsent.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := pcAbsent.ReadFrom(buf); err == nil {
		t.Fatal("a packet sent before this device authenticated was delivered afterwards")
	}
}

// TestRelayRefusesAForgedDeviceID: a client with a perfectly good identity of
// its own cannot claim somebody else's mailbox.
func TestRelayRefusesAForgedDeviceID(t *testing.T) {
	h := newHarness(t)
	attacker := newIdentity(t)
	victim := newIdentity(t)

	conn, err := rawTLS(t, h, attacker)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	nonce := make([]byte, identity.RelayNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	if err := infra.WriteMessage(conn, infra.MsgRelayHello, &openairv1.RelayHello{
		DeviceId: string(victim.DeviceID()), // not the key on this connection
		Nonce:    nonce,
	}); err != nil {
		t.Fatal(err)
	}

	var challenge openairv1.RelayChallenge
	err = infra.ReadInto(conn, infra.MsgRelayChallenge, &challenge)
	if err == nil {
		t.Fatal("the relay issued a challenge for a device id the caller does not hold the key for")
	}
	var serverErr *infra.ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("expected a refusal from the server, got %v", err)
	}
}

// TestRelayRefusesAWrongSignature: the challenge is what makes the exchange
// fresh, so a signature over anything else must not authenticate.
func TestRelayRefusesAWrongSignature(t *testing.T) {
	h := newHarness(t)
	client := newIdentity(t)

	conn, err := rawTLS(t, h, client)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	nonce := make([]byte, identity.RelayNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	if err := infra.WriteMessage(conn, infra.MsgRelayHello, &openairv1.RelayHello{
		DeviceId: string(client.DeviceID()),
		Nonce:    nonce,
	}); err != nil {
		t.Fatal(err)
	}
	var challenge openairv1.RelayChallenge
	if err := infra.ReadInto(conn, infra.MsgRelayChallenge, &challenge); err != nil {
		t.Fatal(err)
	}

	// Signed over a server nonce the server did not choose: this is what a
	// replayed signature from an earlier session looks like.
	stale := make([]byte, identity.RelayNonceLen)
	sig, err := client.SignRelayAuth(nonce, stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := infra.WriteMessage(conn, infra.MsgRelayAuth, &openairv1.RelayAuth{Signature: sig}); err != nil {
		t.Fatal(err)
	}

	var ok openairv1.RelayChallenge
	if err := infra.ReadInto(conn, infra.MsgRelayAuthOK, &ok); err == nil {
		t.Fatal("a signature over the wrong challenge authenticated")
	}
	if got := h.server.Connected(); got != 0 {
		t.Fatalf("relay reports %d connected clients after a failed authentication", got)
	}
}

// TestRelayServerKeyIsPinned: the relay is authenticated the same way a peer is
// (§2, D-62). It cannot read what it carries, but the wrong relay is one that
// drops everything.
func TestRelayServerKeyIsPinned(t *testing.T) {
	h := newHarness(t)
	client := newIdentity(t)
	wrong := newIdentity(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Dial(ctx, ClientConfig{Local: client, Addr: h.addr, ServerID: wrong.DeviceID()})
	if !errors.Is(err, ErrServerKeyMismatch) {
		t.Fatalf("Dial against a mismatched relay key: %v, want ErrServerKeyMismatch", err)
	}
}

// TestRelayDoesNotSeePlaintext is the property PRD R27 rests on, checked the
// only way it can be from outside: what the relay forwards is byte-identical to
// what was handed to it, and it is the sender that chose those bytes. A relay
// that transformed the payload would break QUIC's own authentication, which is
// what makes "the relay is a network element, not a participant" true.
func TestRelayDoesNotSeePlaintext(t *testing.T) {
	h := newHarness(t)
	a, b := newIdentity(t), newIdentity(t)
	pcA, pcB := h.dial(t, a), h.dial(t, b)

	// Bytes that are not valid UTF-8 and not a valid anything: the relay has no
	// business interpreting them.
	payload := make([]byte, 1200)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := pcA.WriteTo(payload, Addr{DeviceID: b.DeviceID()}); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 1500)
	_ = pcB.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := pcB.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Fatal("the relay did not forward the payload verbatim")
	}
}

// TestReconnectReplacesTheMailbox: a device that reconnects -- a phone that
// changed networks -- takes over its own mailbox rather than ending up with two.
func TestReconnectReplacesTheMailbox(t *testing.T) {
	h := newHarness(t)
	a, b := newIdentity(t), newIdentity(t)
	pcA := h.dial(t, a)
	first := h.dial(t, b)

	second := h.dial(t, b)
	waitFor(t, "the first connection to be dropped", func() bool {
		select {
		case <-first.Done():
			return true
		default:
			return false
		}
	})
	if got := h.server.Connected(); got != 2 {
		t.Fatalf("relay reports %d clients, want 2 (one per device)", got)
	}

	if _, err := pcA.WriteTo([]byte("after the reconnect"), Addr{DeviceID: b.DeviceID()}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1500)
	_ = second.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := second.ReadFrom(buf)
	if err != nil {
		t.Fatalf("the reconnected client received nothing: %v", err)
	}
	if string(buf[:n]) != "after the reconnect" {
		t.Fatalf("received %q", buf[:n])
	}
}

// TestOversizedPayloadRefused: the cap is checked before the write, so a
// hostile client cannot make the relay allocate by claiming a large frame.
func TestOversizedPayloadRefused(t *testing.T) {
	h := newHarness(t)
	a, b := newIdentity(t), newIdentity(t)
	pc := h.dial(t, a)

	if _, err := pc.WriteTo(make([]byte, MaxPayload+1), Addr{DeviceID: b.DeviceID()}); err == nil {
		t.Fatal("an oversized payload was accepted")
	}
}

// TestWriteToRequiresARelayAddr: this connection reaches DeviceIDs. Handing it
// a UDP address is a caller mistake worth an error rather than a silent drop.
func TestWriteToRequiresARelayAddr(t *testing.T) {
	h := newHarness(t)
	pc := h.dial(t, newIdentity(t))

	udp := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 9000}
	if _, err := pc.WriteTo([]byte("x"), udp); err == nil {
		t.Fatal("a UDP address was accepted by a relayed connection")
	}
}

// TestReadDeadlineIsHonoured: quic-go sets read deadlines on its PacketConn, so
// one that ignored them would stall the whole stack.
func TestReadDeadlineIsHonoured(t *testing.T) {
	h := newHarness(t)
	pc := h.dial(t, newIdentity(t))

	_ = pc.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	start := time.Now()
	_, _, err := pc.ReadFrom(make([]byte, 1500))
	if err == nil {
		t.Fatal("ReadFrom returned a packet that was never sent")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("ReadFrom took %s to honour a 150 ms deadline", elapsed)
	}
}

// TestRelaySigningInputIsDomainSeparated closes one of D-43's four open
// security questions: a signature made for a relay must not be usable as an
// Owned AuthProof or a rendezvous registration, and none of theirs here.
func TestRelaySigningInputIsDomainSeparated(t *testing.T) {
	id := newIdentity(t)
	nonceA := bytes.Repeat([]byte{1}, identity.RelayNonceLen)
	nonceB := bytes.Repeat([]byte{2}, identity.RelayNonceLen)

	relayInput, err := identity.RelaySigningInput(id.DeviceID(), nonceA, nonceB)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(relayInput, []byte("openair-relay-v1")) {
		t.Fatal("the relay signing input carries no domain separator")
	}

	rendezvousInput, err := identity.RendezvousSigningInput(id.DeviceID(), []string{"192.0.2.1:9000"}, "", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(rendezvousInput, []byte("openair-relay-v1")) {
		t.Fatal("the two signing inputs share a prefix")
	}

	// And the nonces cannot be re-split under one signature.
	short, err := identity.RelaySigningInput(id.DeviceID(), nonceA, nonceB)
	if err != nil {
		t.Fatal(err)
	}
	swapped, err := identity.RelaySigningInput(id.DeviceID(), nonceB, nonceA)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(short, swapped) {
		t.Fatal("swapping the two nonces produces the same signed bytes")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// rawTLS opens the TLS connection without running the §17 exchange, so a test
// can send a handshake the client library would never produce.
func rawTLS(t *testing.T, h *harness, local identity.Identity) (*tls.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", h.addr)
	if err != nil {
		return nil, err
	}
	tlsConf, _, err := infra.PairingTLS(local)
	if err != nil {
		nc.Close()
		return nil, err
	}
	conn := tls.Client(nc, tlsConf)
	if err := conn.HandshakeContext(ctx); err != nil {
		nc.Close()
		return nil, err
	}
	return conn, nil
}
