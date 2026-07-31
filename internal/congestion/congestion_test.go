package congestion

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"testing"
	"time"

	"github.com/apernet/quic-go"

	"github.com/shreyashsri79/openair/internal/congestion/bbr"
)

func TestParseProfile(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    bbr.Profile
		wantErr bool
	}{
		{in: "conservative", want: bbr.ProfileConservative},
		{in: "standard", want: bbr.ProfileStandard},
		{in: "aggressive", want: bbr.ProfileAggressive},
		{in: "CONSERVATIVE", want: bbr.ProfileConservative},
		{in: "", want: bbr.ProfileStandard},
		{in: "cubic", wantErr: true},
	} {
		got, err := ParseProfile(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseProfile(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseProfile(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseProfile(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDefaultProfileIsLatencyFirst pins the choice made in congestion.go rather
// than the mechanism. D-16's whole argument is that BBRv1's standing queue is
// paid by every interactive capability sharing the one connection per peer, and
// that the conservative gain profile is the mitigation. A silent flip of this
// constant back to the throughput-first default would undo that with no other
// visible symptom than latency regressing under load.
func TestDefaultProfileIsLatencyFirst(t *testing.T) {
	if DefaultProfile != bbr.ProfileConservative {
		t.Errorf("DefaultProfile = %q, want %q -- see D-16 before changing this",
			DefaultProfile, bbr.ProfileConservative)
	}
}

// TestUseBBROnLiveConnection is the load-bearing test of this package. D-16
// rests entirely on the apernet fork exposing congestion control as public API,
// so that BBR can be installed on a live connection without patching quic-go's
// internal packages. If SetCongestionControl ever stops working -- because the
// fork drops the export, or because the sender no longer satisfies the
// interface -- this is what says so, and it says it at build-and-test time
// rather than as an unexplained throughput change months later.
func TestUseBBROnLiveConnection(t *testing.T) {
	for _, profile := range []bbr.Profile{
		bbr.ProfileConservative,
		bbr.ProfileStandard,
		bbr.ProfileAggressive,
	} {
		t.Run(string(profile), func(t *testing.T) {
			testTransferWithProfile(t, profile)
		})
	}
}

func testTransferWithProfile(t *testing.T, profile bbr.Profile) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tlsConf := testTLSConfig(t)
	qconf := &quic.Config{
		EnableDatagrams:    true,
		MaxIdleTimeout:     10 * time.Second,
		MaxIncomingStreams: 16,
	}

	ln, err := quic.ListenAddr("127.0.0.1:0", tlsConf, qconf)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	const payloadSize = 1 << 20
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- func() error {
			conn, err := ln.Accept(ctx)
			if err != nil {
				return err
			}
			// Installing on the accepting side too: both directions of a
			// session run our controller, not just the dialler's.
			UseBBR(conn, profile)

			st, err := conn.AcceptStream(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			if _, err := io.Copy(st, io.LimitReader(zeroes{}, payloadSize)); err != nil {
				return err
			}
			return st.Close()
		}()
	}()

	conn, err := quic.DialAddr(ctx, ln.Addr().String(), clientTLSConfig(), qconf)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseWithError(0, "")

	UseBBR(conn, profile)

	st, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if _, err := st.Write([]byte("go")); err != nil {
		t.Fatalf("write: %v", err)
	}

	n, err := io.Copy(io.Discard, st)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read: %v", err)
	}
	if n != payloadSize {
		t.Errorf("received %d bytes, want %d", n, payloadSize)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server: %v", err)
	}
}

type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) { return len(p), nil }

// testTLSConfig is deliberately self-contained. internal/identity owns the real
// D-7 pinning configuration; depending on it here would couple a congestion
// test to an unrelated package's progress.
func testTLSConfig(t *testing.T) *tls.Config {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "openair-congestion-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{der},
			PrivateKey:  priv,
		}},
		NextProtos: []string{"openair/1"},
		MinVersion: tls.VersionTLS13,
	}
}

func clientTLSConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, // this test asserts nothing about identity
		NextProtos:         []string{"openair/1"},
		MinVersion:         tls.VersionTLS13,
	}
}
