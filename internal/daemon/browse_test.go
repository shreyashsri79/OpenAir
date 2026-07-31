package daemon

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/remotefs"
)

// M10 through the daemon, which is where §6's gate actually lives: the
// capability tests can prove a traversal is refused, and only this one can
// prove that an Owned-level request without a proof is.
//
// The browse target is an address rather than a name because these daemons run
// with discovery off, as every test here does -- two daemons inside one process
// have no business announcing on the maintainer's LAN. Naming a device works
// through exactly the same path once discovery or a rendezvous server can say
// where it is (transfer.go's sessionTo), and the unlock is still scoped by
// DeviceID, which is what §6 authorises.

// sharingDaemon is a protected daemon offering dir as a share.
func sharingDaemon(t *testing.T, name, dir string) *Daemon {
	t.Helper()
	return newTestDaemon(t, func(cfg *Config) {
		protect(t, cfg.KeyDir)
		cfg.Shares = []remotefs.Root{{Name: name, Path: dir}}
	})
}

func shareWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestBrowseAndFetchAnUnlockedPeer is the milestone: list another device's
// files, then read one, with nobody watching the far end.
func TestBrowseAndFetchAnUnlockedPeer(t *testing.T) {
	source := sharingDaemon(t, "docs", shareWith(t, map[string]string{
		"notes.txt":    "the quick brown fox",
		"sub/deep.txt": "further in",
	}))
	browser := newProtectedDaemon(t)
	pinOwned(t, browser, source)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bc := connect(t, browser, nil, nil)
	if _, err := bc.Unlock(ctx, string(source.DeviceID()), []byte(testPassphrase), nil, false, time.Minute); err != nil {
		t.Fatalf("unlock: %v", err)
	}

	shares, _, err := bc.Browse(ctx, source.Addr(), "", 0, 0)
	if err != nil {
		t.Fatalf("browse shares: %v", err)
	}
	if len(shares) != 1 || shares[0].GetPath() != "docs" {
		t.Fatalf("shares = %+v", shares)
	}

	entries, truncated, err := bc.Browse(ctx, source.Addr(), "docs", 0, 0)
	if err != nil {
		t.Fatalf("browse docs: %v", err)
	}
	if truncated || len(entries) != 2 {
		t.Fatalf("entries = %+v truncated=%v", entries, truncated)
	}

	dest := filepath.Join(t.TempDir(), "notes.txt")
	n, err := bc.Fetch(ctx, source.Addr(), "docs/notes.txt", dest, 0, 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the quick brown fox" || n != uint64(len(got)) {
		t.Fatalf("fetched %q (%d bytes)", got, n)
	}

	// A range, which is the primitive everything else rests on (§11.2).
	part := filepath.Join(t.TempDir(), "part")
	if _, err := bc.Fetch(ctx, source.Addr(), "docs/notes.txt", part, 4, 5); err != nil {
		t.Fatalf("fetch range: %v", err)
	}
	if got, err := os.ReadFile(part); err != nil || string(got) != "quick" {
		t.Fatalf("range fetch gave %q (%v)", got, err)
	}
}

// TestBrowsingWithoutAnUnlockIsRefused. This is the half the capability's own
// tests cannot reach: the refusal happens in the session layer, against the
// trust store and a missing AuthProof (§6).
func TestBrowsingWithoutAnUnlockIsRefused(t *testing.T) {
	source := sharingDaemon(t, "docs", shareWith(t, map[string]string{"secret.txt": "not without a proof"}))
	browser := newProtectedDaemon(t)
	pinOwned(t, browser, source)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bc := connect(t, browser, nil, nil)
	if _, _, err := bc.Browse(ctx, source.Addr(), "docs", 0, 0); err == nil {
		t.Fatal("a locked device browsed an Owned share")
	}

	dest := filepath.Join(t.TempDir(), "secret.txt")
	if _, err := bc.Fetch(ctx, source.Addr(), "docs/secret.txt", dest, 0, 0); err == nil {
		t.Fatal("a locked device fetched from an Owned share")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("a refused fetch left a file behind")
	}
}

// TestATrustedPeerCannotBrowse: Owned is a level, not a proof. A peer that is
// merely paired is refused even holding a valid unlock of its own, because the
// trust store on the *source* is what decides (§6, D-30).
func TestATrustedPeerCannotBrowse(t *testing.T) {
	source := sharingDaemon(t, "docs", shareWith(t, map[string]string{"x.txt": "no"}))
	browser := newProtectedDaemon(t)

	// Owned on the browser's side, only Trusted on the source's: exactly the
	// state after pairing, before anyone ran `openair trust --owned`.
	pinOwned(t, browser, source)
	pinEachOther(t, browser, source)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bc := connect(t, browser, nil, nil)
	if _, err := bc.Unlock(ctx, string(source.DeviceID()), []byte(testPassphrase), nil, false, time.Minute); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if _, _, err := bc.Browse(ctx, source.Addr(), "docs", 0, 0); err == nil {
		t.Fatal("a Trusted peer browsed a share")
	}
}

// TestTraversalIsRefusedOverTheWire. The capability tests cover the rules; this
// covers that they are still the rules once a real session is in the way.
func TestTraversalIsRefusedOverTheWire(t *testing.T) {
	dir := shareWith(t, map[string]string{"inside.txt": "fine"})
	secret := filepath.Join(filepath.Dir(dir), "outside.txt")
	if err := os.WriteFile(secret, []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}

	source := sharingDaemon(t, "docs", dir)
	browser := newProtectedDaemon(t)
	pinOwned(t, browser, source)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bc := connect(t, browser, nil, nil)
	if _, err := bc.Unlock(ctx, string(source.DeviceID()), []byte(testPassphrase), nil, false, time.Minute); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	for _, p := range []string{"docs/../outside.txt", "../outside.txt", "/etc/passwd"} {
		if _, _, err := bc.Browse(ctx, source.Addr(), p, 0, 0); err == nil {
			t.Fatalf("browsing %q was allowed", p)
		}
	}
}

// TestFetchingALargeFileIsManyReads: §11.2's quantum means a file bigger than
// one read arrives in several, and the client loop is what makes that whole
// again. A file that is exactly a multiple of the quantum is the case that
// catches an off-by-one at the end.
func TestFetchingALargeFileIsManyReads(t *testing.T) {
	dir := t.TempDir()
	payload := make([]byte, 3*remotefs.MaxReadLength)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	source := sharingDaemon(t, "docs", dir)
	browser := newProtectedDaemon(t)
	pinOwned(t, browser, source)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	bc := connect(t, browser, nil, nil)
	if _, err := bc.Unlock(ctx, string(source.DeviceID()), []byte(testPassphrase), nil, false, 5*time.Minute); err != nil {
		t.Fatalf("unlock: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "big.bin")
	n, err := bc.Fetch(ctx, source.Addr(), "docs/big.bin", dest, 0, 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if n != uint64(len(payload)) {
		t.Fatalf("fetched %d bytes of %d", n, len(payload))
	}
	if fileDigest(t, dest) != digest(payload) {
		t.Fatal("the file that arrived is not the file that was shared")
	}
}

// TestADeviceThatSharesNothingSaysSo. Not sharing is the default, and the
// refusal has to be legible enough for a CLI to explain it.
func TestADeviceThatSharesNothingSaysSo(t *testing.T) {
	source := newProtectedDaemon(t)
	browser := newProtectedDaemon(t)
	pinOwned(t, browser, source)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bc := connect(t, browser, nil, nil)
	if _, err := bc.Unlock(ctx, string(source.DeviceID()), []byte(testPassphrase), nil, false, time.Minute); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	entries, _, err := bc.Browse(ctx, source.Addr(), "", 0, 0)
	if err != nil {
		t.Fatalf("listing the shares of a device with none: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a device sharing nothing listed %+v", entries)
	}
	if _, _, err := bc.Browse(ctx, source.Addr(), "anything", 0, 0); err == nil {
		t.Fatal("a device sharing nothing answered a listing")
	} else if !strings.Contains(err.Error(), "may not be shared") && !strings.Contains(err.Error(), "unlock") {
		t.Fatalf("the refusal is not legible: %v", err)
	}
}
