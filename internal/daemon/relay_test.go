package daemon

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/relay"
)

// startRelayServer runs a relay for the test.
func startRelayServer(t *testing.T) RelayConfig {
	t.Helper()

	id, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := relay.NewServer(relay.Config{
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
			t.Errorf("relay serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return RelayConfig{Addr: ln.Addr().String(), ServerID: id.DeviceID()}
}

// TestTransferThroughRelayWithNoDirectPath is M8 as a user meets it: two
// daemons that cannot reach each other at all, and a file that arrives anyway.
//
// "Cannot reach each other" is arranged honestly rather than simulated. Neither
// daemon runs discovery, neither is registered with a rendezvous server, and
// the send names a DeviceID rather than an address -- so there is no address
// for the sender to try and the relay is the only path that exists.
func TestTransferThroughRelayWithNoDirectPath(t *testing.T) {
	rl := startRelayServer(t)

	sender := newTestDaemon(t, func(cfg *Config) { cfg.Relay = rl })
	receiver := newTestDaemon(t, func(cfg *Config) {
		cfg.Relay = rl
		cfg.AutoAccept = true
	})
	pinEachOther(t, sender, receiver)

	waitFor(t, "both daemons to reach the relay", func() bool {
		return sender.relayPacketConn() != nil && receiver.relayPacketConn() != nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	src := writeFile(t, t.TempDir(), "relayed.txt", strings.Repeat("through the relay\n", 500))
	sc := connect(t, sender, nil, nil)

	if _, err := sc.Send(ctx, string(receiver.DeviceID()), []string{src}); err != nil {
		t.Fatalf("send over the relay: %v", err)
	}

	got := filepath.Join(receiver.cfg.DestDir, "relayed.txt")
	if fileDigest(t, got) != fileDigest(t, src) {
		t.Fatal("the file that arrived is not the file that was sent")
	}
}

// TestRelayHomeIsPublishedOnlyWhileConnected: §16's relay_home tells peers
// where to reach this device when nothing direct works, so advertising one this
// device is not connected to would send them to a forwarder with no mailbox for
// it.
func TestRelayHomeIsPublishedOnlyWhileConnected(t *testing.T) {
	rl := startRelayServer(t)

	withRelay := newTestDaemon(t, func(cfg *Config) { cfg.Relay = rl })
	waitFor(t, "the relay connection", func() bool { return withRelay.relayPacketConn() != nil })

	home := withRelay.relayHome()
	if !strings.HasPrefix(home, rl.Addr+"@") {
		t.Fatalf("relay home is %q, want %s@<device id>", home, rl.Addr)
	}
	if !strings.Contains(home, string(rl.ServerID)) {
		t.Fatalf("relay home %q does not name the relay's device id", home)
	}

	// A daemon with no relay configured publishes nothing, rather than an empty
	// address a peer would try to parse.
	without := newTestDaemon(t, nil)
	if got := without.relayHome(); got != "" {
		t.Fatalf("a daemon with no relay published %q", got)
	}
	if without.relayPacketConn() != nil {
		t.Fatal("a daemon with no relay holds a relay connection")
	}
}

// TestRelayRefusesAnUnpairedTarget: a relayed path addresses a DeviceID, and
// the pinned key is what makes the far end verifiable. An unpaired device has
// no key here, so there is nothing to check and the attempt is refused.
func TestRelayRefusesAnUnpairedTarget(t *testing.T) {
	rl := startRelayServer(t)

	d := newTestDaemon(t, func(cfg *Config) { cfg.Relay = rl })
	stranger := newTestDaemon(t, func(cfg *Config) { cfg.Relay = rl })

	waitFor(t, "both daemons to reach the relay", func() bool {
		return d.relayPacketConn() != nil && stranger.relayPacketConn() != nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := d.dialViaRelay(ctx, stranger.DeviceID()); err == nil {
		t.Fatal("a relayed session was opened to an unpaired device")
	}
	if _, err := d.tryRelay(ctx, string(stranger.DeviceID())); err == nil {
		t.Fatal("tryRelay opened a session to an unpaired device")
	}
}

// TestParseRelay is the flag form. It shares ParseRendezvous's rules, and the
// test says so rather than assuming a reader will notice.
func TestParseRelay(t *testing.T) {
	id, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := ParseRelay("relay.example:9444@" + string(id.DeviceID()))
	if err != nil {
		t.Fatalf("ParseRelay: %v", err)
	}
	if cfg.Addr != "relay.example:9444" || cfg.ServerID != id.DeviceID() {
		t.Fatalf("parsed %+v", cfg)
	}
	if _, err := ParseRelay("relay.example:9444"); err == nil {
		t.Fatal("a relay address with no pinned device id was accepted")
	}
	if cfg, err := ParseRelay(""); err != nil || cfg.Addr != "" {
		t.Fatalf("the empty string should disable relaying, got %+v / %v", cfg, err)
	}
}
