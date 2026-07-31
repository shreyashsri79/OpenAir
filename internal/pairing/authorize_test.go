package pairing

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testHandler builds a Handler over a real on-disk trust store, and returns the
// store path so a test can reopen it and prove what survived.
func testHandler(t *testing.T) (*Handler, *identity.FileTrustStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "trust.json")

	local, err := identity.LoadOrCreate(identity.Options{Dir: dir, Tier: identity.TierNone})
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	store, err := identity.OpenTrustStore(path)
	if err != nil {
		t.Fatalf("OpenTrustStore: %v", err)
	}
	h, err := NewHandler(Config{
		Local:       local,
		Store:       store,
		DisplayName: "test",
		Platform:    "linux",
		Confirm: func(context.Context, string, PeerInfo) (bool, error) {
			return true, nil
		},
		Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h, store, path
}

// storePeer writes a paired peer holding pub and returns the record.
func storePeer(t *testing.T, store identity.TrustStore, pub ed25519.PublicKey, level identity.TrustLevel) identity.Peer {
	t.Helper()
	p := identity.Peer{
		DeviceID:          identity.DeriveDeviceID(pub),
		IdentityPublicKey: pub,
		DisplayName:       "peer",
		Platform:          "linux",
		Level:             level,
		AuthPolicy:        "timed",
		CreatedAt:         1,
		LastSeen:          1,
	}
	if err := store.Put(p); err != nil {
		t.Fatalf("Put: %v", err)
	}
	return p
}

// presenting is what the session layer hands Authorize: a peer record built
// from the TLS certificate, not from the store.
func presenting(pub ed25519.PublicKey) identity.Peer {
	return identity.Peer{
		DeviceID:          identity.DeriveDeviceID(pub),
		IdentityPublicKey: pub,
	}
}

func TestAuthorize_PairedPeerAdmitted(t *testing.T) {
	h, store, _ := testHandler(t)
	pub := mustKey(t)
	storePeer(t, store, pub, identity.LevelTrusted)

	if err := h.Authorize(presenting(pub)); err != nil {
		t.Fatalf("a paired peer presenting its pinned key was refused: %v", err)
	}
}

// The gate is the reason M1's nil Authorize could not survive M2: without it
// every inbound connection is admitted.
func TestAuthorize_UnknownPeerRefused(t *testing.T) {
	h, _, _ := testHandler(t)

	err := h.Authorize(presenting(mustKey(t)))
	if err == nil {
		t.Fatal("an unknown peer was admitted with no pairing window open")
	}
	if !errors.Is(err, ErrPairingClosed) {
		t.Fatalf("want ErrPairingClosed, got %v", err)
	}
}

// A peer stored at level unpaired is a different situation from one that was
// never seen, and the errors are distinct so a UI can say something useful.
func TestAuthorize_StoredButUnpairedRefused(t *testing.T) {
	h, store, _ := testHandler(t)
	pub := mustKey(t)
	storePeer(t, store, pub, identity.LevelUnpaired)

	err := h.Authorize(presenting(pub))
	if err == nil {
		t.Fatal("a peer stored at level unpaired was admitted")
	}
	if !errors.Is(err, ErrUnpaired) {
		t.Fatalf("want ErrUnpaired, got %v", err)
	}
}

// PROTOCOL.md §2: a key that is not the pinned one MUST fail the connection and
// MUST surface as a re-pair prompt, never as something to dial again for.
func TestAuthorize_KeyMismatchHardFailsAndIsNotRetryable(t *testing.T) {
	h, store, _ := testHandler(t)
	pinned := mustKey(t)
	stored := storePeer(t, store, pinned, identity.LevelTrusted)

	// Same DeviceID claimed, different key presented -- the shape an attacker
	// substituting itself for a known peer would produce.
	impostor := identity.Peer{DeviceID: stored.DeviceID, IdentityPublicKey: mustKey(t)}

	err := h.Authorize(impostor)
	if err == nil {
		t.Fatal("a peer presenting a key other than the pinned one was admitted")
	}
	if !errors.Is(err, identity.ErrKeyMismatch) {
		t.Fatalf("want a key mismatch, got %v", err)
	}
	if Retryable(err) {
		t.Fatal("a pinned-key mismatch was reported retryable; §2 forbids dialling again on it")
	}
}

// A mismatch stays a mismatch while a pairing window is open. The window admits
// peers that are *unknown*, not peers whose pinned key changed underneath us --
// otherwise opening the pairing screen would silently downgrade every existing
// pin.
func TestAuthorize_WindowDoesNotExcuseAKeyMismatch(t *testing.T) {
	h, store, _ := testHandler(t)
	pinned := mustKey(t)
	stored := storePeer(t, store, pinned, identity.LevelTrusted)

	defer h.OpenWindow()()

	err := h.Authorize(identity.Peer{DeviceID: stored.DeviceID, IdentityPublicKey: mustKey(t)})
	if !errors.Is(err, identity.ErrKeyMismatch) {
		t.Fatalf("an open pairing window excused a pinned-key mismatch: %v", err)
	}
}

func TestAuthorize_NoCertificateRefused(t *testing.T) {
	h, store, _ := testHandler(t)
	pub := mustKey(t)
	stored := storePeer(t, store, pub, identity.LevelTrusted)

	err := h.Authorize(identity.Peer{DeviceID: stored.DeviceID})
	if !errors.Is(err, identity.ErrNoPeerCertificate) {
		t.Fatalf("want ErrNoPeerCertificate, got %v", err)
	}
}

func TestOpenWindow_AdmitsUnpairedThenShuts(t *testing.T) {
	h, _, _ := testHandler(t)
	pub := mustKey(t)

	if h.WindowOpen() {
		t.Fatal("a fresh handler reports a pairing window already open")
	}
	closeWindow := h.OpenWindow()
	if !h.WindowOpen() {
		t.Fatal("OpenWindow did not open a window")
	}
	if err := h.Authorize(presenting(pub)); err != nil {
		t.Fatalf("unpaired peer refused while a pairing window was open: %v", err)
	}

	closeWindow()
	if h.WindowOpen() {
		t.Fatal("closing the window left it open")
	}
	if err := h.Authorize(presenting(pub)); err == nil {
		t.Fatal("unpaired peer still admitted after the window closed")
	}
}

// Two UI surfaces may each be waiting to pair. The door stays open until both
// give up, and a closer that runs twice must not double-count.
func TestOpenWindow_NestsAndIsIdempotent(t *testing.T) {
	h, _, _ := testHandler(t)

	first := h.OpenWindow()
	second := h.OpenWindow()

	first()
	first()
	if !h.WindowOpen() {
		t.Fatal("one closer, called twice, shut a window another caller still holds")
	}

	second()
	if h.WindowOpen() {
		t.Fatal("window still open after every holder released it")
	}
}

// PRD R2 and the M2 test list: the pinning has to outlive the process. This
// reopens the file the way a restarted daemon would.
func TestTrustStore_SurvivesRestart(t *testing.T) {
	h, store, path := testHandler(t)
	pub := mustKey(t)
	want := storePeer(t, store, pub, identity.LevelTrusted)

	// Same process, fresh handle on the same file: everything the first store
	// held has to come back off disk.
	reopened, err := identity.OpenTrustStore(path)
	if err != nil {
		t.Fatalf("reopening the trust store: %v", err)
	}
	got, ok := reopened.Get(want.DeviceID)
	if !ok {
		t.Fatal("the paired peer was gone after restart")
	}
	if got.Level != identity.LevelTrusted {
		t.Fatalf("peer came back at level %d, want %d", got.Level, identity.LevelTrusted)
	}
	if string(got.IdentityPublicKey) != string(pub) {
		t.Fatal("the pinned identity key did not survive restart")
	}

	// And a handler built over the reopened store admits it, which is the
	// property that actually matters: pairing once is enough.
	restarted, err := NewHandler(Config{
		Local:   h.cfg.Local,
		Store:   reopened,
		Confirm: h.cfg.Confirm,
		Logger:  quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if err := restarted.Authorize(presenting(pub)); err != nil {
		t.Fatalf("a peer paired before the restart was refused after it: %v", err)
	}
}

// Authorize records that the peer connected. It is best effort, but when it
// works the timestamp has to actually move.
func TestAuthorize_RecordsLastSeen(t *testing.T) {
	h, store, _ := testHandler(t)
	pub := mustKey(t)
	before := storePeer(t, store, pub, identity.LevelTrusted)

	h.now = func() time.Time { return time.UnixMilli(before.LastSeen + 60_000) }

	if err := h.Authorize(presenting(pub)); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	after, ok := store.Get(before.DeviceID)
	if !ok {
		t.Fatal("peer vanished from the store")
	}
	if after.LastSeen <= before.LastSeen {
		t.Fatalf("LastSeen did not advance: was %d, now %d", before.LastSeen, after.LastSeen)
	}
	if after.Level != before.Level {
		t.Fatalf("recording last-seen changed the trust level to %d", after.Level)
	}
}

func TestNewHandler_RequiresConfirm(t *testing.T) {
	dir := t.TempDir()
	local, err := identity.LoadOrCreate(identity.Options{Dir: dir, Tier: identity.TierNone})
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	store, err := identity.OpenTrustStore(filepath.Join(dir, "trust.json"))
	if err != nil {
		t.Fatalf("OpenTrustStore: %v", err)
	}
	if _, err := NewHandler(Config{Local: local, Store: store}); !errors.Is(err, ErrNoConfirm) {
		t.Fatalf("a Config with no Confirm was accepted: %v", err)
	}
}
