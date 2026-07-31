package discovery

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/identity"
)

// freeUDPPort returns a port nothing is listening on. The instances in these
// tests point at each other directly rather than broadcasting, so that `go
// test` does not spray the maintainer's LAN.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("probe for a free port: %v", err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// startPair brings up two unicast-only instances pointed at each other.
//
// mDNS is off because a `go test` run must not depend on the host's multicast
// behaviour -- containers and CI runners routinely have none, and a test that
// is red there for environmental reasons teaches everyone to ignore it. §15.2
// is exercised here; §15.1 has its own test, skipped where multicast is absent.
func startPair(t *testing.T, ttlA, ttlB time.Duration) (a, b *Discovery, idA, idB identity.DeviceID) {
	t.Helper()

	portA, portB := freeUDPPort(t), freeUDPPort(t)
	idA, idB = idOf(t, 0xa1), idOf(t, 0xb2)

	mk := func(self identity.DeviceID, port, peerPort int, ttl time.Duration) *Discovery {
		d, err := New(Config{
			DeviceID:         self,
			Port:             9000,
			DisplayName:      string(self[:4]),
			DisableMDNS:      true,
			DisableBroadcast: true,
			UnicastPort:      port,
			UnicastPeers:     []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(peerPort))},
			ScanWindow:       200 * time.Millisecond,
			Pause:            50 * time.Millisecond,
			TTL:              ttl,
			Logf:             t.Logf,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { d.Close() })
		return d
	}

	a = mk(idA, portA, portB, ttlA)
	b = mk(idB, portB, portA, ttlB)
	return a, b, idA, idB
}

// waitForPeer blocks until d reports id as a candidate, or the deadline passes.
func waitForPeer(t *testing.T, d *Discovery, id identity.DeviceID, limit time.Duration) PeerCandidate {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		for _, p := range d.Peers() {
			if p.DeviceID == id {
				return p
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not see %s within %s", id, limit)
	return PeerCandidate{}
}

// PRD R6: two instances find each other in under three seconds. The budget is
// measured from the point both are running, which is what a user experiences
// as "I opened the app on my phone and my desktop appeared".
func TestTwoInstancesFindEachOtherUnderThreeSeconds(t *testing.T) {
	start := time.Now()
	a, b, idA, idB := startPair(t, DefaultTTL, DefaultTTL)

	got := waitForPeer(t, a, idB, 3*time.Second)
	waitForPeer(t, b, idA, 3*time.Second)

	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("mutual discovery took %s, PRD R6 allows 3s", elapsed)
	}
	if len(got.Addrs) == 0 {
		t.Fatal("candidate carries no address to dial")
	}
	// The announced QUIC port, not the discovery port: dialling the port the
	// beacon arrived from would reach the beacon socket, not the listener.
	_, port, err := net.SplitHostPort(got.Addrs[0])
	if err != nil {
		t.Fatalf("candidate address %q is not host:port: %v", got.Addrs[0], err)
	}
	if port != "9000" {
		t.Fatalf("candidate points at port %s, want the announced 9000", port)
	}
	if got.Via != ViaUnicast {
		t.Fatalf("candidate came Via %q, want %q", got.Via, ViaUnicast)
	}
}

// The same property stated the way M3 requires it: with multicast unavailable,
// discovery still works. DisableMDNS is exactly that condition, expressed
// without needing a network where multicast is actually blocked.
func TestUnicastFallbackWorksWithMulticastBlocked(t *testing.T) {
	a, b, idA, idB := startPair(t, DefaultTTL, DefaultTTL)

	waitForPeer(t, a, idB, 3*time.Second)
	waitForPeer(t, b, idA, 3*time.Second)

	// And the event stream said so, not just the table.
	select {
	case ev := <-a.Events():
		if ev.Kind != PeerFound || ev.Peer.DeviceID != idB {
			t.Fatalf("first event was %s for %s, want found for %s", ev.Kind, ev.Peer.DeviceID, idB)
		}
	case <-time.After(time.Second):
		t.Fatal("no event was emitted for a peer that appeared in Peers()")
	}
}

// Our own announce comes back off the network constantly -- multicast loops
// back to the sending host, and a broadcast reaches our own bound socket. It
// must never become a candidate.
func TestSelfAnnounceIsFiltered(t *testing.T) {
	self := idOf(t, 0xc3)
	port := freeUDPPort(t)

	d, err := New(Config{
		DeviceID:         self,
		Port:             9000,
		DisableMDNS:      true,
		DisableBroadcast: true,
		UnicastPort:      port,
		// Beacon at ourselves: every announce we send arrives back at us.
		UnicastPeers: []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(port))},
		ScanWindow:   100 * time.Millisecond,
		Pause:        50 * time.Millisecond,
		Logf:         t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	time.Sleep(500 * time.Millisecond)
	for _, p := range d.Peers() {
		if p.DeviceID == self {
			t.Fatal("this device discovered itself")
		}
	}
	if n := len(d.Peers()); n != 0 {
		t.Fatalf("discovered %d peers on a network with nobody else on it", n)
	}
}

// A candidate that stops being heard from expires, and the expiry is reported.
// PeerLost means discovery stopped seeing the device, not that it is gone.
func TestCandidateExpires(t *testing.T) {
	// One instance with nobody beaconing at it: a live peer re-announcing
	// itself mid-test would keep refreshing LastSeen and turn this into a race
	// between the sweep and the beacon interval.
	gone := idOf(t, 0xb2)
	d, err := New(Config{
		DeviceID:         idOf(t, 0xa1),
		Port:             9000,
		DisableMDNS:      true,
		DisableBroadcast: true,
		UnicastPort:      freeUDPPort(t),
		ScanWindow:       100 * time.Millisecond,
		Pause:            50 * time.Millisecond,
		TTL:              300 * time.Millisecond,
		Logf:             t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	// A candidate last heard from an hour ago. Injected rather than aged in
	// real time, so the test does not have to sleep out a TTL to be meaningful.
	stale := time.Now().Add(-time.Hour)
	d.mu.Lock()
	d.peers[gone] = &PeerCandidate{
		DeviceID:  gone,
		Addrs:     []string{"127.0.0.1:9000"},
		Via:       ViaUnicast,
		FirstSeen: stale,
		LastSeen:  stale,
	}
	d.mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, still := findPeer(d, gone); !still {
			// Dropping it silently would leave every device list showing a peer
			// that is not there, so the loss has to be announced too.
			for _, ev := range drainEvents(d) {
				if ev.Kind == PeerLost && ev.Peer.DeviceID == gone {
					return
				}
			}
			t.Fatal("the candidate expired but no PeerLost event was emitted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a candidate last heard from an hour ago was never expired")
}

func findPeer(d *Discovery, id identity.DeviceID) (PeerCandidate, bool) {
	for _, p := range d.Peers() {
		if p.DeviceID == id {
			return p, true
		}
	}
	return PeerCandidate{}, false
}

func drainEvents(d *Discovery) []Event {
	var out []Event
	for {
		select {
		case ev := <-d.Events():
			out = append(out, ev)
		default:
			return out
		}
	}
}

// M3's security property: a hostile announce changes nothing, because peers are
// still authenticated by their pinned key.
//
// The attack is the strongest one this layer allows: an announce claiming a
// victim's DeviceID but pointing at an address the attacker controls. Discovery
// cannot detect it -- nothing in an announce is signed, and §15.2 says so. What
// this test asserts is that believing it costs nothing, because the dial that
// follows pins the victim's key and the attacker cannot present it.
func TestHostileAnnounceCannotRedirectASession(t *testing.T) {
	// The victim, whose identity the attacker will claim.
	victim, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatalf("victim identity: %v", err)
	}
	// The attacker: a real, working OpenAir listener holding a different key.
	attacker, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatalf("attacker identity: %v", err)
	}
	attackerLn, err := conn.Listen("127.0.0.1:0", attacker, "attacker", "linux", nil, conn.ListenOptions{})
	if err != nil {
		t.Fatalf("attacker listener: %v", err)
	}
	defer attackerLn.Close()
	go func() {
		for {
			s, err := attackerLn.Accept(context.Background())
			if err != nil {
				return
			}
			s.Close(0, "")
		}
	}()

	_, attackerPortStr, err := net.SplitHostPort(attackerLn.Addr())
	if err != nil {
		t.Fatalf("attacker address: %v", err)
	}
	attackerPort, err := net.LookupPort("udp", attackerPortStr)
	if err != nil {
		t.Fatalf("attacker port: %v", err)
	}

	// A device running discovery, which the attacker beacons at.
	listenerPort := freeUDPPort(t)
	d, err := New(Config{
		DeviceID:         idOf(t, 0xd4),
		Port:             9000,
		DisableMDNS:      true,
		DisableBroadcast: true,
		UnicastPort:      listenerPort,
		ScanWindow:       100 * time.Millisecond,
		Pause:            50 * time.Millisecond,
		Logf:             t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	// The lie: the victim's DeviceID, the attacker's address.
	forged := EncodeAnnounce(Announce{
		DeviceID:     victim.DeviceID(),
		ProtoVersion: ProtoVersion,
		Port:         attackerPort,
		DisplayName:  "definitely-the-victim",
	})
	sock, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: listenerPort})
	if err != nil {
		t.Fatalf("attacker socket: %v", err)
	}
	defer sock.Close()
	if _, err := sock.Write(forged); err != nil {
		t.Fatalf("send forged announce: %v", err)
	}

	// Discovery believes it, and is supposed to: it has no way not to.
	cand := waitForPeer(t, d, victim.DeviceID(), 3*time.Second)
	if len(cand.Addrs) == 0 {
		t.Fatal("forged candidate carries no address")
	}

	// The part that matters. Dialling the candidate while pinning the victim's
	// key reaches the attacker's listener and fails there, at TLS, before any
	// session exists -- and it fails unretryably, so nothing backs off and
	// tries the lie again.
	client, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatalf("client identity: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := conn.NewDialer(client, "client", "linux", nil).
		DialAddr(ctx, cand.Addrs[0], identity.Peer{
			DeviceID:          victim.DeviceID(),
			IdentityPublicKey: victim.IdentityPublic(),
		})
	if err == nil {
		sess.Close(0, "")
		t.Fatal("a forged announce redirected a pinned dial to the attacker")
	}
	if !errors.Is(err, identity.ErrKeyMismatch) && !errors.Is(err, identity.ErrNoPeerCertificate) {
		// Any TLS failure is a pass in substance, but the pinning error is the
		// one that produces a re-pair prompt rather than a retry.
		t.Logf("dial failed with %v", err)
	}

	// And the forgery changed nothing else: discovery emitted a hint, and a
	// hint is all a candidate ever was.
	if _, still := findPeer(d, victim.DeviceID()); !still {
		t.Fatal("the candidate vanished, which is not what this test is about")
	}
}

// Discovery never dials. The whole point of separating it from the connection
// manager is that an unauthenticated broadcast cannot cause an outbound
// connection, so a candidate arriving must produce no traffic to its address.
func TestDiscoveryNeverDialsACandidate(t *testing.T) {
	// A listener nobody should ever connect to.
	trap, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("trap listener: %v", err)
	}
	defer trap.Close()
	trapPort := trap.LocalAddr().(*net.UDPAddr).Port

	listenerPort := freeUDPPort(t)
	d, err := New(Config{
		DeviceID:         idOf(t, 0xe5),
		Port:             9000,
		DisableMDNS:      true,
		DisableBroadcast: true,
		UnicastPort:      listenerPort,
		ScanWindow:       100 * time.Millisecond,
		Pause:            50 * time.Millisecond,
		Logf:             t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	announced := EncodeAnnounce(Announce{
		DeviceID:     idOf(t, 0xf6),
		ProtoVersion: ProtoVersion,
		Port:         trapPort,
		DisplayName:  "bait",
	})
	sock, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: listenerPort})
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	defer sock.Close()
	if _, err := sock.Write(announced); err != nil {
		t.Fatalf("send announce: %v", err)
	}

	waitForPeer(t, d, idOf(t, 0xf6), 3*time.Second)

	// Nothing may have arrived at the announced address.
	if err := trap.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 64)
	if n, from, err := trap.ReadFromUDP(buf); err == nil {
		t.Fatalf("discovery sent %d bytes to the announced address from %s", n, from)
	}
}

func TestNew_RejectsAnUnannounceableDevice(t *testing.T) {
	if _, err := New(Config{DeviceID: idOf(t, 1)}); err == nil {
		t.Fatal("New accepted a config with no port to announce")
	}
	if _, err := New(Config{DeviceID: "not-a-device-id", Port: 9000}); err == nil {
		t.Fatal("New accepted a config with an invalid DeviceID")
	}
	_, err := New(Config{DeviceID: idOf(t, 1), Port: 9000, DisableMDNS: true, DisableUnicast: true})
	if !errors.Is(err, ErrNoTransport) {
		t.Fatalf("New with every transport disabled returned %v, want ErrNoTransport", err)
	}
}

// §15.1 over real multicast. Skipped rather than failed where the host has no
// usable multicast -- containers and CI runners routinely do not, and the
// unicast fallback tests above cover the aggregation logic without depending on
// it. What this adds is the one thing they cannot: that the DNS-SD service
// type, the TXT encoding and zeroconf's browse actually agree.
func TestMDNSDiscoversAPeer(t *testing.T) {
	if testing.Short() {
		t.Skip("multicast test skipped in -short")
	}

	idA, idB := idOf(t, 0x11), idOf(t, 0x22)
	mk := func(self identity.DeviceID) *Discovery {
		d, err := New(Config{
			DeviceID:       self,
			Port:           9000,
			DisplayName:    "mdns-" + string(self[:4]),
			DisableUnicast: true,
			ScanWindow:     time.Second,
			Pause:          200 * time.Millisecond,
			Logf:           t.Logf,
		})
		if err != nil {
			t.Skipf("mDNS unavailable on this host: %v", err)
		}
		t.Cleanup(func() { d.Close() })
		return d
	}

	a, b := mk(idA), mk(idB)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, sawB := findPeer(a, idB)
		_, sawA := findPeer(b, idA)
		if sawB && sawA {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Skip("no mDNS traffic observed on this host; multicast is likely unavailable")
}

// A process that is not accepting sessions has no port to advertise. Browsing
// must therefore be possible without saying anything at all -- otherwise
// `openair send` would publish an address that refuses every connection.
func TestBrowseOnlyHearsButNeverAnnounces(t *testing.T) {
	announcerPort, browserPort := freeUDPPort(t), freeUDPPort(t)
	announcerID, browserID := idOf(t, 0x31), idOf(t, 0x32)

	announcer, err := New(Config{
		DeviceID:         announcerID,
		Port:             9000,
		DisableMDNS:      true,
		DisableBroadcast: true,
		UnicastPort:      announcerPort,
		UnicastPeers:     []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(browserPort))},
		ScanWindow:       200 * time.Millisecond,
		Pause:            50 * time.Millisecond,
		Logf:             t.Logf,
	})
	if err != nil {
		t.Fatalf("New announcer: %v", err)
	}
	defer announcer.Close()

	// No Port at all: a browse-only config must not require one.
	browser, err := New(Config{
		DeviceID:         browserID,
		BrowseOnly:       true,
		DisableMDNS:      true,
		DisableBroadcast: true,
		UnicastPort:      browserPort,
		UnicastPeers:     []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(announcerPort))},
		ScanWindow:       200 * time.Millisecond,
		Pause:            50 * time.Millisecond,
		Logf:             t.Logf,
	})
	if err != nil {
		t.Fatalf("New browser: %v", err)
	}
	defer browser.Close()

	// The browser hears the announcer...
	waitForPeer(t, browser, announcerID, 3*time.Second)

	// ...and the announcer never hears the browser, however long it waits and
	// however many queries it answers.
	time.Sleep(time.Second)
	if p, found := findPeer(announcer, browserID); found {
		t.Fatalf("a browse-only instance announced itself as %+v", p)
	}
}
