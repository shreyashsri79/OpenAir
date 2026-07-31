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

	"github.com/shreyashsri79/openair/internal/identity"
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

	// M2: the two ends have to know each other before anything moves.
	senderKeys, receiverKeys := t.TempDir(), t.TempDir()
	pairKeyDirs(t, senderKeys, receiverKeys)

	ready := make(chan string, 1)
	recvErr := make(chan error, 1)
	var recvOut lockedBuffer
	go func() {
		recvErr <- receive(ctx, recvOptions{
			listen:  "127.0.0.1:0",
			dir:     recvDir,
			keys:    receiverKeys,
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
		keys:  senderKeys,
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

// TestSendRefusesUnpairedPeer is M2's rule from the sending side: a device this
// one has never paired with gets nothing, and no prompt is offered that could
// talk a user past it.
func TestSendRefusesUnpairedPeer(t *testing.T) {
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
	// Neither side has ever paired, and stdin is empty: there is nothing the
	// user could type here that would let this through.
	err := send(ctx, sendOptions{addr: addr, paths: []string{srcPath}, keys: t.TempDir()},
		strings.NewReader(""), &out)
	if err == nil {
		t.Fatal("send succeeded against a device it had never paired with")
	}
	if !strings.Contains(err.Error(), "nothing was sent") {
		t.Errorf("error = %v, want it to say nothing was sent", err)
	}
	if !strings.Contains(err.Error(), "openair pair") {
		t.Errorf("error = %v, want it to point at the pair command", err)
	}

	entries, err2 := os.ReadDir(recvDir)
	if err2 != nil {
		t.Fatalf("read destination: %v", err2)
	}
	for _, e := range entries {
		if e.Name() == "secret.bin" {
			t.Error("the file was transferred to an unpaired device")
		}
	}
}

// TestReceiveRefusesUnpairedPeer is the same rule from the other side, and the
// more important one: the receiver must refuse before any capability message is
// dispatched, whatever the sender believes.
func TestReceiveRefusesUnpairedPeer(t *testing.T) {
	recvDir := t.TempDir()
	receiverKeys := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ready := make(chan string, 1)
	var recvOut lockedBuffer
	go func() {
		_ = receive(ctx, recvOptions{
			listen: "127.0.0.1:0", dir: recvDir, keys: receiverKeys, yes: true, once: true,
			onReady: func(addr string) { ready <- addr },
		}, strings.NewReader(""), &recvOut)
	}()

	var addr string
	select {
	case addr = <-ready:
	case <-time.After(15 * time.Second):
		t.Fatal("receiver did not bind within 15s")
	}

	// A sender that has pinned the receiver but was never pinned back: the
	// dialling side's own check passes, so only the receiver's gate stands
	// between it and a transfer.
	senderKeys := t.TempDir()
	senderID, err := loadIdentity(senderKeys)
	if err != nil {
		t.Fatalf("sender identity: %v", err)
	}
	receiverID, err := loadIdentity(receiverKeys)
	if err != nil {
		t.Fatalf("receiver identity: %v", err)
	}
	senderStore, err := identity.OpenTrustStore(filepath.Join(senderKeys, trustStoreName))
	if err != nil {
		t.Fatalf("sender trust store: %v", err)
	}
	if err := senderStore.Put(identity.Peer{
		DeviceID:          receiverID.DeviceID(),
		IdentityPublicKey: receiverID.IdentityPublic(),
		Level:             identity.LevelTrusted,
		AuthPolicy:        "timed",
		CreatedAt:         1, LastSeen: 1,
	}); err != nil {
		t.Fatalf("pin receiver: %v", err)
	}
	_ = senderID

	srcPath := filepath.Join(t.TempDir(), "secret.bin")
	if err := os.WriteFile(srcPath, []byte("must not be transmitted"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	var out bytes.Buffer
	if err := send(ctx, sendOptions{addr: addr, paths: []string{srcPath}, keys: senderKeys},
		strings.NewReader(""), &out); err == nil {
		t.Fatalf("a one-sided pairing was enough to transfer:\n%s", out.String())
	}

	if _, err := os.Stat(filepath.Join(recvDir, "secret.bin")); err == nil {
		t.Fatal("the receiver wrote a file from a device it had not paired with")
	}
}

// TestPairCommand_RoundTrip drives both halves of `openair pair` the way a user
// would: one device prints an offer, the other is handed that exact string, and
// both users answer yes to the digits.
func TestPairCommand_RoundTrip(t *testing.T) {
	listenKeys, scanKeys := t.TempDir(), t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	type ready struct{ addr, offer string }
	readyCh := make(chan ready, 1)
	listenErr := make(chan error, 1)
	var listenOut lockedBuffer
	go func() {
		listenErr <- pairListen(ctx, pairOptions{
			listen:  "127.0.0.1:0",
			keys:    listenKeys,
			onReady: func(addr, offer string) { readyCh <- ready{addr, offer} },
		}, strings.NewReader("y\n"), &listenOut)
	}()

	var r ready
	select {
	case r = <-readyCh:
	case err := <-listenErr:
		t.Fatalf("the listening side exited before it was ready: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("the listening side did not bind within 15s")
	}

	var scanOut bytes.Buffer
	if err := pairScan(ctx, pairOptions{offer: r.offer, keys: scanKeys},
		strings.NewReader("y\n"), &scanOut); err != nil {
		t.Fatalf("pair scan: %v\noutput:\n%s", err, scanOut.String())
	}

	select {
	case err := <-listenErr:
		if err != nil {
			t.Fatalf("pair listen: %v\noutput:\n%s", err, listenOut.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("the listening side never finished\noutput:\n%s", listenOut.String())
	}

	// Both users were shown six digits, and both stores came out of it pinned.
	if !strings.Contains(listenOut.String(), "do both devices show exactly these six digits?") {
		t.Errorf("the listening side never displayed a SAS prompt:\n%s", listenOut.String())
	}
	if !strings.Contains(scanOut.String(), "do both devices show exactly these six digits?") {
		t.Errorf("the scanning side never displayed a SAS prompt:\n%s", scanOut.String())
	}

	listenID, err := loadIdentity(listenKeys)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	scanID, err := loadIdentity(scanKeys)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	for _, tc := range []struct {
		dir  string
		want identity.DeviceID
	}{
		{listenKeys, scanID.DeviceID()},
		{scanKeys, listenID.DeviceID()},
	} {
		store, err := identity.OpenTrustStore(filepath.Join(tc.dir, trustStoreName))
		if err != nil {
			t.Fatalf("trust store in %s: %v", tc.dir, err)
		}
		peer, ok := store.Get(tc.want)
		if !ok {
			t.Fatalf("%s did not pin %s after pairing", tc.dir, tc.want)
		}
		if peer.Level != identity.LevelTrusted {
			t.Errorf("%s pinned %s at level %d, want Trusted", tc.dir, tc.want, peer.Level)
		}
	}
}

// The digits are the only thing that detects a man in the middle, so a user who
// says the two screens disagree must end up with nothing pinned.
func TestPairCommand_DeclinePinsNothing(t *testing.T) {
	listenKeys, scanKeys := t.TempDir(), t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	type ready struct{ addr, offer string }
	readyCh := make(chan ready, 1)
	listenErr := make(chan error, 1)
	var listenOut lockedBuffer
	go func() {
		listenErr <- pairListen(ctx, pairOptions{
			listen:  "127.0.0.1:0",
			keys:    listenKeys,
			onReady: func(addr, offer string) { readyCh <- ready{addr, offer} },
		}, strings.NewReader("y\n"), &listenOut)
	}()

	var r ready
	select {
	case r = <-readyCh:
	case <-time.After(15 * time.Second):
		t.Fatal("the listening side did not bind within 15s")
	}

	var scanOut bytes.Buffer
	// "n": the digits on the two screens did not match.
	if err := pairScan(ctx, pairOptions{offer: r.offer, keys: scanKeys},
		strings.NewReader("n\n"), &scanOut); err == nil {
		t.Fatal("pairing succeeded even though the user said the digits differ")
	}

	select {
	case err := <-listenErr:
		if err == nil {
			t.Fatal("the listening side reported a successful pairing after the peer declined")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the listening side never finished")
	}

	for _, dir := range []string{listenKeys, scanKeys} {
		store, err := identity.OpenTrustStore(filepath.Join(dir, trustStoreName))
		if err != nil {
			t.Fatalf("trust store in %s: %v", dir, err)
		}
		if peers := store.List(); len(peers) != 0 {
			t.Errorf("%s pinned %d peer(s) after a declined pairing", dir, len(peers))
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
