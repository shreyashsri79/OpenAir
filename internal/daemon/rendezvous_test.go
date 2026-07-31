package daemon

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/rendezvous"
)

// startRendezvousServer runs a server for the test and returns the flag string
// a daemon would be given.
func startRendezvousServer(t *testing.T) (addr string, serverID identity.DeviceID) {
	t.Helper()

	id, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := rendezvous.NewServer(rendezvous.Config{
		Local: id,
		Logf:  func(format string, args ...any) { t.Logf("rendezvous: "+format, args...) },
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
			t.Errorf("rendezvous serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return ln.Addr().String(), id.DeviceID()
}

// waitFor polls until cond holds, or fails the test. Registration happens on
// its own goroutine, so there is no handle to wait on and no callback to hook.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestTransferFoundThroughRendezvous is M7's point, end to end: two daemons
// with LAN discovery switched off entirely, one sending to the other by name.
//
// Discovery being off is not a convenience here -- it is the scenario. On the
// same network the LAN answer wins and rendezvous is never consulted, so the
// only way to prove the rendezvous path works is to remove the other one.
func TestTransferFoundThroughRendezvous(t *testing.T) {
	addr, serverID := startRendezvousServer(t)
	rv := RendezvousConfig{Addr: addr, ServerID: serverID}

	sender := newTestDaemon(t, func(cfg *Config) { cfg.Rendezvous = rv })
	receiver := newTestDaemon(t, func(cfg *Config) {
		cfg.Rendezvous = rv
		cfg.AutoAccept = true // nobody is watching; M6's owned path has its own test
	})
	pinEachOther(t, sender, receiver)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Wait for the receiver's first registration to land, which is what a
	// sender will look up.
	waitFor(t, "the receiver to register", func() bool {
		c := receiver.rendezvousClient()
		if c == nil {
			return false
		}
		at, _ := c.LastRegistration()
		return !at.IsZero()
	})

	src := writeFile(t, t.TempDir(), "across.txt", strings.Repeat("over the internet\n", 200))
	sc := connect(t, sender, nil, nil)

	// By DeviceID, not by address: resolution has to go through the rendezvous
	// server, because discovery is disabled on both daemons.
	if _, err := sc.Send(ctx, string(receiver.DeviceID()), []string{src}); err != nil {
		t.Fatalf("send resolved through rendezvous: %v", err)
	}

	got := filepath.Join(receiver.cfg.DestDir, "across.txt")
	if fileDigest(t, got) != fileDigest(t, src) {
		t.Fatal("the file that arrived is not the file that was sent")
	}
}

// TestRendezvousLookupNeedsAPairedPeer: the answer is only usable if there is a
// pinned key to check it against, so an unpaired device is refused here rather
// than dialled on a stranger's say-so.
func TestRendezvousLookupNeedsAPairedPeer(t *testing.T) {
	addr, serverID := startRendezvousServer(t)
	rv := RendezvousConfig{Addr: addr, ServerID: serverID}

	d := newTestDaemon(t, func(cfg *Config) { cfg.Rendezvous = rv })
	stranger := newTestDaemon(t, func(cfg *Config) { cfg.Rendezvous = rv })

	waitFor(t, "the stranger to register", func() bool {
		c := stranger.rendezvousClient()
		if c == nil {
			return false
		}
		at, _ := c.LastRegistration()
		return !at.IsZero()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := d.lookupPeer(ctx, stranger.DeviceID())
	if err == nil {
		t.Fatal("an unpaired device was looked up and returned")
	}
	if !strings.Contains(err.Error(), "not paired") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// TestDaemonWithoutRendezvousSaysSo: with no server configured, a name that is
// not on the LAN fails with advice rather than a nil-pointer surprise.
func TestDaemonWithoutRendezvousSaysSo(t *testing.T) {
	d := newTestDaemon(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := d.targetAddrs(ctx, "some-device-elsewhere")
	if err == nil {
		t.Fatal("resolving an unknown name succeeded")
	}
	if _, lookupErr := d.lookupPeer(ctx, d.DeviceID()); lookupErr == nil {
		t.Fatal("lookupPeer succeeded with no rendezvous configured")
	}
}

// TestParseRendezvous covers the flag form, including the two ways of getting
// it wrong that a user will actually hit.
func TestParseRendezvous(t *testing.T) {
	id, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatal(err)
	}
	want := id.DeviceID()

	got, err := ParseRendezvous("rv.example:9443@" + string(want))
	if err != nil {
		t.Fatalf("ParseRendezvous: %v", err)
	}
	if got.Addr != "rv.example:9443" || got.ServerID != want {
		t.Fatalf("parsed %+v, want rv.example:9443 and %s", got, want)
	}

	// The grouped fingerprint form, which is what a user copies off a screen.
	grouped, err := ParseRendezvous("rv.example:9443@" + want.Fingerprint())
	if err != nil {
		t.Fatalf("ParseRendezvous with a grouped fingerprint: %v", err)
	}
	if grouped.ServerID != want {
		t.Fatalf("grouped form parsed to %s, want %s", grouped.ServerID, want)
	}

	if _, err := ParseRendezvous("rv.example:9443"); err == nil {
		t.Fatal("an address with no server id was accepted: any host answering it could impersonate the server")
	}
	if _, err := ParseRendezvous("rv.example:9443@not-a-device-id"); err == nil {
		t.Fatal("a malformed server id was accepted")
	}
	if cfg, err := ParseRendezvous(""); err != nil || cfg.Addr != "" {
		t.Fatalf("the empty string should disable rendezvous, got %+v / %v", cfg, err)
	}
}
