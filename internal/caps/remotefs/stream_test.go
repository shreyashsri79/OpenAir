package remotefs

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/session"
)

// M11: the client that makes a remote file behave like a local one. The source
// is unchanged and stays unchanged -- every test here asserts on what the
// *client* did with §11.2's one primitive.

// bigFile writes n bytes of deterministic content into a share.
func bigFile(t *testing.T, dir, name string, n int) []byte {
	t.Helper()
	content := make([]byte, n)
	rnd := rand.New(rand.NewSource(int64(n)))
	rnd.Read(content)
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
		t.Fatal(err)
	}
	return content
}

// TestStreamingReadsTheWholeFile is the floor: whatever read-ahead does, the
// bytes that come out are the bytes that are there.
func TestStreamingReadsTheWholeFile(t *testing.T) {
	dir, cfg := shareDir(t, "media")
	want := bigFile(t, dir, "film.bin", 5*blockSize+1234)

	client, sess := newFakePair(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r, err := client.Open(ctx, sess, "media/film.bin", OpenOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	if r.Size() != uint64(len(want)) {
		t.Fatalf("size %d, want %d", r.Size(), len(want))
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes, want %d, and they differ", len(got), len(want))
	}
}

// TestSeekingInsideAFortyGigabyteFile is the milestone's stated bar: seek into a
// 40 GB file and get bytes back in under a second on a LAN.
//
// The file is sparse, so this costs no disk. What it proves is the thing that
// would otherwise be false: a seek reads the block it landed in and nothing
// before it. A client that walked the file, or one whose read-ahead started at
// zero, would take hours here rather than milliseconds.
func TestSeekingInsideAFortyGigabyteFile(t *testing.T) {
	dir, cfg := shareDir(t, "media")
	const size = 40 << 30

	f, err := os.Create(filepath.Join(dir, "huge.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Skipf("no sparse files here: %v", err)
	}
	// A marker deep inside, so the read is proved to have landed where it said.
	const at = 30 << 30
	if _, err := f.WriteAt([]byte("here"), at); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	client, sess := newFakePair(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	r, err := client.Open(ctx, sess, "media/huge.bin", OpenOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	started := time.Now()
	if _, err := r.Seek(at, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatalf("read after seek: %v", err)
	}
	took := time.Since(started)

	if string(got) != "here" {
		t.Fatalf("landed on %q", got)
	}
	if took > time.Second {
		t.Fatalf("first byte after a seek took %v; the bar is one second on a LAN", took)
	}

	// And it did not read the 30 GB in front of it. Read-ahead means more than
	// one read, but every one of them is at or after where we seeked.
	for _, call := range sess.readsSeen() {
		if call.offset < at-blockSize {
			t.Fatalf("a seek to %d read at offset %d", at, call.offset)
		}
	}
}

// TestASeekAbandonsTheReadAheadForTheOldPosition is the reason seeking stays
// fast on a slow path. Read-ahead for the previous position is in flight and
// holding every slot; if a seek waited for it, the first byte after a seek
// would cost the whole window rather than one round trip.
func TestASeekAbandonsTheReadAheadForTheOldPosition(t *testing.T) {
	dir, cfg := shareDir(t, "media")
	bigFile(t, dir, "film.bin", 40*blockSize)

	client, sess := newFakePair(t, cfg)
	// One slow round trip, and only two of them may be in flight at once, so a
	// window queued for the old position is many round trips deep.
	sess.latency = 40 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	r, err := client.Open(ctx, sess, "media/film.bin", OpenOptions{
		Parallel:    2,
		WindowBytes: 16 * blockSize,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	// Read at the front, which queues sixteen blocks against two slots.
	head := make([]byte, 8)
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatalf("first read: %v", err)
	}

	if _, err := r.Seek(35*blockSize, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	started := time.Now()
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatalf("read after seek: %v", err)
	}
	took := time.Since(started)

	// One round trip is 40 ms. Draining the queued window instead would be
	// seven of them at best. Four is comfortably between the two and does not
	// mind a slow machine.
	if took > 4*sess.latency {
		t.Fatalf("first byte after a seek took %v, about %d round trips; the read-ahead was not abandoned",
			took, took/sess.latency)
	}
}

// TestReadAheadWidensOnASlowPath. §11.4 asks for a window of roughly RTT times
// the observed bitrate; a relayed path has two orders of magnitude more RTT
// than a LAN and should be reading further ahead because of it.
func TestReadAheadWidensOnASlowPath(t *testing.T) {
	dir, cfg := shareDir(t, "media")
	bigFile(t, dir, "film.bin", 4*blockSize)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	open := func(info session.PathInfo) *Reader {
		client, sess := newFakePair(t, cfg)
		sess.path = &info
		r, err := client.Open(ctx, sess, "media/film.bin", OpenOptions{})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { r.Close() })
		return r
	}

	lan := open(session.PathInfo{Class: "lan", RTTMillis: 1, BandwidthBytes: 100 << 20})
	relayed := open(session.PathInfo{Class: "relayed", RTTMillis: 180, BandwidthBytes: 20 << 20})

	if lan.WindowBytes() != minWindowBlocks*blockSize {
		t.Fatalf("a LAN window of %d bytes; the floor is %d", lan.WindowBytes(), minWindowBlocks*blockSize)
	}
	if relayed.WindowBytes() <= lan.WindowBytes() {
		t.Fatalf("a relayed path reads ahead %d bytes and a LAN %d; the slow path should read further ahead",
			relayed.WindowBytes(), lan.WindowBytes())
	}
	if relayed.WindowBytes() > maxWindowBlocks*blockSize {
		t.Fatalf("a relayed window of %d bytes is past the cap", relayed.WindowBytes())
	}
}

// TestAShortAnsweringSourceIsStitchedBackTogether. §11.2 lets a source answer
// less than was asked for; a block is then several reads, and the client owes
// the caller the whole thing anyway.
func TestAShortAnsweringSourceIsStitchedBackTogether(t *testing.T) {
	dir, cfg := shareDir(t, "media")
	cfg.MaxRead = 4096
	want := bigFile(t, dir, "film.bin", 3*blockSize)

	client, sess := newFakePair(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	r, err := client.Open(ctx, sess, "media/film.bin", OpenOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	got := make([]byte, 2*blockSize)
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want[:len(got)]) {
		t.Fatal("a short-answering source produced the wrong bytes")
	}
	if len(sess.readsSeen()) < 2*blockSize/4096 {
		t.Fatalf("only %d reads for %d bytes at a 4 KiB quantum", len(sess.readsSeen()), len(got))
	}
}

// TestReadAtDoesNotMoveThePosition, because a player reading an index while
// playback continues is the ordinary case and the two must not fight.
func TestReadAtDoesNotMoveThePosition(t *testing.T) {
	dir, cfg := shareDir(t, "media")
	want := bigFile(t, dir, "film.bin", 3*blockSize)

	client, sess := newFakePair(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r, err := client.Open(ctx, sess, "media/film.bin", OpenOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	head := make([]byte, 16)
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatalf("read: %v", err)
	}

	tail := make([]byte, 16)
	if _, err := r.ReadAt(tail, int64(len(want))-16); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(tail, want[len(want)-16:]) {
		t.Fatal("ReadAt returned the wrong bytes")
	}

	next := make([]byte, 16)
	if _, err := io.ReadFull(r, next); err != nil {
		t.Fatalf("read after ReadAt: %v", err)
	}
	if !bytes.Equal(next, want[16:32]) {
		t.Fatal("ReadAt moved the read position")
	}
}

// TestSeekingRelativeToTheEnd is what a player does first: read the container's
// footer.
func TestSeekingRelativeToTheEnd(t *testing.T) {
	dir, cfg := shareDir(t, "media")
	want := bigFile(t, dir, "film.bin", blockSize+9)

	client, sess := newFakePair(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r, err := client.Open(ctx, sess, "media/film.bin", OpenOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	if _, err := r.Seek(-9, io.SeekEnd); err != nil {
		t.Fatalf("seek: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want[len(want)-9:]) {
		t.Fatalf("read %q from the end", got)
	}

	if _, err := r.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("a seek before the start of the file was allowed")
	}
}

// TestTheCacheAnswersASecondPassWithoutTheWire. The cache is optional, so this
// asserts the only thing that makes it worth having.
func TestTheCacheAnswersASecondPassWithoutTheWire(t *testing.T) {
	dir, cfg := shareDir(t, "media")
	want := bigFile(t, dir, "film.bin", 3*blockSize)

	cache, err := NewCache(filepath.Join(t.TempDir(), "cache"), DefaultCacheBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	client, sess := newFakePair(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	read := func() []byte {
		r, err := client.Open(ctx, sess, "media/film.bin", OpenOptions{Cache: cache})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer r.Close()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return got
	}

	if !bytes.Equal(read(), want) {
		t.Fatal("the first pass read the wrong bytes")
	}
	first := len(sess.readsSeen())
	if first == 0 {
		t.Fatal("the first pass read nothing over the wire")
	}

	if !bytes.Equal(read(), want) {
		t.Fatal("the cached pass read the wrong bytes")
	}
	if got := len(sess.readsSeen()); got != first {
		t.Fatalf("a cached pass still made %d wire reads", got-first)
	}
}

// TestTheCacheIsEncryptedAtRest. §11.4 and PRD K8: this directory holds another
// device's files, and it is on a disk that may be shared, backed up or stolen.
func TestTheCacheIsEncryptedAtRest(t *testing.T) {
	dir, cfg := shareDir(t, "media")
	const secret = "the-contents-of-someone-elses-file"
	body := strings.Repeat(secret+" ", 4096)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cacheDir := filepath.Join(t.TempDir(), "cache")
	cache, err := NewCache(cacheDir, DefaultCacheBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	client, sess := newFakePair(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r, err := client.Open(ctx, sess, "media/notes.txt", OpenOptions{Cache: cache})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); err != nil {
		t.Fatal(err)
	}
	r.Close()

	if cache.Bytes() == 0 {
		t.Fatal("nothing was cached, so this test proves nothing")
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no cache files on disk")
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(cacheDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("%s holds the file's contents in the clear", e.Name())
		}
		// The name must not give it away either: it is a digest, not a path.
		if strings.Contains(e.Name(), "notes") {
			t.Fatalf("the cache file is named %q, which names the remote file", e.Name())
		}
	}
}

// TestTheCacheStaysUnderItsCap. Uncapped, this fills the disk of whoever
// browsed a big share -- which is why §11.4 makes the cap mandatory.
func TestTheCacheStaysUnderItsCap(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	const cap = 256 << 10
	cache, err := NewCache(cacheDir, cap)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	block := bytes.Repeat([]byte{7}, 32<<10)
	for i := uint64(0); i < 40; i++ {
		cache.Put("k", i, block)
	}

	if cache.Bytes() > cap {
		t.Fatalf("the cache holds %d bytes with a %d-byte cap", cache.Bytes(), cap)
	}
	var onDisk int64
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		onDisk += info.Size()
	}
	if onDisk > cap {
		t.Fatalf("%d bytes on disk with a %d-byte cap", onDisk, cap)
	}

	// The most recent block survives; the first one was evicted long ago.
	if _, ok := cache.Get("k", 39); !ok {
		t.Fatal("the newest block was evicted")
	}
	if _, ok := cache.Get("k", 0); ok {
		t.Fatal("the oldest block survived a cache eight times its size")
	}
}

// TestANewCacheDoesNotTrustAnOldOne. The key is per process (D-78), so anything
// left behind is unreadable; leaving it there would be a directory that only
// ever grows.
func TestANewCacheDoesNotTrustAnOldOne(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")

	first, err := NewCache(cacheDir, DefaultCacheBytes)
	if err != nil {
		t.Fatal(err)
	}
	first.Put("k", 0, []byte("something"))
	if _, ok := first.Get("k", 0); !ok {
		t.Fatal("the cache did not return what it had just stored")
	}

	second, err := NewCache(cacheDir, DefaultCacheBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, ok := second.Get("k", 0); ok {
		t.Fatal("a new cache read a block written under another key")
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("%d files survived from the previous cache", len(entries))
	}

	if err := second.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatal("closing the cache left the directory behind")
	}
}

// TestACacheKeyFollowsTheFileItNames: a file replaced on the source must not be
// served out of the previous version's blocks.
func TestACacheKeyFollowsTheFileItNames(t *testing.T) {
	const dev = "aaaaaaaaaaaaaaaa"
	base := cacheKey(dev, "media/film.bin", 100, 1)
	for _, other := range []string{
		cacheKey(dev, "media/film.bin", 101, 1),
		cacheKey(dev, "media/film.bin", 100, 2),
		cacheKey(dev, "media/other.bin", 100, 1),
		cacheKey("bbbbbbbbbbbbbbbb", "media/film.bin", 100, 1),
	} {
		if other == base {
			t.Fatal("two different files share a cache key")
		}
	}
}

// TestAClosedReaderStopsReading, because read-ahead is a goroutine holding a
// session, and a player that closed the file has gone.
func TestAClosedReaderStopsReading(t *testing.T) {
	dir, cfg := shareDir(t, "media")
	bigFile(t, dir, "film.bin", 20*blockSize)

	client, sess := newFakePair(t, cfg)
	sess.latency = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r, err := client.Open(ctx, sess, "media/film.bin", OpenOptions{Parallel: 1, WindowBytes: 16 * blockSize})
	if err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 8)
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatal(err)
	}
	r.Close()

	before := len(sess.readsSeen())
	time.Sleep(200 * time.Millisecond)
	if after := len(sess.readsSeen()); after > before+1 {
		t.Fatalf("a closed reader made %d further reads", after-before)
	}
	if _, err := r.Read(head); err == nil {
		t.Fatal("a closed reader kept reading")
	}
}
