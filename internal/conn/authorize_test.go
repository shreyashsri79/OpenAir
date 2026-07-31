package conn

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

var errNotYou = errors.New("test: peer not on the guest list")

// TestAccept_AuthorizeRejectsInboundPeer is the regression test for the hole
// M1a found: the pinned-key comparison inside session.New only fires when
// Config.Peer is already populated, which a listener cannot do, so before
// Config.Authorize existed every inbound connection was admitted
// unconditionally no matter who was calling.
func TestAccept_AuthorizeRejectsInboundPeer(t *testing.T) {
	server := newTestIdentity(t)
	client := newTestIdentity(t)

	var sawPeer identity.Peer
	ln, err := Listen("127.0.0.1:0", server, "server", "linux", nil,
		func(p identity.Peer) error {
			sawPeer = p
			return errNotYou
		})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	acceptErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := ln.Accept(ctx)
		acceptErr <- err
	}()

	d := NewDialer(client, "client", "linux", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The dial itself may succeed -- rejection happens after Hello, so the
	// initiator does not necessarily learn of it synchronously. What matters is
	// that the accepting side produced no usable session.
	if s, err := d.DialAddr(ctx, ln.Addr(), identity.Peer{IdentityPublicKey: server.IdentityPublic()}); err == nil {
		defer s.Close(0, "test done")
	}

	select {
	case err := <-acceptErr:
		if err == nil {
			t.Fatal("Accept returned a session for a peer the callback rejected")
		}
		if !errors.Is(err, errNotYou) {
			t.Errorf("Accept error = %v, want it to wrap the callback's error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Accept did not return within 10s")
	}

	// The callback must be able to make a real decision, which means it must
	// see an identified peer -- not a zero record. The DeviceID is derived from
	// the TLS key rather than taken from Hello, so it is the peer's real one.
	if want := identity.DeriveDeviceID(client.IdentityPublic()); sawPeer.DeviceID != want {
		t.Errorf("Authorize saw DeviceID %q, want %q", sawPeer.DeviceID, want)
	}
	if !sawPeer.IdentityPublicKey.Equal(client.IdentityPublic()) {
		t.Error("Authorize saw the wrong identity key for the calling peer")
	}
}

// TestAccept_AuthorizeAdmitsInboundPeer is the other half: a callback that
// returns nil must leave a working session, so that the gate cannot be
// mistaken for a blanket refusal.
func TestAccept_AuthorizeAdmitsInboundPeer(t *testing.T) {
	server := newTestIdentity(t)
	client := newTestIdentity(t)

	called := make(chan identity.Peer, 1)
	ln, err := Listen("127.0.0.1:0", server, "server", "linux", nil,
		func(p identity.Peer) error {
			called <- p
			return nil
		})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	type result struct {
		sess session.Session
		err  error
	}
	acceptCh := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s, err := ln.Accept(ctx)
		acceptCh <- result{s, err}
	}()

	d := NewDialer(client, "client", "linux", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientSess, err := d.DialAddr(ctx, ln.Addr(), identity.Peer{IdentityPublicKey: server.IdentityPublic()})
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	defer clientSess.Close(0, "test done")

	select {
	case got := <-acceptCh:
		if got.err != nil {
			t.Fatalf("Accept: %v", got.err)
		}
		defer got.sess.Close(0, "test done")
		if got.sess.Peer().DeviceID != identity.DeriveDeviceID(client.IdentityPublic()) {
			t.Error("accepted session reports the wrong peer")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Accept did not return within 10s")
	}

	select {
	case <-called:
	default:
		t.Error("Authorize was never called on the accept path")
	}
}
