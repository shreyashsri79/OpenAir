package path

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/relay"
)

// newConn builds a Conn on a loopback socket.
func newConn(t *testing.T, id identity.DeviceID) *Conn {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{
		Local: id,
		UDP:   pc,
		Logf:  func(format string, args ...any) { t.Logf("path: "+format, args...) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func deviceID(t *testing.T) identity.DeviceID {
	t.Helper()
	id, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatal(err)
	}
	return id.DeviceID()
}

func localAddrPort(t *testing.T, c *Conn) netip.AddrPort {
	t.Helper()
	ap, ok := addrPortOf(c.LocalAddr())
	if !ok {
		t.Fatalf("cannot read %s as an address", c.LocalAddr())
	}
	return ap
}

// TestProbeAndQUICNeverCollide: the demultiplexing at the top of the read loop
// is the one place where getting it wrong means QUIC packets silently
// disappearing, so it is asserted rather than reasoned about.
func TestProbeAndQUICNeverCollide(t *testing.T) {
	id := deviceID(t)
	probe, err := encodeProbe(probePing, make([]byte, TokenLen), id)
	if err != nil {
		t.Fatal(err)
	}
	if isQUIC(probe) {
		t.Fatal("a probe looks like a QUIC packet")
	}
	if isSTUN(probe) {
		t.Fatal("a probe looks like a STUN message")
	}

	// Every QUIC packet has the fixed bit set (RFC 9000 §17.2/§17.3), and no
	// packet with it set may be read as a probe.
	for _, first := range []byte{0x40, 0x41, 0xC0, 0xC3, 0xFF} {
		pkt := append([]byte{first}, make([]byte, probeSize-1)...)
		if !isQUIC(pkt) {
			t.Fatalf("packet starting %#x is not recognised as QUIC", first)
		}
		if _, _, _, err := decodeProbe(pkt); err == nil {
			t.Fatalf("packet starting %#x decoded as a probe", first)
		}
	}
}

func TestProbeRoundTrip(t *testing.T) {
	id := deviceID(t)
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := encodeProbe(probePong, token, id)
	if err != nil {
		t.Fatal(err)
	}
	kind, gotToken, sender, err := decodeProbe(b)
	if err != nil {
		t.Fatal(err)
	}
	if kind != probePong || string(gotToken) != string(token) || sender != id {
		t.Fatalf("decoded kind %d, sender %s", kind, sender)
	}
	if _, err := encodeProbe(probePing, token[:4], id); err == nil {
		t.Fatal("a short token was accepted")
	}
}

// TestSTUNReportsTheSocketsOwnAddress is the reflexive half of §18. On loopback
// there is no NAT, so the right answer is the socket's own address — which is
// exactly what makes it a usable assertion.
func TestSTUNReportsTheSocketsOwnAddress(t *testing.T) {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := ServeSTUN(ctx, server); err != nil {
			t.Errorf("serve STUN: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		server.Close()
	})

	c := newConn(t, deviceID(t))
	reflexive, err := c.Reflexive(context.Background(), []string{server.LocalAddr().String()})
	if err != nil {
		t.Fatalf("reflexive: %v", err)
	}
	if len(reflexive) != 1 {
		t.Fatalf("got %d reflexive addresses, want 1", len(reflexive))
	}
	if got, want := reflexive[0], localAddrPort(t, c); got != want {
		t.Fatalf("STUN reported %s, the socket is on %s", got, want)
	}
}

func TestSTUNServerIgnoresRubbish(t *testing.T) {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ServeSTUN(ctx, server) //nolint:errcheck // asserted by the server still answering below

	junk, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer junk.Close()
	for _, b := range [][]byte{{}, {0x00}, make([]byte, 64), []byte("not stun at all")} {
		if _, err := junk.WriteTo(b, server.LocalAddr()); err != nil {
			t.Fatal(err)
		}
	}

	// Still answering afterwards is the assertion: a responder that panicked or
	// returned on a malformed packet is one an attacker turns off with a single
	// datagram.
	c := newConn(t, deviceID(t))
	if _, err := c.Reflexive(context.Background(), []string{server.LocalAddr().String()}); err != nil {
		t.Fatalf("the STUN server stopped answering after junk: %v", err)
	}
}

// TestPunchFindsAPath is the mechanism itself: two sockets, one token, and a
// direct address at the end of it.
func TestPunchFindsAPath(t *testing.T) {
	aID, bID := deviceID(t), deviceID(t)
	a, b := newConn(t, aID), newConn(t, bID)

	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type result struct {
		addr netip.AddrPort
		err  error
	}
	aDone := make(chan result, 1)
	go func() {
		addr, err := a.Punch(ctx, bID, token, []netip.AddrPort{localAddrPort(t, b)})
		aDone <- result{addr, err}
	}()

	bAddr, err := b.Punch(ctx, aID, token, []netip.AddrPort{localAddrPort(t, a)})
	if err != nil {
		t.Fatalf("b punch: %v", err)
	}
	if want := localAddrPort(t, a); bAddr != want {
		t.Fatalf("b validated %s, a is on %s", bAddr, want)
	}

	r := <-aDone
	if r.err != nil {
		t.Fatalf("a punch: %v", r.err)
	}
	if want := localAddrPort(t, b); r.addr != want {
		t.Fatalf("a validated %s, b is on %s", r.addr, want)
	}
}

// TestPunchLearnsTheAddressItIsProbedFrom: behind NAT the address a peer's
// probes arrive from is not one either side could have predicted, and probing
// it back is what completes the pair. Here a is told nothing about b at all.
func TestPunchLearnsTheAddressItIsProbedFrom(t *testing.T) {
	aID, bID := deviceID(t), deviceID(t)
	a, b := newConn(t, aID), newConn(t, bID)

	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go b.Punch(ctx, aID, token, []netip.AddrPort{localAddrPort(t, a)}) //nolint:errcheck // a's result is the assertion

	addr, err := a.Punch(ctx, bID, token, nil)
	if err != nil {
		t.Fatalf("a punch with no candidates: %v", err)
	}
	if want := localAddrPort(t, b); addr != want {
		t.Fatalf("a validated %s, b is on %s", addr, want)
	}
}

// TestAProbeWithTheWrongTokenIsIgnored. The token is the only thing that makes
// a probe the peer's rather than a stranger's, so a socket that answers without
// one is a socket anybody can bind an address on.
func TestAProbeWithTheWrongTokenIsIgnored(t *testing.T) {
	aID, bID := deviceID(t), deviceID(t)
	a, b := newConn(t, aID), newConn(t, bID)

	real, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go b.Punch(ctx, aID, real, []netip.AddrPort{localAddrPort(t, a)}) //nolint:errcheck // expected to fail with the mismatched token

	attempt, cancelAttempt := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancelAttempt()
	if addr, err := a.Punch(attempt, bID, wrong, []netip.AddrPort{localAddrPort(t, b)}); err == nil {
		t.Fatalf("a punch with the wrong token validated %s", addr)
	}
}

// TestPromotedRouteRenamesThePeersPackets: this is what makes migration
// invisible. Once a route exists, packets from that address are reported as
// coming from the peer, so the QUIC connection above sees no change of address.
func TestPromotedRouteRenamesThePeersPackets(t *testing.T) {
	aID, bID := deviceID(t), deviceID(t)
	a := newConn(t, aID)

	peer, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	peerAP, _ := addrPortOf(peer.LocalAddr())

	// A QUIC-shaped packet (fixed bit set) before promotion arrives under its
	// raw address.
	quicish := []byte{0x40, 1, 2, 3, 4}
	if _, err := peer.WriteTo(quicish, a.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	a.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, from, err := a.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, isPeer := from.(Addr); isPeer {
		t.Fatalf("an unrouted address was reported as a peer: %s", from)
	}
	if n != len(quicish) {
		t.Fatalf("read %d bytes, wrote %d", n, len(quicish))
	}

	token, _ := NewToken()
	a.Promote(bID, peerAP, token)

	if _, err := peer.WriteTo(quicish, a.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	_, from, err = a.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	pa, ok := from.(Addr)
	if !ok || pa.DeviceID != bID {
		t.Fatalf("after promotion the packet arrived from %s (%T), want %s", from, from, bID)
	}
	if got := a.Class(bID); got != ClassLAN {
		t.Fatalf("class is %q, want %q for a loopback route", got, ClassLAN)
	}

	// And writing to the peer's stable address now leaves by the direct route.
	if _, err := a.WriteTo(quicish, Addr{DeviceID: bID}); err != nil {
		t.Fatalf("write over the promoted route: %v", err)
	}
	peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, 64)
	nr, _, err := peer.ReadFrom(got)
	if err != nil {
		t.Fatalf("the peer never received the packet: %v", err)
	}
	if string(got[:nr]) != string(quicish) {
		t.Fatal("the packet that arrived is not the one sent")
	}
}

// TestWithoutARouteOrARelayThereIsNowhereToSend. The error matters: a caller
// that dialled a DeviceID with neither path available should be told that
// rather than have its packets silently dropped.
func TestWithoutARouteOrARelayThereIsNowhereToSend(t *testing.T) {
	a := newConn(t, deviceID(t))
	if _, err := a.WriteTo([]byte{0x40}, Addr{DeviceID: deviceID(t)}); err == nil {
		t.Fatal("a write to an unreachable peer reported success")
	}
}

// TestCooldownStopsImmediateRepromotion is §18's hysteresis: a path that just
// died is not retried at once, or a marginal one makes the session flap.
func TestCooldownStopsImmediateRepromotion(t *testing.T) {
	a := newConn(t, deviceID(t))
	peer := deviceID(t)

	if !a.CanUpgrade(peer) {
		t.Fatal("a peer with no route and no history cannot be upgraded")
	}
	token, _ := NewToken()
	a.Promote(peer, netip.MustParseAddrPort("192.0.2.7:9000"), token)
	if a.CanUpgrade(peer) {
		t.Fatal("a peer that already has a direct route was offered an upgrade")
	}
	if got, want := a.Class(peer), ClassPunched; got != want {
		t.Fatalf("class of a public direct route is %q, want %q", got, want)
	}

	a.Demote(peer, "test")
	if _, ok := a.Direct(peer); ok {
		t.Fatal("the route survived being demoted")
	}
	if a.CanUpgrade(peer) {
		t.Fatal("a peer whose path just died was immediately re-punched")
	}
}

// TestSprayDelayIgnoresAClockItCannotTrust — see SprayDelay for why start_at
// cannot be read as an absolute instant (D-67).
func TestSprayDelayIgnoresAClockItCannotTrust(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name    string
		startAt time.Time
		want    time.Duration
	}{
		{"unset", time.Time{}, 0},
		{"already passed", now.Add(-time.Second), 0},
		{"just ahead", now.Add(300 * time.Millisecond), 300 * time.Millisecond},
		{"a peer whose clock is a minute fast", now.Add(time.Minute), 0},
		{"a peer whose clock is a day out", now.Add(24 * time.Hour), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SprayDelay(tc.startAt, now); got != tc.want {
				t.Fatalf("SprayDelay = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestParseCandidatesSkipsRubbish(t *testing.T) {
	got := ParseCandidates([]string{
		"10.0.0.4:9000",
		"not an address",
		"10.0.0.5:0",
		"[2001:db8::1]:9000",
		"",
	})
	if len(got) != 2 {
		t.Fatalf("parsed %v, want the two usable candidates", got)
	}
	if FormatCandidates(got)[0] != "10.0.0.4:9000" {
		t.Fatalf("round trip gave %v", FormatCandidates(got))
	}
}

// startRelay runs a relay server and returns a client conn on it.
func startRelay(t *testing.T, local identity.Identity) *relay.PacketConn {
	t.Helper()
	serverID, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := relay.NewServer(relay.Config{
		Local: serverID,
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
			t.Errorf("relay serve: %v", err)
		}
	}()

	pc, err := relay.Dial(context.Background(), relay.ClientConfig{
		Local: local, Addr: ln.Addr().String(), ServerID: serverID.DeviceID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pc.Close()
		cancel()
		<-done
	})
	return pc
}

// TestADeadDirectPathFallsBackToTheRelay. A punched path lives on a NAT mapping
// and a network that can go away; when it does, the session has to end up back
// on the relay rather than on a route to nowhere (§18 step 5).
func TestADeadDirectPathFallsBackToTheRelay(t *testing.T) {
	local, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatal(err)
	}
	c := newConn(t, local.DeviceID())
	peer := deviceID(t)

	token, _ := NewToken()
	c.Promote(peer, netip.MustParseAddrPort("192.0.2.9:9000"), token)

	// With no relay there is nowhere to fall back to, so a quiet route is kept:
	// a path that might recover beats a session that certainly cannot send.
	c.sweep(time.Now().Add(keepaliveTimeout + time.Minute))
	if _, ok := c.Direct(peer); !ok {
		t.Fatal("a route was abandoned with no relay to fall back to")
	}

	c.SetRelay(startRelay(t, local))
	c.sweep(time.Now().Add(keepaliveTimeout + time.Minute))

	if _, ok := c.Direct(peer); ok {
		t.Fatal("a route that received nothing for well over the timeout is still in use")
	}
	if got := c.Class(peer); got != ClassRelayed {
		t.Fatalf("after the fallback the peer's class is %q, want %q", got, ClassRelayed)
	}
	if c.CanUpgrade(peer) {
		t.Fatal("the peer was offered an immediate re-punch, with no hysteresis at all")
	}
}

// TestTrafficKeepsARouteAlive: a route carrying packets is not swept, or a
// working path would be abandoned in the middle of a transfer.
func TestTrafficKeepsARouteAlive(t *testing.T) {
	local, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatal(err)
	}
	c := newConn(t, local.DeviceID())
	c.SetRelay(startRelay(t, local))

	peer := deviceID(t)
	sender, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	senderAP, _ := addrPortOf(sender.LocalAddr())

	token, _ := NewToken()
	c.Promote(peer, senderAP, token)

	if _, err := sender.WriteTo([]byte{0x40, 9, 9, 9}, c.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := c.ReadFrom(buf); err != nil {
		t.Fatal(err)
	}

	c.sweep(time.Now())
	if _, ok := c.Direct(peer); !ok {
		t.Fatal("a route that just carried a packet was abandoned")
	}
}
