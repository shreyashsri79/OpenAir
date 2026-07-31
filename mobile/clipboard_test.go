package mobile

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// clipRec is a ClipboardCallback that records what arrived. Like every callback
// in this binding it is called on a Go goroutine, so it locks.
type clipRec struct {
	mu    sync.Mutex
	texts []string
	peers []*PeerInfo
	got   chan struct{}
}

func newClipRec() *clipRec { return &clipRec{got: make(chan struct{}, 8)} }

func (c *clipRec) OnClipboard(peer *PeerInfo, text string) {
	c.mu.Lock()
	c.texts = append(c.texts, text)
	c.peers = append(c.peers, peer)
	c.mu.Unlock()
	select {
	case c.got <- struct{}{}:
	default:
	}
}

func (c *clipRec) wait(t *testing.T, d time.Duration) (string, *PeerInfo) {
	t.Helper()
	select {
	case <-c.got:
	case <-time.After(d):
		t.Fatal("no clipboard content arrived")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.texts[len(c.texts)-1], c.peers[len(c.peers)-1]
}

func (c *clipRec) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.texts)
}

// clipHarness is two paired identities, one listening with a clipboard
// registered and one pushing to it.
type clipHarness struct {
	recvIden, sendIden *Identity
	recv               *Receiver
	recvClip, sendClip *Clipboard
	rec                *clipRec
	addr               string
}

func newClipHarness(t *testing.T) *clipHarness {
	t.Helper()

	recvIden, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatalf("receiver identity: %v", err)
	}
	sendIden, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatalf("sender identity: %v", err)
	}
	pairIdentities(t, recvIden, sendIden)

	h := &clipHarness{
		recvIden: recvIden,
		sendIden: sendIden,
		rec:      newClipRec(),
	}
	h.recvClip = NewClipboard(recvIden, "test-receiver")
	h.recvClip.SetClipboardCallback(h.rec)
	h.sendClip = NewClipboard(sendIden, "test-sender")

	h.recv = NewReceiver(recvIden, "test-receiver", t.TempDir())
	h.recv.SetClipboard(h.recvClip)
	if err := h.recv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("start receiver: %v", err)
	}
	t.Cleanup(func() { _ = h.recv.Stop() })

	h.addr = h.recv.Addr()
	if h.addr == "" {
		t.Fatal("receiver reported no address after Start")
	}
	return h
}

// TestClipboardPushThroughBinding is M5 as an Android shell sees it: push text
// from one device, have it arrive on the other through the exported API only.
func TestClipboardPushThroughBinding(t *testing.T) {
	h := newClipHarness(t)

	const text = "café 👋 日本語\nsecond line"
	if err := h.sendClip.Push(h.addr, text); err != nil {
		t.Fatalf("Push: %v", err)
	}

	got, peer := h.rec.wait(t, 15*time.Second)
	if got != text {
		t.Fatalf("received %q, want %q", got, text)
	}
	if peer == nil || peer.DeviceID() != string(h.sendIden.impl.DeviceID()) {
		t.Fatalf("callback named the wrong peer")
	}
	if peer.Fingerprint() == "" {
		t.Error("the callback got no fingerprint to show a user")
	}
}

// TestClipboardRefusesUnpairedPeer: M2's rule holds for the clipboard as it
// does for transfers, and it is refused at both ends independently.
func TestClipboardRefusesUnpairedPeer(t *testing.T) {
	h := newClipHarness(t)

	stranger, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clip := NewClipboard(stranger, "stranger")

	if err := clip.Push(h.addr, "let me in"); err == nil {
		t.Fatal("an unpaired device pushed to a paired one")
	}
	time.Sleep(250 * time.Millisecond)
	if n := h.rec.count(); n != 0 {
		t.Fatalf("%d pushes from an unpaired device reached the callback", n)
	}
}

// TestClipboardRefusesOversized is §9's cap, enforced before anything is dialled
// so a shell learns immediately rather than after a round trip.
func TestClipboardRefusesOversized(t *testing.T) {
	h := newClipHarness(t)

	big := strings.Repeat("x", h.sendClip.MaxBytes()+1)
	if err := h.sendClip.Push(h.addr, big); err == nil {
		t.Fatal("oversized content was pushed")
	}
	time.Sleep(250 * time.Millisecond)
	if n := h.rec.count(); n != 0 {
		t.Fatal("oversized content reached the callback")
	}
}

func TestClipboardRefusesEmptyPush(t *testing.T) {
	h := newClipHarness(t)
	if err := h.sendClip.Push(h.addr, ""); err == nil {
		t.Fatal("an empty push was accepted")
	}
}

// TestReceiverWithoutClipboardIgnoresPushes: registration is opt-in, and a
// shell that never calls SetClipboard must not be reachable through capID 2.
// The push does not fail -- §3.1 makes an unnegotiated capability silent, not
// an error -- but nothing is delivered.
func TestReceiverWithoutClipboardIgnoresPushes(t *testing.T) {
	recvIden, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sendIden, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pairIdentities(t, recvIden, sendIden)

	rec := newClipRec()
	unused := NewClipboard(recvIden, "receiver")
	unused.SetClipboardCallback(rec)

	recv := NewReceiver(recvIden, "receiver", t.TempDir())
	if err := recv.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer recv.Stop()

	_ = NewClipboard(sendIden, "sender").Push(recv.Addr(), "into the void")
	time.Sleep(500 * time.Millisecond)
	if n := rec.count(); n != 0 {
		t.Fatalf("%d pushes reached a receiver with no clipboard registered", n)
	}
}
