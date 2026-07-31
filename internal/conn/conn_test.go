package conn

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

// newTestIdentity builds a throwaway on-disk identity (TierNone: no privilege
// key, irrelevant to a plain dial/accept test) for these tests.
//
// M1b's identity.LoadOrCreate and M1a's session.New have both landed since
// this package was first written, so these are now real integration tests
// across conn + session + identity + congestion, not stubs at the QUIC/TLS
// layer only.
func newTestIdentity(t *testing.T) *identity.FileIdentity {
	t.Helper()
	id, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	return id
}

func TestDialAccept_CorrectPinnedKey_Succeeds(t *testing.T) {
	server := newTestIdentity(t)
	client := newTestIdentity(t)

	ln, err := Listen("127.0.0.1:0", server, "server", "linux", nil, nil)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	type acceptResult struct {
		sess session.Session
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s, err := ln.Accept(ctx)
		acceptCh <- acceptResult{s, err}
	}()

	d := NewDialer(client, "client", "linux", nil)
	pinned := identity.Peer{IdentityPublicKey: server.IdentityPublic()}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientSess, err := d.DialAddr(ctx, ln.Addr(), pinned)
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	defer clientSess.Close(0, "test done")

	res := <-acceptCh
	if res.err != nil {
		t.Fatalf("Accept: %v", res.err)
	}
	defer res.sess.Close(0, "test done")

	// Both sides completed Hello (session.New's job) and each resolved the
	// other's DeviceID from its TLS key -- proof the dial/accept path wired
	// the two into one working session, not just a bare QUIC handshake.
	if got, want := clientSess.Peer().DeviceID, server.DeviceID(); got != want {
		t.Errorf("client session peer DeviceID = %q, want %q", got, want)
	}
	if got, want := res.sess.Peer().DeviceID, client.DeviceID(); got != want {
		t.Errorf("server session peer DeviceID = %q, want %q", got, want)
	}
}

func TestDialAddr_WrongPinnedKey_FailsHandshake(t *testing.T) {
	server := newTestIdentity(t)
	client := newTestIdentity(t)
	wrong := newTestIdentity(t) // a key that is not the server's

	ln, err := Listen("127.0.0.1:0", server, "server", "linux", nil, nil)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ln.Accept(ctx) // best effort; the dial below is expected to fail first
	}()

	d := NewDialer(client, "client", "linux", nil)
	pinned := identity.Peer{IdentityPublicKey: wrong.IdentityPublic()}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := d.DialAddr(ctx, ln.Addr(), pinned); err == nil {
		t.Fatal("DialAddr succeeded despite a pinned key that does not match the server")
	}
}

func TestListener_Addr_ReportsBoundPort(t *testing.T) {
	server := newTestIdentity(t)
	ln, err := Listen("127.0.0.1:0", server, "server", "linux", nil, nil)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	host, port, err := net.SplitHostPort(ln.Addr())
	if err != nil {
		t.Fatalf("Addr() = %q is not host:port: %v", ln.Addr(), err)
	}
	if host == "" || port == "" || port == "0" {
		t.Fatalf("Addr() = %q, want a concrete bound port", ln.Addr())
	}
}

func TestListener_Close_UnblocksAccept(t *testing.T) {
	server := newTestIdentity(t)
	ln, err := Listen("127.0.0.1:0", server, "server", "linux", nil, nil)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := ln.Accept(context.Background())
		done <- err
	}()

	// Give Accept a moment to actually start blocking before we close.
	time.Sleep(50 * time.Millisecond)

	if err := ln.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Accept returned no error after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not unblock after Close")
	}
}

func TestDialAddr_ContextCancellationAbortsDial(t *testing.T) {
	client := newTestIdentity(t)
	d := NewDialer(client, "client", "linux", nil)

	// TEST-NET-3 (RFC 5737): reserved and non-routable, so nothing answers
	// and the dial would otherwise hang until it times out on its own -- the
	// cancelled context must be what actually aborts it.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pinned := identity.Peer{IdentityPublicKey: client.IdentityPublic()}
	if _, err := d.DialAddr(ctx, "203.0.113.1:1234", pinned); err == nil {
		t.Fatal("DialAddr succeeded despite an already-cancelled context")
	}
}

func TestDialer_Dial_NotImplementedInPhase1(t *testing.T) {
	client := newTestIdentity(t)
	d := NewDialer(client, "client", "linux", nil)

	_, err := d.Dial(context.Background(), identity.DeviceID("someotherdevice"))
	if err == nil {
		t.Fatal("Dial succeeded; Phase 1 has no discovery path and should report a clear error")
	}
	if err != ErrDialNotImplemented {
		t.Fatalf("Dial error = %v, want ErrDialNotImplemented", err)
	}
}
