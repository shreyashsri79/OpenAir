package mobile

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/files"
	"github.com/shreyashsri79/openair/internal/identity"
)

// ── test doubles ──────────────────────────────────────────────────────────────

// verifier is a scripted PeerVerifier/OfferVerifier. It records what it was
// shown, which is the point: M1's security model is that a human sees a
// fingerprint, so a test that does not assert the fingerprint reached the
// prompt is not testing the model.
type verifier struct {
	mu        sync.Mutex
	peerAns   bool
	offerAns  bool
	peers     []*PeerInfo
	offers    []*Offer
	peerCalls int
}

func (v *verifier) VerifyPeer(p *PeerInfo) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.peers = append(v.peers, p)
	v.peerCalls++
	return v.peerAns
}

func (v *verifier) VerifyOffer(p *PeerInfo, o *Offer) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.offers = append(v.offers, o)
	return v.offerAns
}

func (v *verifier) lastPeer() *PeerInfo {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.peers) == 0 {
		return nil
	}
	return v.peers[len(v.peers)-1]
}

func (v *verifier) lastOffer() *Offer {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.offers) == 0 {
		return nil
	}
	return v.offers[len(v.offers)-1]
}

type progressRec struct {
	mu    sync.Mutex
	calls int
	last  int64
	total int64
}

func (p *progressRec) OnProgress(_ string, done, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.last, p.total = done, total
}

func (p *progressRec) snapshot() (int, int64, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.last, p.total
}

type completion struct {
	ch chan bool
}

func newCompletion() *completion { return &completion{ch: make(chan bool, 4)} }

func (c *completion) OnComplete(_ string, ok bool) {
	select {
	case c.ch <- ok:
	default:
	}
}

func (c *completion) wait(t *testing.T, d time.Duration) bool {
	t.Helper()
	select {
	case ok := <-c.ch:
		return ok
	case <-time.After(d):
		t.Fatal("no completion callback within timeout")
		return false
	}
}

// ── harness ───────────────────────────────────────────────────────────────────

type harness struct {
	recv     *Receiver
	send     *Sender
	recvDir  string
	addr     string
	rVerify  *verifier
	sVerify  *verifier
	done     *completion
	sProg    *progressRec
	rProg    *progressRec
	recvIden *Identity
	sendIden *Identity
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	recvIden, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatalf("receiver identity: %v", err)
	}
	sendIden, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatalf("sender identity: %v", err)
	}

	h := &harness{
		recvDir:  t.TempDir(),
		rVerify:  &verifier{peerAns: true, offerAns: true},
		sVerify:  &verifier{peerAns: true},
		done:     newCompletion(),
		sProg:    &progressRec{},
		rProg:    &progressRec{},
		recvIden: recvIden,
		sendIden: sendIden,
	}

	// M2: the two ends have to know each other before anything moves. This
	// writes the records the pairing exchange writes; the exchange itself is
	// tested in TestPairingThroughBinding.
	pairIdentities(t, recvIden, sendIden)

	h.recv = NewReceiver(recvIden, "test-receiver", h.recvDir)
	h.recv.SetPeerVerifier(h.rVerify)
	h.recv.SetOfferVerifier(h.rVerify)
	h.recv.SetTransferCallback(h.done)
	h.recv.SetProgressCallback(h.rProg)

	h.send = NewSender(sendIden, "test-sender")
	h.send.SetPeerVerifier(h.sVerify)
	h.send.SetProgressCallback(h.sProg)

	return h
}

// pairIdentities pins each identity in the other's trust store.
func pairIdentities(t *testing.T, a, b *Identity) {
	t.Helper()
	pin := func(holder, peer *Identity) {
		t.Helper()
		err := holder.store.Put(identity.Peer{
			DeviceID:          peer.impl.DeviceID(),
			IdentityPublicKey: peer.impl.IdentityPublic(),
			DisplayName:       "test peer",
			Platform:          PlatformName,
			Level:             identity.LevelTrusted,
			AuthPolicy:        "timed",
			CreatedAt:         1,
			LastSeen:          1,
		})
		if err != nil {
			t.Fatalf("pin peer: %v", err)
		}
	}
	pin(a, b)
	pin(b, a)
}

func (h *harness) start(t *testing.T) {
	t.Helper()
	if err := h.recv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("start receiver: %v", err)
	}
	t.Cleanup(func() { _ = h.recv.Stop() })
	h.addr = h.recv.Addr()
	if h.addr == "" {
		t.Fatal("receiver reported no address after Start")
	}
}

func writeFile(t *testing.T, dir, name string, n int) (string, [32]byte) {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("generate payload: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf, 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p, sha256.Sum256(buf)
}

func digestOf(t *testing.T, path string) ([32]byte, int64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		t.Fatalf("digest %s: %v", path, err)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, n
}

// ── end-to-end ────────────────────────────────────────────────────────────────

// TestSendReceiveThroughBinding is the binding's definition of done: a file
// moves end to end through the exported API only, with both fingerprint
// prompts exercised, and arrives byte-identical.
func TestSendReceiveThroughBinding(t *testing.T) {
	const size = 3 << 20

	h := newHarness(t)
	h.start(t)

	srcPath, want := writeFile(t, t.TempDir(), "payload.bin", size)

	list := NewFileList()
	if err := list.Add(srcPath, ""); err != nil {
		t.Fatalf("add file: %v", err)
	}
	if got := list.TotalBytes(); got != size {
		t.Errorf("FileList.TotalBytes = %d, want %d", got, size)
	}

	transferID, err := h.send.SendFiles(h.addr, list)
	if err != nil {
		t.Fatalf("SendFiles: %v", err)
	}
	if transferID == "" {
		t.Error("SendFiles returned an empty transfer id")
	}

	if ok := h.done.wait(t, 30*time.Second); !ok {
		t.Fatal("receiver reported the transfer as failed")
	}

	got, n := digestOf(t, filepath.Join(h.recvDir, "payload.bin"))
	if n != size {
		t.Errorf("received %d bytes, want %d", n, size)
	}
	if got != want {
		t.Fatalf("SHA-256 mismatch:\n got %s\nwant %s",
			hex.EncodeToString(got[:]), hex.EncodeToString(want[:]))
	}

	// The sender was shown the receiver's real fingerprint, and vice versa.
	// If these ever disagree the UI is asking the user to compare the wrong
	// string, which looks identical on screen and is worthless.
	if p := h.sVerify.lastPeer(); p == nil {
		t.Fatal("the sender's PeerVerifier was never called")
	} else if p.DeviceID() != h.recvIden.DeviceID() {
		t.Errorf("sender saw peer %q, want the receiver's id %q", p.DeviceID(), h.recvIden.DeviceID())
	} else if p.Fingerprint() != h.recvIden.Fingerprint() {
		t.Errorf("sender saw fingerprint %q, want %q", p.Fingerprint(), h.recvIden.Fingerprint())
	}
	if p := h.rVerify.lastPeer(); p == nil {
		t.Fatal("the receiver's PeerVerifier was never called")
	} else if p.DeviceID() != h.sendIden.DeviceID() {
		t.Errorf("receiver saw peer %q, want the sender's id %q", p.DeviceID(), h.sendIden.DeviceID())
	}

	// The offer prompt described what actually arrived.
	o := h.rVerify.lastOffer()
	if o == nil {
		t.Fatal("the receiver's OfferVerifier was never called")
	}
	if o.FileCount() != 1 {
		t.Errorf("offer FileCount = %d, want 1", o.FileCount())
	}
	if o.TotalBytes() != size {
		t.Errorf("offer TotalBytes = %d, want %d", o.TotalBytes(), size)
	}
	if o.Path(0) != "payload.bin" {
		t.Errorf("offer Path(0) = %q, want %q", o.Path(0), "payload.bin")
	}
	if o.Size(0) != size {
		t.Errorf("offer Size(0) = %d, want %d", o.Size(0), size)
	}
	if o.Path(9) != "" || o.Size(9) != -1 {
		t.Error("Offer index accessors must be bounds-safe: gobind has no exceptions to throw here")
	}

	// Progress, when it arrives, carries a total the UI can divide by.
	//
	// It may not arrive at all. The core reports on a 1 Hz ticker (§8.5) with
	// no final flush, so a transfer that finishes inside a second emits nothing
	// — which is exactly what happens here on loopback. That is why the
	// receiving side has OnComplete at all: a shell that infers completion from
	// progress reaching the total will hang on every fast transfer.
	if calls, last, total := h.rProg.snapshot(); calls > 0 {
		if total != size {
			t.Errorf("receiver progress total = %d, want %d", total, size)
		}
		if last == 0 {
			t.Error("receiver progress reported zero bytes")
		}
	}
	if calls, _, total := h.sProg.snapshot(); calls > 0 && total <= 0 {
		t.Errorf("sender progress total = %d; the UI would divide by that", total)
	}
}

func TestSendMultipleFiles(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	srcDir := t.TempDir()
	aPath, aWant := writeFile(t, srcDir, "a.bin", 64<<10)
	bPath, bWant := writeFile(t, srcDir, "b.bin", 128<<10)

	list := NewFileList()
	if err := list.Add(aPath, ""); err != nil {
		t.Fatalf("add a: %v", err)
	}
	if err := list.Add(bPath, "nested/b.bin"); err != nil {
		t.Fatalf("add b: %v", err)
	}

	if _, err := h.send.SendFiles(h.addr, list); err != nil {
		t.Fatalf("SendFiles: %v", err)
	}
	if ok := h.done.wait(t, 30*time.Second); !ok {
		t.Fatal("transfer reported as failed")
	}

	if got, _ := digestOf(t, filepath.Join(h.recvDir, "a.bin")); got != aWant {
		t.Error("a.bin did not survive the transfer")
	}
	if got, _ := digestOf(t, filepath.Join(h.recvDir, "nested", "b.bin")); got != bWant {
		t.Error("nested/b.bin did not survive the transfer")
	}
}

// ── the fingerprint gate ──────────────────────────────────────────────────────

// TestSenderRefusesUnconfirmedPeer pins the whole M1 security model on the
// sending side: anything but an explicit yes must move no bytes.
func TestSenderRefusesUnconfirmedPeer(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	h.sVerify.peerAns = false

	srcPath, _ := writeFile(t, t.TempDir(), "secret.bin", 4096)
	list := NewFileList()
	if err := list.Add(srcPath, ""); err != nil {
		t.Fatalf("add: %v", err)
	}

	_, err := h.send.SendFiles(h.addr, list)
	if !errors.Is(err, ErrPeerRefused) {
		t.Fatalf("SendFiles error = %v, want ErrPeerRefused", err)
	}
	if _, statErr := os.Stat(filepath.Join(h.recvDir, "secret.bin")); statErr == nil {
		t.Error("the file was written even though the fingerprint was refused")
	}
}

// TestSenderRefusesUnpairedPeer is M2's rule on the sending side: a device this
// one never paired with gets nothing, whatever the UI callbacks say.
func TestSenderRefusesUnpairedPeer(t *testing.T) {
	h := newHarness(t)

	// Undo the harness's pairing on the sending side only, leaving a sender
	// that is trusted by the receiver but trusts nobody itself.
	if err := h.sendIden.Unpair(h.recvIden.DeviceID()); err != nil {
		t.Fatalf("unpair: %v", err)
	}
	h.start(t)

	srcPath, _ := writeFile(t, t.TempDir(), "secret.bin", 4096)
	list := NewFileList()
	if err := list.Add(srcPath, ""); err != nil {
		t.Fatalf("add: %v", err)
	}

	_, err := h.send.SendFiles(h.addr, list)
	if !errors.Is(err, ErrNotPaired) {
		t.Fatalf("SendFiles error = %v, want ErrNotPaired", err)
	}
	if _, statErr := os.Stat(filepath.Join(h.recvDir, "secret.bin")); statErr == nil {
		t.Error("the file was written to a device that was never paired")
	}
}

// TestReceiverRefusesUnpairedPeer is the same rule from the other side, and the
// one that actually matters: the receiver must refuse before any capability
// message is dispatched, whatever the sender believes about itself.
func TestReceiverRefusesUnpairedPeer(t *testing.T) {
	h := newHarness(t)

	// The receiver forgets the sender; the sender still holds its pin, so its
	// own check passes and only the receiver's gate is left.
	if err := h.recvIden.Unpair(h.sendIden.DeviceID()); err != nil {
		t.Fatalf("unpair: %v", err)
	}
	h.start(t)

	srcPath, _ := writeFile(t, t.TempDir(), "secret.bin", 4096)
	list := NewFileList()
	if err := list.Add(srcPath, ""); err != nil {
		t.Fatalf("add: %v", err)
	}

	if _, err := h.send.SendFiles(h.addr, list); err == nil {
		t.Fatal("a one-sided pairing was enough to transfer")
	}
	if _, statErr := os.Stat(filepath.Join(h.recvDir, "secret.bin")); statErr == nil {
		t.Error("the receiver wrote a file from a device it had not paired with")
	}
}

// TestReceiverRefusesUnconfirmedPeer is the same gate on the receiving side:
// the session must not be admitted at all.
func TestReceiverRefusesUnconfirmedPeer(t *testing.T) {
	h := newHarness(t)
	h.rVerify.peerAns = false
	h.start(t)

	srcPath, _ := writeFile(t, t.TempDir(), "secret.bin", 4096)
	list := NewFileList()
	if err := list.Add(srcPath, ""); err != nil {
		t.Fatalf("add: %v", err)
	}

	if _, err := h.send.SendFiles(h.addr, list); err == nil {
		t.Fatal("SendFiles succeeded although the receiver refused the peer")
	}
	if _, statErr := os.Stat(filepath.Join(h.recvDir, "secret.bin")); statErr == nil {
		t.Error("the file was written despite the peer being refused")
	}
}

// TestReceiverWithNoVerifiersRefusesEverything: a receiver left listening with
// no UI attached must be a closed door, not an open drop box. This is the
// binding deliberately inverting the core's own nil-Accept default.
func TestReceiverWithNoVerifiersRefusesEverything(t *testing.T) {
	h := newHarness(t)
	h.recv.SetPeerVerifier(nil)
	h.recv.SetOfferVerifier(nil)
	h.start(t)

	srcPath, _ := writeFile(t, t.TempDir(), "secret.bin", 4096)
	list := NewFileList()
	if err := list.Add(srcPath, ""); err != nil {
		t.Fatalf("add: %v", err)
	}

	if _, err := h.send.SendFiles(h.addr, list); err == nil {
		t.Fatal("SendFiles succeeded against a receiver with no verifiers installed")
	}
	entries, err := os.ReadDir(h.recvDir)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("destination is not empty: %v", entries)
	}
}

// TestReceiverRejectsOffer exercises the second prompt: the peer is accepted,
// the transfer is not.
func TestReceiverRejectsOffer(t *testing.T) {
	h := newHarness(t)
	h.rVerify.offerAns = false
	h.start(t)

	srcPath, _ := writeFile(t, t.TempDir(), "unwanted.bin", 4096)
	list := NewFileList()
	if err := list.Add(srcPath, ""); err != nil {
		t.Fatalf("add: %v", err)
	}

	_, err := h.send.SendFiles(h.addr, list)
	if !errors.Is(err, files.ErrRejected) {
		t.Fatalf("SendFiles error = %v, want files.ErrRejected", err)
	}
	if _, statErr := os.Stat(filepath.Join(h.recvDir, "unwanted.bin")); statErr == nil {
		t.Error("a rejected offer still wrote its file")
	}
	// The peer prompt must have run first: rejecting an offer from a peer you
	// were never shown is a different, worse UX than the one we designed.
	if h.rVerify.lastPeer() == nil {
		t.Error("the offer was rejected without the peer prompt ever running")
	}
}

// ── lifecycle and argument handling ───────────────────────────────────────────

func TestSenderRejectsEmptyList(t *testing.T) {
	id, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	s := NewSender(id, "empty")
	s.SetPeerVerifier(&verifier{peerAns: true})
	if _, err := s.SendFiles("127.0.0.1:1", NewFileList()); !errors.Is(err, ErrNoFiles) {
		t.Fatalf("SendFiles error = %v, want ErrNoFiles", err)
	}
	if s.IsSending() {
		t.Error("IsSending is true after a rejected send")
	}
}

func TestReceiverLifecycle(t *testing.T) {
	id, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "created", "on", "demand")
	r := NewReceiver(id, "lifecycle", dir)

	if r.IsRunning() {
		t.Error("a fresh receiver reports itself running")
	}
	if r.Addr() != "" {
		t.Error("a stopped receiver reports an address")
	}
	if r.Port() != -1 {
		t.Errorf("Port on a stopped receiver = %d, want -1", r.Port())
	}
	if err := r.Stop(); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Stop on a stopped receiver = %v, want ErrNotRunning", err)
	}

	if err := r.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = r.Stop() }()

	if !r.IsRunning() {
		t.Error("IsRunning is false after Start")
	}
	if !strings.HasPrefix(r.Addr(), "127.0.0.1:") {
		t.Errorf("Addr = %q, want a 127.0.0.1 address", r.Addr())
	}
	if r.Port() <= 0 {
		t.Errorf("Port = %d, want the bound port", r.Port())
	}
	if err := r.Start("127.0.0.1:0"); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second Start = %v, want ErrAlreadyRunning", err)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Errorf("Start did not create the destination directory: %v", err)
	}

	if err := r.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if r.IsRunning() {
		t.Error("IsRunning is true after Stop")
	}
}

func TestReceiverRestartsOnANewPort(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	first := h.addr
	if err := h.recv.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := h.recv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("restart: %v", err)
	}
	defer func() { _ = h.recv.Stop() }()
	if h.recv.Addr() == "" {
		t.Fatal("no address after restart")
	}
	if h.recv.Addr() == first {
		t.Log("restart reused the same ephemeral port; harmless, but note it")
	}
}

// ── FileList ─────────────────────────────────────────────────────────────────

func TestFileListValidation(t *testing.T) {
	dir := t.TempDir()
	good, _ := writeFile(t, dir, "good.bin", 32)

	l := NewFileList()

	if err := l.Add(filepath.Join(dir, "missing.bin"), ""); err == nil {
		t.Error("Add accepted a path that does not exist")
	}
	if err := l.Add(dir, ""); err == nil {
		t.Error("Add accepted a directory")
	}
	if err := l.Add(good, "/etc/passwd"); err == nil {
		t.Error("Add accepted an absolute destination path")
	}
	if err := l.Add(good, "../escape.bin"); err == nil {
		t.Error("Add accepted a destination path that climbs out of the root")
	}
	if l.Len() != 0 {
		t.Fatalf("Len = %d after only failed adds, want 0", l.Len())
	}

	if err := l.Add(good, ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if l.Name(0) != "good.bin" {
		t.Errorf("Name(0) = %q, want the base name", l.Name(0))
	}
	if l.Path(0) != good {
		t.Errorf("Path(0) = %q, want %q", l.Path(0), good)
	}
	if l.Size(0) != 32 {
		t.Errorf("Size(0) = %d, want 32", l.Size(0))
	}
	if l.Path(5) != "" || l.Name(5) != "" || l.Size(5) != -1 {
		t.Error("FileList index accessors must be bounds-safe")
	}

	second, _ := writeFile(t, dir, "second.bin", 8)
	if err := l.Add(second, "sub/second.bin"); err != nil {
		t.Fatalf("Add nested: %v", err)
	}
	if l.TotalBytes() != 40 {
		t.Errorf("TotalBytes = %d, want 40", l.TotalBytes())
	}

	l.RemoveAt(0)
	if l.Len() != 1 || l.Name(0) != "sub/second.bin" {
		t.Errorf("after RemoveAt(0): Len=%d Name(0)=%q", l.Len(), l.Name(0))
	}
	l.RemoveAt(42) // must not panic
	l.Clear()
	if l.Len() != 0 || l.TotalBytes() != 0 {
		t.Error("Clear left the list non-empty")
	}
}

// ── identity ─────────────────────────────────────────────────────────────────

func TestLoadIdentityIsStable(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadIdentity(dir)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	b, err := LoadIdentity(dir)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if a.DeviceID() != b.DeviceID() {
		t.Fatalf("device id changed across loads: %q then %q", a.DeviceID(), b.DeviceID())
	}
	if len(a.DeviceID()) != 16 {
		t.Errorf("device id %q is %d characters, want 16 (PROTOCOL.md §2)", a.DeviceID(), len(a.DeviceID()))
	}
	if a.Fingerprint() != FormatFingerprint(a.DeviceID()) {
		t.Error("Identity.Fingerprint disagrees with FormatFingerprint")
	}
	// A device that regenerates its key on restart would have to be re-paired
	// every time, which pinning makes fatal rather than annoying.
	other, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatalf("third load: %v", err)
	}
	if other.DeviceID() == a.DeviceID() {
		t.Error("two separate key directories produced the same device id")
	}
}

func TestFormatFingerprint(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"abcdefghijklmnop", "abcd-efgh-ijkl-mnop"},
		{"abcde", "abcd-e"},
		{"abcd", "abcd"},
		{"", ""},
	} {
		if got := FormatFingerprint(tc.in); got != tc.want {
			t.Errorf("FormatFingerprint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
