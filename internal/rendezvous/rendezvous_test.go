package rendezvous

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

func newIdentity(t *testing.T) *identity.FileIdentity {
	t.Helper()
	id, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	return id
}

// clock is a hand-wound clock, so an expiry can be crossed without waiting for
// one. The server takes it; the client uses the real one, which is the honest
// arrangement — the two ends genuinely do not share a clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.t.IsZero() {
		return time.Now()
	}
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

type harness struct {
	server   *Server
	serverID identity.DeviceID
	addr     string
	clk      *clock
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clk := &clock{}
	srvID := newIdentity(t)
	srv, err := NewServer(Config{
		Local: srvID,
		Now:   clk.now,
		Logf:  func(format string, args ...any) { t.Logf(format, args...) },
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
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

	return &harness{server: srv, serverID: srvID.DeviceID(), addr: ln.Addr().String(), clk: clk}
}

func (h *harness) client(t *testing.T, local identity.Identity) *Client {
	t.Helper()
	c, err := NewClient(ClientConfig{
		Local:    local,
		Addr:     h.addr,
		ServerID: h.serverID,
		Logf:     func(format string, args ...any) { t.Logf(format, args...) },
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func ctxFor(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestRegisterThenLookup is §16 end to end: a device says where it is, another
// asks, and the answer is verified against the key it already pinned.
func TestRegisterThenLookup(t *testing.T) {
	h := newHarness(t)
	deviceA := newIdentity(t)
	deviceB := newIdentity(t)

	endpoints := []string{"192.0.2.10:9000", "198.51.100.7:41234"}
	expires, err := h.client(t, deviceA).Register(ctxFor(t), endpoints, "relay.example:443")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !expires.After(time.Now()) {
		t.Fatalf("registration expires at %s, which is not in the future", expires)
	}

	got, relayHome, err := h.client(t, deviceB).Lookup(ctxFor(t), deviceA.DeviceID(), deviceA.IdentityPublic())
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if strings.Join(got, ",") != strings.Join(endpoints, ",") {
		t.Errorf("endpoints %v, want %v", got, endpoints)
	}
	if relayHome != "relay.example:443" {
		t.Errorf("relay home %q, want relay.example:443", relayHome)
	}
}

// TestForgedRegistrationRejected is the property the whole design rests on:
// only the key holder can move a device's endpoints (§16).
//
// The attacker here is not an outsider guessing — it holds a valid connection
// and a valid identity of its own, and simply claims to be somebody else.
func TestForgedRegistrationRejected(t *testing.T) {
	h := newHarness(t)
	victim := newIdentity(t)
	attacker := newIdentity(t)

	c := h.client(t, attacker)
	conn, err := c.dial(ctxFor(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	now := time.Now()
	// Signed by the attacker's key, but claiming the victim's DeviceID.
	sig, err := attacker.SignRegistration([]string{"203.0.113.9:9000"}, "", now.UnixMilli(), now.Add(time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	err = writeMessage(conn, MsgRegister, &openairv1.Registration{
		DeviceId:  string(victim.DeviceID()),
		Endpoints: []string{"203.0.113.9:9000"},
		IssuedAt:  now.UnixMilli(),
		ExpiresAt: now.Add(time.Minute).UnixMilli(),
		Signature: sig,
	})
	if err != nil {
		t.Fatal(err)
	}

	var ack openairv1.RegistrationAck
	if err := readInto(conn, MsgRegisterAck, &ack); err == nil {
		t.Fatal("the server accepted a registration for a device the caller does not hold the key for")
	}
	if h.server.Entries() != 0 {
		t.Fatal("the forged registration was stored")
	}
}

// TestRegistrationSignatureIsCheckedAgainstItsContents: a valid signature over
// *different* endpoints must not carry a swapped-in list. This is what stops a
// server, or anything between, editing where a device says it is.
func TestRegistrationSignatureIsCheckedAgainstItsContents(t *testing.T) {
	h := newHarness(t)
	device := newIdentity(t)

	conn, err := h.client(t, device).dial(ctxFor(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	now := time.Now()
	sig, err := device.SignRegistration([]string{"192.0.2.1:9000"}, "", now.UnixMilli(), now.Add(time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	err = writeMessage(conn, MsgRegister, &openairv1.Registration{
		DeviceId:  string(device.DeviceID()),
		Endpoints: []string{"203.0.113.9:9000"}, // not what was signed
		IssuedAt:  now.UnixMilli(),
		ExpiresAt: now.Add(time.Minute).UnixMilli(),
		Signature: sig,
	})
	if err != nil {
		t.Fatal(err)
	}

	var ack openairv1.RegistrationAck
	if err := readInto(conn, MsgRegisterAck, &ack); err == nil {
		t.Fatal("the server accepted endpoints that were not the ones signed")
	}
	if h.server.Entries() != 0 {
		t.Fatal("the edited registration was stored")
	}
}

// TestExpiryBeyondTenMinutesRefused is §16's cap. It is what forces heartbeats,
// and without it one registration could advertise an address for a year.
func TestExpiryBeyondTenMinutesRefused(t *testing.T) {
	h := newHarness(t)
	device := newIdentity(t)

	c, err := NewClient(ClientConfig{
		Local:    device,
		Addr:     h.addr,
		ServerID: h.serverID,
		Lifetime: 11 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Register(ctxFor(t), []string{"192.0.2.1:9000"}, ""); err == nil {
		t.Fatal("an eleven-minute registration was accepted")
	}
	if h.server.Entries() != 0 {
		t.Fatal("the over-long registration was stored")
	}

	// The boundary itself is fine, which is the other half of the rule.
	ok, err := NewClient(ClientConfig{Local: device, Addr: h.addr, ServerID: h.serverID, Lifetime: 9 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ok.Register(ctxFor(t), []string{"192.0.2.1:9000"}, ""); err != nil {
		t.Fatalf("a nine-minute registration was refused: %v", err)
	}
}

// TestExpiredEntryIsNotReturned: an entry that has lapsed is gone as far as a
// lookup is concerned, whether or not anything has swept it yet.
func TestExpiredEntryIsNotReturned(t *testing.T) {
	h := newHarness(t)
	device := newIdentity(t)
	other := newIdentity(t)

	start := time.Now()
	h.clk.set(start)
	if _, err := h.client(t, device).Register(ctxFor(t), []string{"192.0.2.1:9000"}, ""); err != nil {
		t.Fatal(err)
	}
	if h.server.Entries() != 1 {
		t.Fatal("the registration was not stored")
	}

	h.clk.set(start.Add(DefaultLifetime + time.Minute))

	_, _, err := h.client(t, other).Lookup(ctxFor(t), device.DeviceID(), device.IdentityPublic())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Lookup after expiry: %v, want ErrNotFound", err)
	}
	if h.server.Entries() != 0 {
		t.Fatal("the expired entry is still held")
	}
}

// TestLookupOfUnknownDeviceIsNotAnError distinguishes "offline" from "broken".
// A device that has not registered is the normal case, not a failure.
func TestLookupOfUnknownDeviceIsNotAnError(t *testing.T) {
	h := newHarness(t)
	asker := newIdentity(t)
	stranger := newIdentity(t)

	_, _, err := h.client(t, asker).Lookup(ctxFor(t), stranger.DeviceID(), stranger.IdentityPublic())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Lookup of an unregistered device: %v, want ErrNotFound", err)
	}
}

// TestLookupVerifiesAgainstThePinnedKey is the reason a lookup answer is not
// simply trusted: a server that lies is supposed to achieve nothing, and it
// achieves nothing only because the client checks.
func TestLookupVerifiesAgainstThePinnedKey(t *testing.T) {
	h := newHarness(t)
	device := newIdentity(t)
	asker := newIdentity(t)
	impostor := newIdentity(t)

	if _, err := h.client(t, device).Register(ctxFor(t), []string{"192.0.2.1:9000"}, ""); err != nil {
		t.Fatal(err)
	}

	// The same registration, checked against somebody else's key: this is what
	// a swapped record would look like to a client that had pinned the right
	// device.
	_, _, err := h.client(t, asker).Lookup(ctxFor(t), device.DeviceID(), impostor.IdentityPublic())
	if err == nil {
		t.Fatal("a registration verified against the wrong key was accepted")
	}
	if !strings.Contains(err.Error(), "signed by its key") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// TestServerKeyIsPinned: the rendezvous server is authenticated the same way a
// peer is (§2). A different key on the same address is refused, not trusted on
// the grounds that it answered.
func TestServerKeyIsPinned(t *testing.T) {
	h := newHarness(t)
	device := newIdentity(t)
	wrong := newIdentity(t)

	c, err := NewClient(ClientConfig{Local: device, Addr: h.addr, ServerID: wrong.DeviceID()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Register(ctxFor(t), []string{"192.0.2.1:9000"}, "")
	if !errors.Is(err, ErrServerKeyMismatch) {
		t.Fatalf("Register against a mismatched server key: %v, want ErrServerKeyMismatch", err)
	}
}

// TestReRegistrationReplacesEndpoints: a device that moves networks publishes
// its new addresses over the old ones rather than accumulating both.
func TestReRegistrationReplacesEndpoints(t *testing.T) {
	h := newHarness(t)
	device := newIdentity(t)
	asker := newIdentity(t)
	c := h.client(t, device)

	if _, err := c.Register(ctxFor(t), []string{"192.0.2.1:9000"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Register(ctxFor(t), []string{"198.51.100.2:9000"}, "relay.example:443"); err != nil {
		t.Fatal(err)
	}
	if h.server.Entries() != 1 {
		t.Fatalf("the server holds %d entries for one device", h.server.Entries())
	}

	got, relayHome, err := h.client(t, asker).Lookup(ctxFor(t), device.DeviceID(), device.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "198.51.100.2:9000" || relayHome != "relay.example:443" {
		t.Fatalf("lookup returned %v / %q, want the second registration", got, relayHome)
	}
}

// TestOversizedFrameRefused: the length is checked before anything is
// allocated, so a six-byte header claiming a gigabyte costs nothing.
func TestOversizedFrameRefused(t *testing.T) {
	var buf strings.Builder
	// msgType 1, length 0xFFFFFFFF.
	buf.Write([]byte{1, 0, 0xff, 0xff, 0xff, 0xff})

	_, _, err := readMessage(strings.NewReader(buf.String()))
	if err == nil {
		t.Fatal("a frame claiming 4 GiB was accepted")
	}
	if !strings.Contains(err.Error(), "over the") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestUnknownMessageTypeIsRefused: there is no capability negotiation on this
// connection, so an unknown type is a client speaking a protocol this server
// does not have, and saying so beats ignoring it.
func TestUnknownMessageTypeIsRefused(t *testing.T) {
	h := newHarness(t)
	device := newIdentity(t)

	conn, err := h.client(t, device).dial(ctxFor(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := writeMessage(conn, 999, &openairv1.LookupRequest{DeviceId: string(device.DeviceID())}); err != nil {
		t.Fatal(err)
	}
	var resp openairv1.LookupResponse
	err = readInto(conn, MsgLookupResponse, &resp)
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("reply to an unknown message type: %v, want a ServerError", err)
	}
}

// TestSigningInputIsUnambiguous guards the length prefixes in
// identity.RendezvousSigningInput: without them, two different endpoint lists
// would produce the same signed bytes and one signature would cover both.
func TestSigningInputIsUnambiguous(t *testing.T) {
	id := newIdentity(t)
	a, err := identity.RendezvousSigningInput(id.DeviceID(), []string{"a", "bc"}, "", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	b, err := identity.RendezvousSigningInput(id.DeviceID(), []string{"ab", "c"}, "", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) == string(b) {
		t.Fatal(`["a","bc"] and ["ab","c"] sign identically: the endpoint list can be re-split under one signature`)
	}

	// And the relay home is not absorbable into the last endpoint either.
	c, err := identity.RendezvousSigningInput(id.DeviceID(), []string{"a"}, "b", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	d, err := identity.RendezvousSigningInput(id.DeviceID(), []string{"ab"}, "", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(c) == string(d) {
		t.Fatal("the relay home can be shifted into an endpoint under one signature")
	}
}

// TestVerifyRegistrationRejectsAMismatchedKey: the DeviceID must derive from
// the key that signed, or a valid signature by any key would do (§2).
func TestVerifyRegistrationRejectsAMismatchedKey(t *testing.T) {
	signer := newIdentity(t)
	other := newIdentity(t)

	sig, err := signer.SignRegistration([]string{"192.0.2.1:9000"}, "", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	err = identity.VerifyRegistration(other.IdentityPublic(), signer.DeviceID(),
		[]string{"192.0.2.1:9000"}, "", 1, 2, sig)
	if !errors.Is(err, identity.ErrRegistrationSignature) {
		t.Fatalf("verifying against another device's key: %v, want ErrRegistrationSignature", err)
	}

	var short ed25519.PublicKey = []byte{1, 2, 3}
	if err := identity.VerifyRegistration(short, signer.DeviceID(), nil, "", 1, 2, sig); err == nil {
		t.Fatal("a three-byte public key was accepted")
	}
}

// TestKeepRegisteredHeartbeats: the loop re-registers on its own, which is what
// makes §16's ten-minute cap workable rather than a countdown to being
// unreachable.
func TestKeepRegisteredHeartbeats(t *testing.T) {
	h := newHarness(t)
	device := newIdentity(t)

	c, err := NewClient(ClientConfig{
		Local:    device,
		Addr:     h.addr,
		ServerID: h.serverID,
		Lifetime: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomicCounter
	go c.KeepRegistered(ctx, func() ([]string, string) {
		calls.add()
		return []string{"192.0.2.1:9000"}, ""
	})

	deadline := time.Now().Add(5 * time.Second)
	for calls.get() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("KeepRegistered never registered")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if at, err := c.LastRegistration(); err != nil || at.IsZero() {
		t.Fatalf("LastRegistration reports %s / %v after a successful heartbeat", at, err)
	}
}

type atomicCounter struct {
	mu sync.Mutex
	n  int
}

func (c *atomicCounter) add() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *atomicCounter) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
