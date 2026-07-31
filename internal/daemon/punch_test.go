package daemon

import (
	"context"
	"crypto/rand"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/caps/files"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/path"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// M9 as a user meets it: a session that started on the relay stops using it,
// and a transfer that is running while that happens does not notice.

// relayedPair brings up a relay and two daemons that can only reach each other
// through it: no discovery, no rendezvous, and a send addressed by DeviceID.
func relayedPair(t *testing.T) (sender, receiver *Daemon) {
	t.Helper()
	rl := startRelayServer(t)

	sender = newTestDaemon(t, func(cfg *Config) { cfg.Relay = rl })
	receiver = newTestDaemon(t, func(cfg *Config) {
		cfg.Relay = rl
		cfg.AutoAccept = true
	})
	pinEachOther(t, sender, receiver)

	waitFor(t, "both daemons to reach the relay", func() bool {
		return sender.relayPacketConn() != nil && receiver.relayPacketConn() != nil
	})
	return sender, receiver
}

// TestARelayedSessionMovesOffTheRelay is the milestone: the session starts on
// the relay because that is what is available (§18 step 2), and ends up direct
// because the two ends punched their way there (step 3 and step 4).
func TestARelayedSessionMovesOffTheRelay(t *testing.T) {
	sender, receiver := relayedPair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	src := writeFile(t, t.TempDir(), "punched.txt", strings.Repeat("off the relay\n", 500))
	sc := connect(t, sender, nil, nil)
	if _, err := sc.Send(ctx, string(receiver.DeviceID()), []string{src}); err != nil {
		t.Fatalf("send over the relay: %v", err)
	}

	// Both ends have to promote: each one's own packets take its own route, and
	// a path that only works in one direction is not a path.
	waitFor(t, "the sender to find a direct path", func() bool {
		_, ok := sender.paths.Direct(receiver.DeviceID())
		return ok
	})
	waitFor(t, "the receiver to find a direct path", func() bool {
		_, ok := receiver.paths.Direct(sender.DeviceID())
		return ok
	})

	// §7.2's hint follows the path rather than the session: a capability asking
	// now is told it is on a LAN path, having been told "relayed" before.
	sess, ok := sender.sessionFor(receiver.DeviceID())
	if !ok {
		t.Fatal("the session with the receiver is gone")
	}
	if got := sess.PathInfo().Class; got != path.ClassLAN {
		t.Fatalf("path class is %q after the upgrade, want %q", got, path.ClassLAN)
	}

	// And the session still works, over the path it moved to.
	second := writeFile(t, t.TempDir(), "after.txt", strings.Repeat("direct now\n", 500))
	if _, err := sc.Send(ctx, string(receiver.DeviceID()), []string{second}); err != nil {
		t.Fatalf("send after the upgrade: %v", err)
	}
	got := filepath.Join(receiver.cfg.DestDir, "after.txt")
	if fileDigest(t, got) != fileDigest(t, second) {
		t.Fatal("the file that arrived after the upgrade is not the file that was sent")
	}
}

// TestATransferSurvivesTheMigration is PRD R9's actual promise: the path
// changes underneath a running transfer and the transfer does not restart,
// stall or corrupt.
//
// The migration is forced here rather than waited for, so that it lands in the
// middle of the transfer instead of wherever the punch happens to finish. What
// it exercises is the same code the punch drives: Promote, and the routing
// underneath a live QUIC connection.
func TestATransferSurvivesTheMigration(t *testing.T) {
	sender, receiver := relayedPair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	payload := make([]byte, 24<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "migrating.bin")
	if err := os.WriteFile(src, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(receiver.cfg.DestDir, "migrating.bin")
	part := dest + files.PartSuffix

	// Timed off the receiver's part file rather than off a progress event:
	// §8.5's progress runs at 1 Hz and a transfer this size over loopback is
	// finished long before the first tick. The part file exists for exactly as
	// long as the transfer is unfinished -- it is renamed into place at the end
	// -- so seeing it still there straight after the switch is what proves the
	// path changed under a live transfer rather than after one.
	var (
		mu       sync.Mutex
		switched bool
	)
	watching := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(watching)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := os.Stat(part); err == nil {
				forceDirect(t, sender, receiver)
				_, stillRunning := os.Stat(part)
				mu.Lock()
				switched = stillRunning == nil
				mu.Unlock()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	sc := connect(t, sender, nil, nil)
	if _, err := sc.Send(ctx, string(receiver.DeviceID()), []string{src}); err != nil {
		t.Fatalf("send across a migration: %v", err)
	}
	close(stop)
	<-watching

	mu.Lock()
	migratedMidTransfer := switched
	mu.Unlock()
	if !migratedMidTransfer {
		t.Fatal("the transfer finished before the path could be switched, so no migration was tested")
	}

	if fileDigest(t, dest) != fileDigest(t, src) {
		t.Fatal("the file that arrived across the migration is not the file that was sent")
	}
	if _, ok := sender.paths.Direct(receiver.DeviceID()); !ok {
		t.Fatal("the sender is not on the direct path it was switched to")
	}
}

// forceDirect promotes each daemon onto the other's real socket address.
func forceDirect(t *testing.T, a, b *Daemon) {
	t.Helper()
	token := make([]byte, path.TokenLen)
	if _, err := rand.Read(token); err != nil {
		t.Error(err)
		return
	}
	aAddr, err := netip.ParseAddrPort(a.paths.LocalAddr().String())
	if err != nil {
		t.Error(err)
		return
	}
	bAddr, err := netip.ParseAddrPort(b.paths.LocalAddr().String())
	if err != nil {
		t.Error(err)
		return
	}
	a.paths.Promote(b.DeviceID(), bAddr, token)
	b.paths.Promote(a.DeviceID(), aAddr, token)
}

// TestOnlyOneSideSignalsThePunch. Both signalling would work, but it means two
// tokens and two sprays for one pair of addresses, and each peer answering a
// request it also sent. The rule is arbitrary; being agreed is the point.
func TestOnlyOneSideSignalsThePunch(t *testing.T) {
	a := identity.DeviceID("aaaaaaaaaaaaaaaa")
	b := identity.DeviceID("bbbbbbbbbbbbbbbb")

	if !initiatesPunch(a, b) {
		t.Fatal("the lower device id does not signal")
	}
	if initiatesPunch(b, a) {
		t.Fatal("both sides signal")
	}
	if initiatesPunch(a, a) {
		t.Fatal("a device signalled a punch to itself")
	}
	if initiatesPunch(a, "not-a-device-id") {
		t.Fatal("a punch was signalled towards something that is not a device")
	}
}

// TestPunchRequestForSomebodyElseIsRefused: a request names its target, and a
// device that punched on behalf of a request addressed elsewhere would be
// opening a NAT mapping for a conversation it is not in.
func TestPunchRequestForSomebodyElseIsRefused(t *testing.T) {
	d := newTestDaemon(t, nil)
	stranger := newTestDaemon(t, nil)

	token := make([]byte, path.TokenLen)
	payload, err := proto.Marshal(&openairv1.PunchRequest{
		TargetDeviceId: string(stranger.DeviceID()),
		PunchToken:     token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.onPunchRequest(context.Background(), nil, payload); err == nil {
		t.Fatal("a punch request addressed to another device was accepted")
	}

	short, err := proto.Marshal(&openairv1.PunchRequest{
		TargetDeviceId: string(d.DeviceID()),
		PunchToken:     []byte{1, 2, 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.onPunchRequest(context.Background(), nil, short); err == nil {
		t.Fatal("a punch request with a three-byte token was accepted")
	}
}

// TestPunchReadyForAnUnknownTokenIsIgnored. It is not an error: an answer that
// arrives after the initiator gave up is ordinary, and failing the session over
// it would turn a late packet into a dropped connection.
func TestPunchReadyForAnUnknownTokenIsIgnored(t *testing.T) {
	d := newTestDaemon(t, nil)
	payload, err := proto.Marshal(&openairv1.PunchReady{PunchToken: make([]byte, path.TokenLen)})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.onPunchReady(payload); err != nil {
		t.Fatalf("a stale punch answer was treated as an error: %v", err)
	}
}

// TestCandidatesIncludeTheReflexiveAddress: without STUN, a device behind NAT
// offers only addresses nobody outside can use. The rendezvous server answers
// STUN on its own port, so configuring one is enough (D-68).
func TestCandidatesIncludeTheReflexiveAddress(t *testing.T) {
	rvAddr, rvID := startRendezvousServer(t)
	rv := RendezvousConfig{Addr: rvAddr, ServerID: rvID}
	d := newTestDaemon(t, func(cfg *Config) { cfg.Rendezvous = rv })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	candidates := d.localCandidates(ctx)
	if len(candidates) == 0 {
		t.Fatal("this device offered no candidates at all")
	}
	// On loopback the reflexive address is the socket's own, so the assertion
	// is that STUN was asked and answered rather than that it revealed
	// something new.
	want := d.paths.LocalAddr().String()
	var found bool
	for _, c := range candidates {
		if c == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("candidates %v do not include this socket's address %s", candidates, want)
	}

	if got := d.stunServers(); len(got) != 1 || got[0] != rv.Addr {
		t.Fatalf("stun servers = %v, want the rendezvous server %s", got, rv.Addr)
	}
}

// TestADaemonWithNoRelayNeverPunches: punching exists to get off a relay. A
// session that was never on one is already as direct as it gets.
func TestADaemonWithNoRelayNeverPunches(t *testing.T) {
	a := newTestDaemon(t, nil)
	b := newTestDaemon(t, func(cfg *Config) { cfg.AutoAccept = true })
	pinEachOther(t, a, b)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	src := writeFile(t, t.TempDir(), "direct.txt", "no relay here\n")
	ac := connect(t, a, nil, nil)
	if _, err := ac.Send(ctx, b.Addr(), []string{src}); err != nil {
		t.Fatalf("direct send: %v", err)
	}

	if class := a.paths.Class(b.DeviceID()); class != "" {
		t.Fatalf("a directly dialled peer is carried by DeviceID (class %q)", class)
	}
	sess, ok := a.sessionFor(b.DeviceID())
	if !ok {
		t.Fatal("no session with b")
	}
	if got := sess.PathInfo().Class; got != path.ClassLAN {
		t.Fatalf("a direct session reports path class %q, want %q", got, path.ClassLAN)
	}
}

// TestPathClassIsRelayedBeforeTheUpgrade. The hint has to be right at the
// moment it is asked for, which for a fresh relayed session is before any
// punch has finished.
func TestPathClassIsRelayedBeforeTheUpgrade(t *testing.T) {
	sender, receiver := relayedPair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sess, err := sender.dialViaRelay(ctx, receiver.DeviceID())
	if err != nil {
		t.Fatalf("dial over the relay: %v", err)
	}
	defer sess.Close(0, "test over")

	if got := sess.PathInfo().Class; got != path.ClassRelayed {
		t.Fatalf("a relayed session reports path class %q, want %q", got, path.ClassRelayed)
	}
	if got := sender.paths.Class(receiver.DeviceID()); got != path.ClassRelayed {
		t.Fatalf("the path conn says %q, want %q", got, path.ClassRelayed)
	}
}
