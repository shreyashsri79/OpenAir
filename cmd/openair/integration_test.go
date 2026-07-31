package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSendReceiveOverLoopback is M1's definition of done, minus the two
// physical machines: a file moves end to end and its SHA-256 matches.
//
// It exercises every M1 task at once -- identity and TLS pinning (M1b), the
// envelope and session handshake (M1a), dial and accept (M1d), the chunk
// engine (M1c) and the CLI wiring (M1e) -- over a real QUIC connection on
// loopback, with BBR installed (X1). Nothing here is mocked.
func TestSendReceiveOverLoopback(t *testing.T) {
	const size = 8 << 20
	src := make([]byte, size)
	if _, err := rand.Read(src); err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	want := sha256.Sum256(src)

	sendDir, recvDir := t.TempDir(), t.TempDir()
	srcPath := filepath.Join(sendDir, "payload.bin")
	if err := os.WriteFile(srcPath, src, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ready := make(chan string, 1)
	recvErr := make(chan error, 1)
	var recvOut lockedBuffer
	go func() {
		recvErr <- receive(ctx, recvOptions{
			listen:  "127.0.0.1:0",
			dir:     recvDir,
			keys:    t.TempDir(),
			yes:     true,
			once:    true,
			onReady: func(addr string) { ready <- addr },
		}, strings.NewReader(""), &recvOut)
	}()

	var addr string
	select {
	case addr = <-ready:
	case err := <-recvErr:
		t.Fatalf("receiver exited before it was ready: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("receiver did not bind within 15s")
	}

	var sendOut bytes.Buffer
	if err := send(ctx, sendOptions{
		addr:  addr,
		paths: []string{srcPath},
		keys:  t.TempDir(),
		yes:   true,
	}, strings.NewReader(""), &sendOut); err != nil {
		t.Fatalf("send: %v\nsender output:\n%s", err, sendOut.String())
	}

	select {
	case err := <-recvErr:
		if err != nil {
			t.Fatalf("receive: %v\nreceiver output:\n%s", err, recvOut.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("receiver did not finish within 30s\nreceiver output:\n%s", recvOut.String())
	}

	gotPath := filepath.Join(recvDir, "payload.bin")
	f, err := os.Open(gotPath)
	if err != nil {
		t.Fatalf("open received file: %v", err)
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		t.Fatalf("digest received file: %v", err)
	}
	if n != size {
		t.Errorf("received %d bytes, want %d", n, size)
	}
	var got [sha256.Size]byte
	copy(got[:], h.Sum(nil))
	if got != want {
		t.Fatalf("SHA-256 mismatch:\n got %s\nwant %s", hex.EncodeToString(got[:]), hex.EncodeToString(want[:]))
	}

	// No .oapart staging file may survive a successful transfer (§8.5).
	entries, err := os.ReadDir(recvDir)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".oapart") {
			t.Errorf("staging file %s left behind after a completed transfer", e.Name())
		}
	}
}

// TestSendRefusesUnconfirmedFingerprint pins M1's entire security model: the
// fingerprint prompt is the only thing authenticating a peer at this
// milestone, so answering anything but yes must send nothing at all.
func TestSendRefusesUnconfirmedFingerprint(t *testing.T) {
	sendDir, recvDir := t.TempDir(), t.TempDir()
	srcPath := filepath.Join(sendDir, "secret.bin")
	if err := os.WriteFile(srcPath, []byte("must not be transmitted"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ready := make(chan string, 1)
	recvErr := make(chan error, 1)
	var recvOut lockedBuffer
	go func() {
		recvErr <- receive(ctx, recvOptions{
			listen: "127.0.0.1:0", dir: recvDir, keys: t.TempDir(), yes: true, once: true,
			onReady: func(addr string) { ready <- addr },
		}, strings.NewReader(""), &recvOut)
	}()

	var addr string
	select {
	case addr = <-ready:
	case <-time.After(15 * time.Second):
		t.Fatal("receiver did not bind within 15s")
	}

	var out bytes.Buffer
	// "n" at the fingerprint prompt.
	err := send(ctx, sendOptions{addr: addr, paths: []string{srcPath}, keys: t.TempDir()},
		strings.NewReader("n\n"), &out)
	if err == nil {
		t.Fatal("send succeeded despite an unconfirmed fingerprint")
	}
	if !strings.Contains(err.Error(), "nothing was sent") {
		t.Errorf("error = %v, want it to say nothing was sent", err)
	}

	entries, err2 := os.ReadDir(recvDir)
	if err2 != nil {
		t.Fatalf("read destination: %v", err2)
	}
	for _, e := range entries {
		if e.Name() == "secret.bin" {
			t.Error("the file was transferred even though the fingerprint was refused")
		}
	}
}

func TestFingerprintGrouping(t *testing.T) {
	if got, want := fingerprint("abcdefghijklmnop"), "abcd-efgh-ijkl-mnop"; got != want {
		t.Errorf("fingerprint = %q, want %q", got, want)
	}
}

func TestConfirmRequiresExplicitYes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"y\n", true}, {"Y\n", true}, {"yes\n", true},
		{"n\n", false}, {"\n", false}, {"", false}, {"maybe\n", false},
	} {
		if got := confirm(strings.NewReader(tc.in), io.Discard, "?"); got != tc.want {
			t.Errorf("confirm(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
