package bench

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// ALPN identifies the OpenAir bulk-transfer protocol during the TLS handshake.
const ALPN = "openair-bench/1"

// spikeSeed makes the server's Ed25519 identity deterministic so the client can
// pin it without an out-of-band exchange.
//
// SPIKE ONLY. Real device identities are generated per install and persisted in
// the trust store; nothing in the v2 daemon may ship a fixed seed.
var spikeSeed = []byte("openair-v2-spike-fixed-seed-32byt")[:ed25519.SeedSize]

// SpikeIdentity returns the deterministic keypair used by both ends of the
// benchmark.
//
// Beyond wiring up the benchmark, this exercises the ADR-2 mechanism end to
// end: an Ed25519 device key doubling as the TLS 1.3 certificate key, with peer
// verification by raw public key rather than by any certificate authority.
func SpikeIdentity() (ed25519.PublicKey, ed25519.PrivateKey) {
	priv := ed25519.NewKeyFromSeed(spikeSeed)
	return priv.Public().(ed25519.PublicKey), priv
}

// DeviceID is the HLD's identity format: truncated SHA-256 of the public key,
// base32, no padding.
func DeviceID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:10]))
}

// ServerTLS builds a server config presenting a self-signed certificate whose
// key is the device's Ed25519 identity key.
func ServerTLS() (*tls.Config, error) {
	pub, priv := SpikeIdentity()

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: DeviceID(pub)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{der},
			PrivateKey:  priv,
		}},
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{ALPN},
	}, nil
}

// ClientTLS verifies the peer by pinned public key instead of by CA chain --
// the ADR-2 "TLS 1.3 with pinned self-signed certs" model. InsecureSkipVerify
// disables only the chain/hostname checks; VerifyPeerCertificate below is
// strictly stronger for this threat model, since it demands one exact key.
func ClientTLS(pinned ed25519.PublicKey) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{ALPN},
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("peer presented no certificate")
			}
			cert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("parse peer certificate: %w", err)
			}
			got, ok := cert.PublicKey.(ed25519.PublicKey)
			if !ok {
				return fmt.Errorf("peer key is %T, want ed25519.PublicKey", cert.PublicKey)
			}
			if !got.Equal(pinned) {
				return fmt.Errorf("peer key mismatch: pinned %s, got %s",
					DeviceID(pinned), DeviceID(got))
			}
			return nil
		},
	}
}
