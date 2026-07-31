package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"
)

// newTestIdentity builds an identity in a throwaway directory with cheap Argon2
// parameters, so that a test which seals a privilege key does not spend a
// second per call doing it.
func newTestIdentity(t *testing.T) *FileIdentity {
	t.Helper()
	id, err := LoadOrCreate(Options{
		Dir:        t.TempDir(),
		Tier:       TierPassphrase,
		Passphrase: []byte("correct horse battery staple"),
		Argon2:     testArgon2,
	})
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	return id
}

// testArgon2 is deliberately far below DefaultArgon2Params. Sealing cost is a
// property of the file, not of the code, so tests can set it to nothing.
var testArgon2 = Argon2Params{Time: 1, Memory: 8, Lanes: 1}

type handshakeResult struct {
	clientErr   error
	serverErr   error
	clientState tls.ConnectionState
	serverState tls.ConnectionState
}

// handshake runs one TLS handshake over a loopback socket and returns both
// sides' outcomes.
//
// A real socket rather than net.Pipe: net.Pipe is unbuffered and synchronous,
// so an aborted handshake deadlocks. The responder blocks writing the tail of
// its flight while the initiator, having already rejected the certificate,
// blocks writing its alert -- neither is reading. Loopback has kernel buffers
// and behaves like the transport this code actually runs on.
func handshake(t *testing.T, clientCfg, serverCfg *tls.Config) handshakeResult {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type serverOut struct {
		err   error
		state tls.ConnectionState
	}
	serverDone := make(chan serverOut, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			serverDone <- serverOut{err: err}
			return
		}
		defer raw.Close()
		_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
		server := tls.Server(raw, serverCfg)
		defer server.Close()
		err = server.HandshakeContext(ctx)
		serverDone <- serverOut{err: err, state: server.ConnectionState()}
	}()

	raw, err := net.DialTimeout("tcp", ln.Addr().String(), 10*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))

	client := tls.Client(raw, clientCfg)
	defer client.Close()

	res := handshakeResult{}
	res.clientErr = client.HandshakeContext(ctx)
	res.clientState = client.ConnectionState()

	// In TLS 1.3 the initiator can finish before the responder has processed its
	// certificate, so a one-byte read forces the responder's verdict back to the
	// initiator instead of letting the test race it.
	if res.clientErr == nil {
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		var b [1]byte
		_, _ = client.Read(b[:])
	}

	out := <-serverDone
	res.serverErr = out.err
	res.serverState = out.state
	return res
}

func TestTLSHandshakeMutualPinning(t *testing.T) {
	a := newTestIdentity(t)
	b := newTestIdentity(t)

	clientCfg, err := a.TLSConfig(b.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}
	serverCfg, err := b.TLSConfig(a.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}

	res := handshake(t, clientCfg, serverCfg)
	if res.clientErr != nil {
		t.Fatalf("client handshake: %v", res.clientErr)
	}
	if res.serverErr != nil {
		t.Fatalf("server handshake: %v", res.serverErr)
	}

	// PROTOCOL.md §1: ALPN openair/1, TLS 1.3.
	if got := res.clientState.NegotiatedProtocol; got != ALPN {
		t.Errorf("client ALPN = %q, want %q", got, ALPN)
	}
	if got := res.serverState.NegotiatedProtocol; got != ALPN {
		t.Errorf("server ALPN = %q, want %q", got, ALPN)
	}
	if got := res.clientState.Version; got != tls.VersionTLS13 {
		t.Errorf("negotiated version = 0x%04x, want TLS 1.3", got)
	}

	// The peer key must be readable from the completed handshake, in both
	// directions -- this is what pairing pins and what the session layer checks
	// the claimed DeviceID against.
	gotServerKey, err := PeerPublicKey(res.clientState)
	if err != nil {
		t.Fatalf("PeerPublicKey on client: %v", err)
	}
	if !gotServerKey.Equal(b.IdentityPublic()) {
		t.Error("client saw the wrong server key")
	}
	gotClientKey, err := PeerPublicKey(res.serverState)
	if err != nil {
		t.Fatalf("PeerPublicKey on server: %v", err)
	}
	if !gotClientKey.Equal(a.IdentityPublic()) {
		t.Error("server saw the wrong client key")
	}
	if DeriveDeviceID(gotServerKey) != b.DeviceID() {
		t.Error("DeviceID derived from the presented key is not the peer's DeviceID")
	}
}

func TestTLSHandshakeWrongPinnedKeyOnClient(t *testing.T) {
	a := newTestIdentity(t)
	b := newTestIdentity(t)
	imposter := newTestIdentity(t)

	// A pins the imposter's key but reaches B: exactly the shape of a device
	// that rotated its identity key, or of an active substitution.
	clientCfg, err := a.TLSConfig(imposter.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}
	serverCfg, err := b.TLSConfig(a.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}

	res := handshake(t, clientCfg, serverCfg)
	if res.clientErr == nil {
		t.Fatal("client handshake succeeded against an unpinned key")
	}
	if !errors.Is(res.clientErr, ErrKeyMismatch) {
		t.Fatalf("client error %v does not classify as ErrKeyMismatch", res.clientErr)
	}

	// PROTOCOL.md §2: a mismatch must surface as a re-pair prompt, never as a
	// retryable error. Callers must be able to reach the detail without string
	// matching.
	var mismatch *KeyMismatchError
	if !errors.As(res.clientErr, &mismatch) {
		t.Fatalf("client error %v is not a *KeyMismatchError", res.clientErr)
	}
	if mismatch.Retryable() {
		t.Error("KeyMismatchError reports itself retryable")
	}
	if !mismatch.Pinned.Equal(imposter.IdentityPublic()) {
		t.Error("KeyMismatchError.Pinned is not the key we pinned")
	}
	if !mismatch.Got.Equal(b.IdentityPublic()) {
		t.Error("KeyMismatchError.Got is not the key the peer presented")
	}
}

func TestTLSHandshakeWrongPinnedKeyOnServer(t *testing.T) {
	a := newTestIdentity(t)
	b := newTestIdentity(t)
	imposter := newTestIdentity(t)

	// The responder pins the wrong client key. Verification is symmetric: a
	// server that only checked the transport and not the key would let an
	// unpaired device in, so this direction matters as much as the other.
	clientCfg, err := a.TLSConfig(b.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}
	serverCfg, err := b.TLSConfig(imposter.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}

	res := handshake(t, clientCfg, serverCfg)
	if res.serverErr == nil {
		t.Fatal("server handshake succeeded against an unpinned client key")
	}
	if !errors.Is(res.serverErr, ErrKeyMismatch) {
		t.Fatalf("server error %v does not classify as ErrKeyMismatch", res.serverErr)
	}
}

func TestTLSHandshakeRejectsMissingClientCertificate(t *testing.T) {
	a := newTestIdentity(t)
	b := newTestIdentity(t)

	clientCfg, err := a.TLSConfig(b.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}
	clientCfg.Certificates = nil // client offers nothing to pin
	serverCfg, err := b.TLSConfig(a.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}

	res := handshake(t, clientCfg, serverCfg)
	if res.serverErr == nil {
		t.Fatal("server accepted a client that presented no certificate")
	}
}

func TestTLSHandshakeRejectsWrongALPN(t *testing.T) {
	a := newTestIdentity(t)
	b := newTestIdentity(t)

	clientCfg, err := a.TLSConfig(b.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}
	clientCfg.NextProtos = []string{"h3"}
	serverCfg, err := b.TLSConfig(a.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}

	res := handshake(t, clientCfg, serverCfg)
	if res.clientErr == nil && res.serverErr == nil {
		t.Fatal("handshake succeeded with a non-OpenAir ALPN")
	}
}

func TestTLSHandshakeRejectsAbsentALPN(t *testing.T) {
	a := newTestIdentity(t)
	b := newTestIdentity(t)

	clientCfg, err := a.TLSConfig(b.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}
	// A client that sends no ALPN extension at all negotiates the empty string
	// rather than failing, so §1's "MUST be rejected" has to be enforced by us.
	clientCfg.NextProtos = nil
	serverCfg, err := b.TLSConfig(a.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}

	res := handshake(t, clientCfg, serverCfg)
	if res.clientErr == nil {
		t.Fatal("handshake succeeded with no ALPN negotiated")
	}
	if !errors.Is(res.clientErr, ErrALPNMismatch) {
		t.Fatalf("error %v does not classify as ErrALPNMismatch", res.clientErr)
	}
}

func TestTLSHandshakeRefusesTLS12(t *testing.T) {
	a := newTestIdentity(t)
	b := newTestIdentity(t)

	clientCfg, err := a.TLSConfig(b.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}
	clientCfg.MinVersion = tls.VersionTLS12
	clientCfg.MaxVersion = tls.VersionTLS12
	serverCfg, err := b.TLSConfig(a.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}

	res := handshake(t, clientCfg, serverCfg)
	if res.serverErr == nil && res.clientErr == nil {
		t.Fatal("handshake succeeded at TLS 1.2")
	}
	if res.serverState.HandshakeComplete || res.clientState.HandshakeComplete {
		t.Fatal("a TLS 1.2 handshake completed")
	}
}

// TestTLSHandshakeServerRefusesTLS12 checks the responder's own floor rather
// than the initiator's, since a device we did not configure is the one that
// would try to downgrade us.
func TestTLSHandshakeServerRefusesTLS12(t *testing.T) {
	b := newTestIdentity(t)
	serverCfg, err := b.TLSConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if serverCfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = 0x%04x, want TLS 1.3", serverCfg.MinVersion)
	}
	if serverCfg.MaxVersion != tls.VersionTLS13 {
		t.Errorf("MaxVersion = 0x%04x, want TLS 1.3", serverCfg.MaxVersion)
	}
	if len(serverCfg.NextProtos) != 1 || serverCfg.NextProtos[0] != ALPN {
		t.Errorf("NextProtos = %v, want [%q]", serverCfg.NextProtos, ALPN)
	}
	if serverCfg.ClientAuth != tls.RequireAnyClientCert {
		t.Errorf("ClientAuth = %v, want RequireAnyClientCert", serverCfg.ClientAuth)
	}
}

func TestTLSPairingModeSurfacesPeerKey(t *testing.T) {
	a := newTestIdentity(t)
	b := newTestIdentity(t)

	// Pairing runs where neither side has a pinned key yet (§5).
	clientCfg, clientObserved, err := a.TLSConfigPairing()
	if err != nil {
		t.Fatal(err)
	}
	serverCfg, serverObserved, err := b.TLSConfigPairing()
	if err != nil {
		t.Fatal(err)
	}

	res := handshake(t, clientCfg, serverCfg)
	if res.clientErr != nil || res.serverErr != nil {
		t.Fatalf("pairing handshake failed: client %v, server %v", res.clientErr, res.serverErr)
	}
	if got := clientObserved.Key(); !got.Equal(b.IdentityPublic()) {
		t.Error("pairing client did not observe the server's identity key")
	}
	if got := serverObserved.Key(); !got.Equal(a.IdentityPublic()) {
		t.Error("pairing server did not observe the client's identity key")
	}
	// §5.1: B verifies A's key against the offer's fingerprint before
	// proceeding, which means the observed key must be enough to derive the ID.
	if DeriveDeviceID(clientObserved.Key()) != b.DeviceID() {
		t.Error("observed key does not derive the peer's DeviceID")
	}
}

func TestTLSConfigNilPinnedStillAcceptsPeer(t *testing.T) {
	a := newTestIdentity(t)
	b := newTestIdentity(t)

	// TLSConfig(nil) is what the listener uses: it cannot know which peer is
	// dialling, so it accepts any certificate and lets the session layer decide.
	clientCfg, err := a.TLSConfig(b.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}
	serverCfg, err := b.TLSConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	res := handshake(t, clientCfg, serverCfg)
	if res.clientErr != nil || res.serverErr != nil {
		t.Fatalf("unpinned handshake failed: client %v, server %v", res.clientErr, res.serverErr)
	}
	if _, err := PeerPublicKey(res.serverState); err != nil {
		t.Fatalf("unpinned server could not read the peer key: %v", err)
	}
}

func TestObservedKeyIsEmptyBeforeHandshake(t *testing.T) {
	a := newTestIdentity(t)
	_, observed, err := a.TLSConfigPairing()
	if err != nil {
		t.Fatal(err)
	}
	if observed.Key() != nil {
		t.Fatal("ObservedKey reported a key before any handshake")
	}
}

func TestObservedKeyReturnsACopy(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	o := &ObservedKey{}
	o.set(pub)
	got := o.Key()
	got[0] ^= 0xff
	if !o.Key().Equal(pub) {
		t.Fatal("mutating the returned key changed the observer's copy")
	}
}

func TestTLSConfigRejectsMalformedPinnedKey(t *testing.T) {
	a := newTestIdentity(t)
	if _, err := a.TLSConfig(ed25519.PublicKey{1, 2, 3}); err == nil {
		t.Fatal("TLSConfig accepted a 3-byte pinned key")
	}
}

func TestSelfSignedCertificateKeyIsTheIdentityKey(t *testing.T) {
	a := newTestIdentity(t)
	cert := a.Certificate()
	if cert.Leaf == nil {
		t.Fatal("certificate has no parsed leaf")
	}
	pub, ok := cert.Leaf.PublicKey.(ed25519.PublicKey)
	if !ok {
		t.Fatalf("certificate key is %T, want ed25519.PublicKey", cert.Leaf.PublicKey)
	}
	// D-7: the certificate key is the device key, not a second keypair mapped
	// onto it.
	if !pub.Equal(a.IdentityPublic()) {
		t.Error("certificate key is not the identity key")
	}
	if cert.Leaf.Subject.CommonName != string(a.DeviceID()) {
		t.Errorf("certificate CN = %q, want the DeviceID %q", cert.Leaf.Subject.CommonName, a.DeviceID())
	}
	if !cert.Leaf.NotAfter.After(time.Now().Add(365 * 24 * time.Hour)) {
		t.Error("certificate expires within a year, which would be a scheduled outage")
	}
}
