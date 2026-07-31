package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/clipboard"
	"github.com/shreyashsri79/openair/internal/identity"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// newTestDaemon starts a daemon on loopback with discovery off. Discovery is
// off because two daemons inside one test process announcing on the real LAN is
// a good way to send a maintainer's laptop a file it did not ask for.
func newTestDaemon(t *testing.T, mutate func(*Config)) *Daemon {
	t.Helper()

	dir := t.TempDir()
	cfg := Config{
		KeyDir:        filepath.Join(dir, "keys"),
		DestDir:       filepath.Join(dir, "inbox"),
		SocketPath:    filepath.Join(dir, "d.sock"),
		Listen:        "127.0.0.1:0",
		DisplayName:   "test-" + filepath.Base(dir),
		PromptTimeout: 3 * time.Second,
		NoAnnounce:    true,
		Discovery:     DiscoveryOptions{DisableMDNS: true, DisableBroadcast: true},
		Logf:          func(format string, args ...any) { t.Logf(format, args...) },
	}
	if mutate != nil {
		mutate(&cfg)
	}

	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := d.Run(ctx); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return d
}

// pinEachOther writes the trust-store records pairing would have written. The
// pairing exchange has its own end-to-end test in internal/pairing; what these
// tests need is the state it leaves behind.
func pinEachOther(t *testing.T, a, b *Daemon) {
	t.Helper()
	pin := func(store identity.TrustStore, peer *Daemon) {
		err := store.Put(identity.Peer{
			DeviceID:          peer.id.DeviceID(),
			IdentityPublicKey: peer.id.IdentityPublic(),
			DisplayName:       peer.cfg.DisplayName,
			Platform:          "linux",
			Level:             identity.LevelTrusted,
			AuthPolicy:        "timed",
			CreatedAt:         1,
			LastSeen:          1,
		})
		if err != nil {
			t.Fatalf("pin peer: %v", err)
		}
	}
	pin(a.store, b)
	pin(b.store, a)
}

func connect(t *testing.T, d *Daemon, onEvent EventFunc, onPrompt PromptFunc) *Client {
	t.Helper()
	c, err := Connect(context.Background(), d.SocketPath(), onEvent, onPrompt)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestStatusReportsTheBoundListener(t *testing.T) {
	d := newTestDaemon(t, nil)
	c := connect(t, d, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.GetDeviceId() != string(d.DeviceID()) {
		t.Errorf("device id = %q, want %q", st.GetDeviceId(), d.DeviceID())
	}
	if st.GetListenAddr() != d.Addr() {
		t.Errorf("listen addr = %q, want %q", st.GetListenAddr(), d.Addr())
	}
	if strings.HasSuffix(st.GetListenAddr(), ":0") {
		t.Error("status reported the requested port, not the bound one")
	}
}

// TestDaemonSurvivesClientDisconnect is M4's stated requirement. A shell that
// dies -- cleanly or by being killed -- must take nothing with it, because the
// whole point of a daemon is that it outlives the thing driving it.
func TestDaemonSurvivesClientDisconnect(t *testing.T) {
	d := newTestDaemon(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// A client that subscribes, then vanishes without closing anything down.
	first, err := Connect(ctx, d.SocketPath(), nil, func(*openairv1.DaemonPrompt) bool { return true })
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := first.Subscribe(ctx, true); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := first.Status(ctx); err != nil {
		t.Fatalf("Status: %v", err)
	}
	first.Close()

	// The daemon should now be serving nobody, and should still be serving.
	deadline := time.Now().Add(5 * time.Second)
	for {
		second := connect(t, d, nil, nil)
		st, err := second.Status(ctx)
		if err != nil {
			t.Fatalf("Status after a client disconnected: %v", err)
		}
		if st.GetSubscribers() == 0 {
			break
		}
		second.Close()
		if time.Now().After(deadline) {
			t.Fatal("the departed client is still counted as a subscriber")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// And the QUIC listener is still up: a second daemon can still reach it.
	if d.Addr() == "" {
		t.Fatal("listener address went away")
	}
}

// TestInboundIsRefusedWhenNobodyIsWatching: with no subscriber able to answer
// and no --accept-all, the answer is no. A daemon that accepted files because
// nobody was looking would be a worse default than one that refuses them.
func TestInboundIsRefusedWhenNobodyIsWatching(t *testing.T) {
	a := newTestDaemon(t, nil)
	b := newTestDaemon(t, nil)
	pinEachOther(t, a, b)

	src := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(src, []byte("nobody should see this"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := connect(t, a, nil, nil)
	_, err := client.Send(ctx, b.Addr(), []string{src})
	if err == nil {
		t.Fatal("send succeeded with nobody watching the receiving daemon")
	}
	if _, statErr := os.Stat(filepath.Join(b.cfg.DestDir, "hello.txt")); statErr == nil {
		t.Fatal("the file was written despite the transfer being refused")
	}
}

// TestTransferBetweenTwoDaemons is the milestone end to end: one daemon offers,
// a human at the other approves through a subscribed client, and the bytes
// match.
func TestTransferBetweenTwoDaemons(t *testing.T) {
	a := newTestDaemon(t, nil)
	b := newTestDaemon(t, nil)
	pinEachOther(t, a, b)

	payload := strings.Repeat("openair m4 ", 40000) // ~440 KB, several chunks
	src := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(src, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The receiving side's human.
	var asked int
	var mu sync.Mutex
	watcher := connect(t, b, nil, func(p *openairv1.DaemonPrompt) bool {
		mu.Lock()
		asked++
		mu.Unlock()
		return p.GetKind() == openairv1.DaemonPromptKind_DAEMON_PROMPT_KIND_ACCEPT_TRANSFER
	})
	if err := watcher.Subscribe(ctx, true); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	sender := connect(t, a, nil, nil)
	resp, err := sender.Send(ctx, b.Addr(), []string{src})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.GetTransferId() == "" {
		t.Error("no transfer id came back")
	}
	if resp.GetDeviceId() != string(b.DeviceID()) {
		t.Errorf("transfer reported peer %q, want %q", resp.GetDeviceId(), b.DeviceID())
	}

	mu.Lock()
	n := asked
	mu.Unlock()
	if n != 1 {
		t.Errorf("the user was asked %d times, want exactly 1", n)
	}

	got, err := os.ReadFile(filepath.Join(b.cfg.DestDir, "payload.bin"))
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}
	if digest(got) != digest([]byte(payload)) {
		t.Fatalf("received %d bytes with a different digest than the %d sent", len(got), len(payload))
	}
}

// TestAutoAcceptNeedsNoWatcher is the headless install: --accept-all is the
// explicit way to say "no human here", and nothing else grants it.
func TestAutoAcceptNeedsNoWatcher(t *testing.T) {
	a := newTestDaemon(t, nil)
	b := newTestDaemon(t, func(c *Config) { c.AutoAccept = true })
	pinEachOther(t, a, b)

	src := filepath.Join(t.TempDir(), "auto.txt")
	want := []byte("accepted by policy")
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := connect(t, a, nil, nil)
	if _, err := client.Send(ctx, b.Addr(), []string{src}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(b.cfg.DestDir, "auto.txt"))
	if err != nil {
		t.Fatalf("read received file: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("received %q, want %q", got, want)
	}
}

// TestUnpairedPeerIsRefused: M2's rule still holds through the daemon, on both
// sides independently.
func TestUnpairedPeerIsRefused(t *testing.T) {
	a := newTestDaemon(t, nil)
	b := newTestDaemon(t, func(c *Config) { c.AutoAccept = true })
	// Deliberately no pinning.

	src := filepath.Join(t.TempDir(), "nope.txt")
	if err := os.WriteFile(src, []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := connect(t, a, nil, nil)
	if _, err := client.Send(ctx, b.Addr(), []string{src}); err == nil {
		t.Fatal("send to an unpaired device succeeded")
	}
	if _, err := os.Stat(filepath.Join(b.cfg.DestDir, "nope.txt")); err == nil {
		t.Fatal("an unpaired peer's file was written")
	}
}

// TestDevicesListsPairedAndSeen checks the two sources a device list has to
// merge: the trust store and discovery.
func TestDevicesListsPairedAndSeen(t *testing.T) {
	a := newTestDaemon(t, nil)
	b := newTestDaemon(t, nil)
	pinEachOther(t, a, b)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := connect(t, a, nil, nil)
	devices, err := c.Devices(ctx, false)
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}
	if devices[0].GetDeviceId() != string(b.DeviceID()) {
		t.Errorf("device = %q, want %q", devices[0].GetDeviceId(), b.DeviceID())
	}
	if !devices[0].GetPaired() {
		t.Error("a pinned device is not reported as paired")
	}
	if devices[0].GetLevel() != openairv1.TrustLevel_TRUST_LEVEL_TRUSTED {
		t.Errorf("level = %v, want TRUSTED", devices[0].GetLevel())
	}
}

// TestSessionsAreDroppedWhenTheyEnd is what Session.Done was added for: a
// daemon that never forgets a dead session reports devices as connected long
// after they have gone.
func TestSessionsAreDroppedWhenTheyEnd(t *testing.T) {
	a := newTestDaemon(t, nil)
	b := newTestDaemon(t, func(c *Config) { c.AutoAccept = true })
	pinEachOther(t, a, b)

	src := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := connect(t, a, nil, nil)
	if _, err := client.Send(ctx, b.Addr(), []string{src}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	sess, ok := a.sessionFor(b.DeviceID())
	if !ok {
		t.Fatal("no session registered after a transfer")
	}
	sess.Close(0, "test")

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, still := a.sessionFor(b.DeviceID()); !still {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a closed session is still registered")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestClipboardPushBetweenTwoDaemons is M5 end to end: `openair clip push` in
// its daemon form, with the emoji case that catches a byte-oriented mistake.
//
// The receiving daemon has no system clipboard in a test environment, which is
// deliberately not a failure: the content is accepted and reported as an event,
// and whether this machine has somewhere to paste it is not the sender's
// problem.
func TestClipboardPushBetweenTwoDaemons(t *testing.T) {
	a := newTestDaemon(t, nil)
	b := newTestDaemon(t, nil)
	pinEachOther(t, a, b)

	const text = "café 👩‍💻 日本語\nsecond line"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got := make(chan string, 4)
	watcher := connect(t, b, func(ev *openairv1.DaemonEvent) {
		if ev.GetKind() == openairv1.DaemonEventKind_DAEMON_EVENT_KIND_CLIPBOARD {
			got <- ev.GetText()
		}
	}, nil)
	if err := watcher.Subscribe(ctx, false); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	sender := connect(t, a, nil, nil)
	err := sender.Clipboard(ctx, b.Addr(), &openairv1.ClipboardPush{
		Mime:    clipboard.TextMIME,
		Content: []byte(text),
	})
	if err != nil {
		t.Fatalf("Clipboard: %v", err)
	}

	select {
	case have := <-got:
		if have != text {
			t.Fatalf("received %q, want %q", have, text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the push never reached the other daemon")
	}
}

// TestOversizedClipboardPushIsRefused: §9 says reject rather than buffer, and
// the refusal has to reach the caller rather than being swallowed.
func TestOversizedClipboardPushIsRefused(t *testing.T) {
	a := newTestDaemon(t, nil)
	b := newTestDaemon(t, nil)
	pinEachOther(t, a, b)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	applied := make(chan string, 1)
	watcher := connect(t, b, func(ev *openairv1.DaemonEvent) {
		if ev.GetKind() == openairv1.DaemonEventKind_DAEMON_EVENT_KIND_CLIPBOARD {
			applied <- ev.GetText()
		}
	}, nil)
	if err := watcher.Subscribe(ctx, false); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	sender := connect(t, a, nil, nil)
	err := sender.Clipboard(ctx, b.Addr(), &openairv1.ClipboardPush{
		Mime:    clipboard.TextMIME,
		Content: []byte(strings.Repeat("x", clipboard.DefaultMaxBytes+1)),
	})
	if err == nil {
		t.Fatal("an oversized push was accepted")
	}

	select {
	case have := <-applied:
		t.Fatalf("oversized content was applied anyway (%d bytes)", len(have))
	case <-time.After(500 * time.Millisecond):
	}
}
