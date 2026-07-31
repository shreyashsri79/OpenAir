package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// ALPN identifies the OpenAir protocol during the TLS handshake
// (PROTOCOL.md §1). A peer offering no matching ALPN is rejected.
const ALPN = "openair/1"

// certLifetime is how long the self-signed certificate claims validity.
//
// Nothing checks it. Under D-7 there is no chain and no authority, so both ends
// skip x509 verification entirely and reach the raw public key directly, which
// is the only thing that carries meaning here. The dates exist so that a stray
// tool inspecting the certificate sees something sane, and the span is long
// because an expiry would be a scheduled outage with no security benefit.
const certLifetime = 100 * 365 * 24 * time.Hour

// ObservedKey records the peer public key seen during a pairing-mode handshake,
// where nothing is pinned yet and the key is the thing being learned (§5).
// Safe for concurrent use; Key returns nil until a handshake has reached the
// certificate.
type ObservedKey struct {
	mu  sync.Mutex
	key ed25519.PublicKey
}

func (o *ObservedKey) set(k ed25519.PublicKey) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.key = k
}

// Key returns the peer public key observed during the handshake, or nil if no
// handshake has completed against this observer. The returned slice is a copy.
func (o *ObservedKey) Key() ed25519.PublicKey {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.key == nil {
		return nil
	}
	out := make(ed25519.PublicKey, len(o.key))
	copy(out, o.key)
	return out
}

// SelfSignedCertificate builds the certificate that terminates TLS for an
// identity key. The certificate key *is* the identity key (D-7): there is no
// second keypair to map or revoke.
func SelfSignedCertificate(priv ed25519.PrivateKey) (*tls.Certificate, error) {
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("identity: private key is %T, want ed25519.PrivateKey", priv)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("identity: serial number: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: string(DeriveDeviceID(pub))},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(certLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("identity: create certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("identity: parse own certificate: %w", err)
	}
	return &tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
		Leaf:        leaf,
	}, nil
}

// pinnedTLSConfig builds the config both ends of an OpenAir connection use.
//
// One config serves both roles because Identity.TLSConfig has one form and a
// device is initiator on one connection and responder on the next. On the
// client side InsecureSkipVerify turns off chain and hostname checking, which
// is what D-7 discards; on the server side RequireAnyClientCert asks for the
// peer certificate without asking Go to chain it to anything. In both
// directions verifyPinned is what actually decides, and it is strictly stronger
// than a chain check: it demands one exact key.
//
// A nil pinned key is pairing mode (§5): the peer certificate is accepted and
// its key handed to observed, because at that point there is nothing to compare
// against and the short authentication string in §5.2 is what establishes trust.
func pinnedTLSConfig(cert *tls.Certificate, pinned ed25519.PublicKey, observed *ObservedKey) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{*cert},

		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		NextProtos: []string{ALPN},

		// Client side: skip chain and hostname validation, then do something
		// stronger in VerifyPeerCertificate.
		InsecureSkipVerify: true,

		// Server side: demand a certificate, do not chain it. Verification is
		// the same callback as on the client.
		ClientAuth: tls.RequireAnyClientCert,

		VerifyPeerCertificate: verifyPinned(pinned, observed),
		VerifyConnection:      verifyNegotiated,
	}
}

// verifyPinned compares the peer's raw certificate public key against pinned.
// No CA, no chain, no hostname (D-7, PROTOCOL.md §2).
func verifyPinned(pinned ed25519.PublicKey, observed *ObservedKey) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return ErrNoPeerCertificate
		}
		cert, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("identity: parse peer certificate: %w", err)
		}
		got, ok := cert.PublicKey.(ed25519.PublicKey)
		if !ok {
			return fmt.Errorf("identity: peer key is %T, want ed25519.PublicKey", cert.PublicKey)
		}
		if observed != nil {
			observed.set(got)
		}
		if pinned == nil {
			// Pairing mode: nothing to compare against yet (§5).
			return nil
		}
		if !got.Equal(pinned) {
			return &KeyMismatchError{Pinned: pinned, Got: got}
		}
		return nil
	}
}

// verifyNegotiated enforces the two transport invariants of PROTOCOL.md §1 that
// crypto/tls will not enforce on its own in every direction: TLS 1.3, and the
// OpenAir ALPN. A client that offers no ALPN extension at all negotiates the
// empty string rather than failing, so the check has to be explicit.
func verifyNegotiated(cs tls.ConnectionState) error {
	if cs.Version != tls.VersionTLS13 {
		return fmt.Errorf("%w: negotiated 0x%04x", ErrTLSVersion, cs.Version)
	}
	if cs.NegotiatedProtocol != ALPN {
		return fmt.Errorf("%w: negotiated %q", ErrALPNMismatch, cs.NegotiatedProtocol)
	}
	return nil
}

// PeerPublicKey extracts the peer's Ed25519 identity key from a completed
// handshake. This is how pairing reads the key it is about to pin when the
// caller holds the connection state; ObservedKey covers callers that do not.
func PeerPublicKey(cs tls.ConnectionState) (ed25519.PublicKey, error) {
	if len(cs.PeerCertificates) == 0 {
		return nil, ErrNoPeerCertificate
	}
	pub, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("identity: peer key is %T, want ed25519.PublicKey", cs.PeerCertificates[0].PublicKey)
	}
	return pub, nil
}
