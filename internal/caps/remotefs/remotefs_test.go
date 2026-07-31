package remotefs

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shreyashsri79/openair/internal/session"
)

func shareDir(t *testing.T, name string) (dir string, cfg Config) {
	t.Helper()
	dir = t.TempDir()
	return dir, Config{Roots: []Root{{Name: name, Path: dir}}}
}

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

// TestBrowseAndRead is M10 as a user meets it: find out what is shared, look
// inside it, and read part of a file without transferring the whole thing.
func TestBrowseAndRead(t *testing.T) {
	dir, cfg := shareDir(t, "docs")
	write(t, dir, "notes.txt", "the quick brown fox")
	write(t, dir, "sub/deep.txt", "further in")

	client, sess := newPair(t, cfg)
	ctx := context.Background()

	// The empty path is the share list. Without it a client has no way to
	// discover what it may browse (D-72).
	shares, truncated, err := client.List(ctx, sess, "", 0, 0)
	if err != nil {
		t.Fatalf("list shares: %v", err)
	}
	if truncated || len(shares) != 1 || shares[0].Path != "docs" || !shares[0].IsDir {
		t.Fatalf("shares = %+v", shares)
	}

	entries, _, err := client.List(ctx, sess, "docs", 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Path)
	}
	if len(names) != 2 || names[0] != "docs/notes.txt" || names[1] != "docs/sub" {
		t.Fatalf("entries = %v", names)
	}

	st, err := client.Stat(ctx, sess, "docs/notes.txt")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size != 19 || st.IsDir {
		t.Fatalf("stat = %+v", st)
	}
	if !strings.HasPrefix(st.MIME, "text/plain") {
		t.Fatalf("mime = %q, want text/plain", st.MIME)
	}

	// A range read in the middle of the file: the point of §11.2.
	buf := make([]byte, 5)
	n, eof, err := client.ReadAt(ctx, sess, "docs/notes.txt", 4, buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "quick" {
		t.Fatalf("read %q at offset 4, want %q", got, "quick")
	}
	if eof {
		t.Fatal("a read in the middle of a file reported eof")
	}
}

// TestTraversalIsRefused. §11.1 requires UNAUTHORISED, and the code matters:
// NOT_FOUND would tell an attacker which paths exist.
func TestTraversalIsRefused(t *testing.T) {
	dir, cfg := shareDir(t, "docs")
	write(t, dir, "inside.txt", "fine")

	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}

	client, sess := newPair(t, cfg)
	ctx := context.Background()

	for _, p := range []string{
		"docs/../secret.txt",
		"../secret.txt",
		"/etc/passwd",
		"docs/./../../etc/passwd",
		"docs\\..\\secret.txt",
		"nosuchshare/x",
		"C:/windows",
	} {
		t.Run(p, func(t *testing.T) {
			if _, err := client.Stat(ctx, sess, p); err == nil {
				t.Fatalf("stat %q was allowed", p)
			} else if !errors.Is(err, ErrLockedPeer) && !errors.Is(err, ErrRefused) {
				// Both are what the client makes of an UNAUTHORISED reset,
				// which is the code §11.1 requires here; which one depends on
				// whether the request went out with a proof.
				t.Fatalf("stat %q refused with %v, want an UNAUTHORISED reset", p, err)
			}
			buf := make([]byte, 4)
			if _, _, err := client.ReadAt(ctx, sess, p, 0, buf); err == nil {
				t.Fatalf("read %q was allowed", p)
			}
		})
	}
}

// TestASymlinkOutOfTheShareIsNotFollowed. The syntax check cannot see this one:
// the path is ordinary and the escape happens in the filesystem.
func TestASymlinkOutOfTheShareIsNotFollowed(t *testing.T) {
	dir, cfg := shareDir(t, "docs")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	write(t, dir, "real.txt", "fine")

	client, sess := newPair(t, cfg)
	ctx := context.Background()

	if _, err := client.Stat(ctx, sess, "docs/escape.txt"); err == nil {
		t.Fatal("a symlink out of the share was followed")
	}
	// And it is not advertised either: naming it would point a user at a path
	// the source will refuse.
	entries, _, err := client.List(ctx, sess, "docs", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Path, "escape.txt") {
			t.Fatalf("the escaping link is listed: %+v", entries)
		}
	}
}

// TestListingAHundredThousandEntries is §11.1's stated reason for pagination:
// a directory this size must not become one 16 MiB envelope.
func TestListingAHundredThousandEntries(t *testing.T) {
	if testing.Short() {
		t.Skip("creates 100k files")
	}
	dir, cfg := shareDir(t, "big")
	for i := 0; i < 100_000; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%06d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	client, sess := newPair(t, cfg)
	ctx := context.Background()

	seen := 0
	offset := 0
	for {
		entries, truncated, err := client.List(ctx, sess, "big", offset, MaxListLimit)
		if err != nil {
			t.Fatalf("list at %d: %v", offset, err)
		}
		if len(entries) > MaxListLimit {
			t.Fatalf("a page carried %d entries, over the %d cap", len(entries), MaxListLimit)
		}
		// Paging depends on the order being stable across calls; readdir order
		// is not, which is why the source sorts.
		if want := fmt.Sprintf("big/f%06d", offset); len(entries) > 0 && entries[0].Path != want {
			t.Fatalf("page at %d starts with %s, want %s", offset, entries[0].Path, want)
		}
		seen += len(entries)
		offset += len(entries)
		if !truncated {
			break
		}
		if len(entries) == 0 {
			t.Fatal("truncated, but the page was empty")
		}
	}
	if seen != 100_000 {
		t.Fatalf("paged through %d entries, want 100000", seen)
	}
}

// TestShortReadsAreTheNormalCase. §11.2 lets a source answer with less than was
// asked for and requires clients to cope; this asserts both halves.
func TestShortReadsAreTheNormalCase(t *testing.T) {
	dir, cfg := shareDir(t, "docs")
	payload := make([]byte, 300<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.MaxRead = 64 << 10 // a source that chose a smaller quantum

	client, sess := newPair(t, cfg)
	ctx := context.Background()

	buf := make([]byte, len(payload))
	n, _, err := client.ReadAt(ctx, sess, "docs/big.bin", 0, buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n != cfg.MaxRead {
		t.Fatalf("one read returned %d bytes, want the source's %d-byte quantum", n, cfg.MaxRead)
	}

	// ReadFull is the client-side loop that turns short reads into a range.
	got := make([]byte, len(payload))
	total, err := client.ReadFull(ctx, sess, "docs/big.bin", 0, got)
	if err != nil {
		t.Fatalf("read full: %v", err)
	}
	if total != len(payload) || !bytes.Equal(got, payload) {
		t.Fatalf("read %d bytes of %d, and they %s", total, len(payload),
			map[bool]string{true: "match", false: "differ"}[bytes.Equal(got, payload)])
	}
}

// TestReadingPastTheEnd: a client that seeked beyond what the file was when it
// looked gets an empty answer and eof, not an error.
func TestReadingPastTheEnd(t *testing.T) {
	dir, cfg := shareDir(t, "docs")
	write(t, dir, "small.txt", "12345")

	client, sess := newPair(t, cfg)
	ctx := context.Background()

	buf := make([]byte, 16)
	n, eof, err := client.ReadAt(ctx, sess, "docs/small.txt", 3, buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "45" || !eof {
		t.Fatalf("read %q eof=%v at offset 3 of a 5-byte file", buf[:n], eof)
	}

	n, eof, err = client.ReadAt(ctx, sess, "docs/small.txt", 900, buf)
	if err != nil {
		t.Fatalf("a read past the end failed: %v", err)
	}
	if n != 0 || !eof {
		t.Fatalf("a read past the end returned %d bytes, eof=%v", n, eof)
	}
}

// TestAMissingPathIsNotFound, rather than REJECTED — which §10 defines as "user
// declined" and would send a user looking for a prompt nobody saw (D-73).
func TestAMissingPathIsNotFound(t *testing.T) {
	_, cfg := shareDir(t, "docs")
	client, sess := newPair(t, cfg)

	_, err := client.Stat(context.Background(), sess, "docs/absent.txt")
	if err == nil {
		t.Fatal("stat of a missing file succeeded")
	}
	if !strings.Contains(err.Error(), "no such path") {
		t.Fatalf("stat of a missing file reported %v", err)
	}
}

// TestSharingNothingSharesNothing: the zero Config is a device that offers no
// filesystem at all, which is what a daemon started without --share has.
func TestSharingNothingSharesNothing(t *testing.T) {
	client, sess := newPair(t, Config{})
	ctx := context.Background()

	shares, _, err := client.List(ctx, sess, "", 0, 0)
	if err != nil {
		t.Fatalf("list shares: %v", err)
	}
	if len(shares) != 0 {
		t.Fatalf("a device sharing nothing listed %+v", shares)
	}
	if _, err := client.Stat(ctx, sess, "anything/at/all"); err == nil {
		t.Fatal("a device sharing nothing answered a stat")
	}
}

// TestThumbnails is §11.3: the source renders, so browsing a folder of photos
// does not transfer them.
func TestThumbnails(t *testing.T) {
	dir, cfg := shareDir(t, "photos")
	cfg.Thumbnails = true

	src := image.NewRGBA(image.Rect(0, 0, 900, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 900; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 40, A: 255})
		}
	}
	f, err := os.Create(filepath.Join(dir, "big.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, src); err != nil {
		t.Fatal(err)
	}
	f.Close()
	original, err := os.Stat(filepath.Join(dir, "big.png"))
	if err != nil {
		t.Fatal(err)
	}

	client, sess := newPair(t, cfg)
	ctx := context.Background()

	mime, data, err := client.Thumb(ctx, sess, "photos/big.png", 128)
	if err != nil {
		t.Fatalf("thumb: %v", err)
	}
	if mime != ThumbMIME {
		t.Fatalf("thumb mime = %q", mime)
	}
	cfgImg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the thumbnail is not an image: %v", err)
	}
	if cfgImg.Width != 128 || cfgImg.Height != 85 {
		t.Fatalf("thumbnail is %dx%d, want 128x85 for a 900x600 source", cfgImg.Width, cfgImg.Height)
	}
	if int64(len(data)) >= original.Size() {
		t.Fatalf("the thumbnail is %d bytes and the original is %d; that is not a preview",
			len(data), original.Size())
	}

	// Asking again is served from the cache (§11.3 SHOULD), which the test can
	// only observe as the same bytes coming back.
	_, again, err := client.Thumb(ctx, sess, "photos/big.png", 128)
	if err != nil || !bytes.Equal(again, data) {
		t.Fatalf("the second thumbnail differs: %v", err)
	}

	// A file that is not an image is refused rather than served as one.
	write(t, dir, "notes.txt", "not an image")
	if _, _, err := client.Thumb(ctx, sess, "photos/notes.txt", 128); err == nil {
		t.Fatal("a text file was rendered as a thumbnail")
	}
}

// TestThumbnailsAreOffByDefault: generating one costs the source real work on a
// file it was only asked to list, so it is opt-in.
func TestThumbnailsAreOffByDefault(t *testing.T) {
	dir, cfg := shareDir(t, "photos")
	write(t, dir, "x.png", "not really a png")

	client, sess := newPair(t, cfg)
	if _, _, err := client.Thumb(context.Background(), sess, "photos/x.png", 64); err == nil {
		t.Fatal("thumbnails were generated without being enabled")
	}
}

// TestRefusalIsLegible: an UNAUTHORISED reset is the one failure a user can
// act on, so it does not arrive as a transport error.
func TestRefusalIsLegible(t *testing.T) {
	_, cfg := shareDir(t, "docs")
	source := New(cfg)
	sess := &fakeSession{t: t, source: source, resetWith: session.CodeUnauthorised}
	client := New(Config{})

	_, err := client.Stat(context.Background(), sess, "docs")
	if !errors.Is(err, ErrLockedPeer) && !errors.Is(err, ErrRefused) {
		t.Fatalf("an UNAUTHORISED refusal surfaced as %v", err)
	}
}

// TestWirePathRules covers the syntax gate on its own, including the cases the
// filesystem would never see because they are refused first.
func TestWirePathRules(t *testing.T) {
	for _, p := range []string{"", "/", ".", "..", "a/../b", "a//b", "a/./b", "a\\b", "a:b", "docs/\x00x"} {
		if _, err := safeWirePath(p); err == nil {
			t.Errorf("safeWirePath(%q) was accepted", p)
		}
	}
	for _, p := range []string{"docs", "/docs/", "docs/sub/file.txt", "docs/a b/c.txt"} {
		if _, err := safeWirePath(p); err != nil {
			t.Errorf("safeWirePath(%q) was refused: %v", p, err)
		}
	}
	for _, p := range []string{"", "/", "."} {
		if !isRootPath(p) {
			t.Errorf("isRootPath(%q) = false", p)
		}
	}
}
