package relay_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/files"
	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/relay"
	"github.com/shreyashsri79/openair/internal/session"
)

// This file is the point of M8: a real QUIC session, and a real file transfer,
// between two devices that never exchange a packet directly.
//
// It lives in relay_test (the external test package) because it reaches across
// conn, session and caps/files -- if the relay were only tested against itself,
// the thing that would go untested is exactly the claim being made, which is
// that everything above it is unchanged.

func newIdentity(t *testing.T) *identity.FileIdentity {
	t.Helper()
	id, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// startRelay runs a relay and returns its address and DeviceID.
func startRelay(t *testing.T) (string, identity.DeviceID) {
	t.Helper()
	id := newIdentity(t)
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
	return ln.Addr().String(), id.DeviceID()
}

func pin(t *testing.T, store identity.TrustStore, peer *identity.FileIdentity) {
	t.Helper()
	err := store.Put(identity.Peer{
		DeviceID:          peer.DeviceID(),
		IdentityPublicKey: peer.IdentityPublic(),
		DisplayName:       "peer",
		Platform:          "linux",
		Level:             identity.LevelTrusted,
		AuthPolicy:        identity.PolicyTimed,
		CreatedAt:         1,
		LastSeen:          1,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestQUICSessionOverRelay is the whole milestone in one test: two devices, a
// relay between them, no direct path at all, and a file that arrives intact.
//
// Nothing above the packet conn knows the difference. The session does its
// usual Hello, the peer is authenticated by its pinned key, and the transfer is
// the same files capability -- which is what "the relay is a network element,
// not a participant" has to mean if it is to mean anything (PRD R27).
func TestQUICSessionOverRelay(t *testing.T) {
	relayAddr, relayID := startRelay(t)

	senderID := newIdentity(t)
	receiverID := newIdentity(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Both devices connect to the relay. This is what a device behind a NAT
	// does at startup: the relay path is available immediately, which §18 leans
	// on so a session is usable within a round trip rather than after a punch.
	receiverPC, err := relay.Dial(ctx, relay.ClientConfig{
		Local: receiverID, Addr: relayAddr, ServerID: relayID,
	})
	if err != nil {
		t.Fatalf("receiver dial relay: %v", err)
	}
	defer receiverPC.Close()

	senderPC, err := relay.Dial(ctx, relay.ClientConfig{
		Local: senderID, Addr: relayAddr, ServerID: relayID,
	})
	if err != nil {
		t.Fatalf("sender dial relay: %v", err)
	}
	defer senderPC.Close()

	// The receiving side listens *on the relay conn*. A device that only
	// dialled would be reachable by nobody.
	destDir := t.TempDir()
	recvCap := files.New(files.Config{
		DestRoot: destDir,
		Accept:   func(context.Context, identity.Peer, files.Offer) (bool, error) { return true, nil },
	})
	recvStore := openStore(t)
	pin(t, recvStore, senderID)

	ln, err := conn.ListenPacketConn(receiverPC, receiverID, "receiver", "linux",
		map[byte]session.Handler{files.CapID: recvCap},
		conn.ListenOptions{
			Authorize: func(p identity.Peer) error { return authorizeAgainst(recvStore, p) },
		})
	if err != nil {
		t.Fatalf("listen over the relay: %v", err)
	}
	defer ln.Close()

	accepted := make(chan session.Session, 1)
	go func() {
		s, err := ln.Accept(ctx)
		if err != nil {
			t.Errorf("accept over the relay: %v", err)
			return
		}
		accepted <- s
	}()

	// And the sender dials the receiver by DeviceID, which is the only address
	// a relayed path has.
	sendCap := files.New(files.Config{DestRoot: t.TempDir()})
	sendStore := openStore(t)
	pin(t, sendStore, receiverID)
	pinned, _ := sendStore.Get(receiverID.DeviceID())

	sess, err := conn.DialPacketConn(ctx, senderPC, relay.Addr{DeviceID: receiverID.DeviceID()},
		senderID, "sender", "linux", map[byte]session.Handler{files.CapID: sendCap}, pinned)
	if err != nil {
		t.Fatalf("dial the peer over the relay: %v", err)
	}
	defer sess.Close(0, "test over")

	select {
	case <-accepted:
	case <-time.After(30 * time.Second):
		t.Fatal("the receiving side never accepted a session over the relay")
	}

	// A payload big enough to need many packets, so this exercises the relay as
	// a path rather than as a single round trip.
	src := filepath.Join(t.TempDir(), "relayed.bin")
	payload := make([]byte, 512<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := sendCap.Send(ctx, sess, []files.Item{{LocalPath: src, RelPath: "relayed.bin"}}); err != nil {
		t.Fatalf("send over the relay: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "relayed.bin"))
	if err != nil {
		t.Fatalf("the file never arrived: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("the file that arrived differs: %s vs %s", digest(got), digest(payload))
	}
}

// TestRelayedPeerIsStillAuthenticated: the relay chooses who to forward to, so
// it could forward to the wrong device. The pinned key is what makes that a
// failed handshake rather than a wrong session.
func TestRelayedPeerIsStillAuthenticated(t *testing.T) {
	relayAddr, relayID := startRelay(t)

	senderID := newIdentity(t)
	receiverID := newIdentity(t)
	strangerID := newIdentity(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	receiverPC, err := relay.Dial(ctx, relay.ClientConfig{Local: receiverID, Addr: relayAddr, ServerID: relayID})
	if err != nil {
		t.Fatal(err)
	}
	defer receiverPC.Close()
	senderPC, err := relay.Dial(ctx, relay.ClientConfig{Local: senderID, Addr: relayAddr, ServerID: relayID})
	if err != nil {
		t.Fatal(err)
	}
	defer senderPC.Close()

	ln, err := conn.ListenPacketConn(receiverPC, receiverID, "receiver", "linux",
		map[byte]session.Handler{}, conn.ListenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go ln.Accept(ctx) //nolint:errcheck // the dial is expected to fail

	// Dial the receiver's DeviceID while pinning somebody else's key: this is
	// what a relay delivering to the wrong mailbox would look like from here.
	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()
	_, err = conn.DialPacketConn(dialCtx, senderPC, relay.Addr{DeviceID: receiverID.DeviceID()},
		senderID, "sender", "linux", map[byte]session.Handler{},
		identity.Peer{
			DeviceID:          strangerID.DeviceID(),
			IdentityPublicKey: strangerID.IdentityPublic(),
		})
	if err == nil {
		t.Fatal("a relayed session completed against a peer whose key was not the pinned one")
	}
}

func openStore(t *testing.T) identity.TrustStore {
	t.Helper()
	store, err := identity.OpenTrustStore(filepath.Join(t.TempDir(), "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func authorizeAgainst(store identity.TrustStore, p identity.Peer) error {
	if _, ok := store.Get(p.DeviceID); !ok {
		return errNotPaired
	}
	return nil
}

var errNotPaired = errNotPairedType{}

type errNotPairedType struct{}

func (errNotPairedType) Error() string { return "not paired" }

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
