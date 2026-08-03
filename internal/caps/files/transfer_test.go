package files

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// ---------------------------------------------------------------- helpers

type source struct {
	item   Item
	digest [32]byte
	size   int
}

// writeSources creates deterministic pseudo-random source files. Random rather
// than zeroes: a bug that drops or duplicates a chunk is invisible against a
// uniform file.
func writeSources(t *testing.T, dir string, spec map[string]int) []source {
	t.Helper()
	out := make([]source, 0, len(spec))
	// Deterministic iteration so a failure reproduces.
	names := make([]string, 0, len(spec))
	for n := range spec {
		names = append(names, n)
	}
	sortStrings(names)

	for i, name := range names {
		size := spec[name]
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, size)
		rng := rand.New(rand.NewSource(int64(size)*1_000_003 + int64(i)))
		rng.Read(buf)
		if err := os.WriteFile(full, buf, 0o644); err != nil {
			t.Fatal(err)
		}
		out = append(out, source{
			item:   Item{LocalPath: full, RelPath: name},
			digest: sha256.Sum256(buf),
			size:   size,
		})
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func items(srcs []source) []Item {
	out := make([]Item, len(srcs))
	for i, s := range srcs {
		out[i] = s.item
	}
	return out
}

func digestFile(t *testing.T, path string) [32]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// verifyDelivered checks every destination file byte-for-byte by digest, and
// that nothing incomplete was left lying around.
func verifyDelivered(t *testing.T, dstRoot string, srcs []source) {
	t.Helper()
	for _, s := range srcs {
		dst := filepath.Join(dstRoot, filepath.FromSlash(s.item.RelPath))
		fi, err := os.Stat(dst)
		if err != nil {
			t.Fatalf("%s: %v", s.item.RelPath, err)
		}
		if fi.Size() != int64(s.size) {
			t.Fatalf("%s: size %d, want %d", s.item.RelPath, fi.Size(), s.size)
		}
		if got := digestFile(t, dst); got != s.digest {
			t.Fatalf("%s: sha256 %x, want %x", s.item.RelPath, got, s.digest)
		}
		if _, err := os.Stat(dst + PartSuffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: part file survived a completed transfer", s.item.RelPath)
		}
	}
}

// ---------------------------------------------------------------- round trip

func TestRoundTripPreservesDigests(t *testing.T) {
	srcDir, dstDir := t.TempDir(), t.TempDir()
	srcs := writeSources(t, srcDir, map[string]int{
		"a.bin":            1 << 20,
		"nested/dir/b.bin": 300*1024 + 17, // not a chunk multiple
		"c.bin":            0,             // empty file, no chunks at all
		"d.bin":            1,             // one-byte file
		"e.bin":            64 << 10,      // exactly one chunk
	})

	l := newLink(t, linkOpts{
		sendCfg: Config{ChunkSize: 64 << 10, Streams: 2},
		recvCfg: Config{DestRoot: dstDir, StateDir: filepath.Join(t.TempDir(), "state")},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := l.sendCap.Send(ctx, l.sendSess, items(srcs)); err != nil {
		t.Fatal(err)
	}
	verifyDelivered(t, dstDir, srcs)
}

// The default stream count is two. D-13 measured QUIC peaking at two streams
// and declining past them; D-33 measured Windows falling 2.2x from one stream
// to four. v1.0's eight workers are actively harmful on QUIC, so this is a
// regression test against someone restoring them.
func TestDefaultStreamCountIsTwo(t *testing.T) {
	if DefaultStreams != 2 {
		t.Fatalf("DefaultStreams = %d, want 2 (D-13, D-33)", DefaultStreams)
	}
	if got := (Config{}).streams(); got != 2 {
		t.Fatalf("zero Config streams = %d, want 2", got)
	}
	if got := (Config{Streams: 64}).streams(); got != MaxStreams {
		t.Fatalf("Streams=64 clamped to %d, want %d", got, MaxStreams)
	}

	srcDir, dstDir := t.TempDir(), t.TempDir()
	srcs := writeSources(t, srcDir, map[string]int{"a.bin": 256 << 10})
	l := newLink(t, linkOpts{
		sendCfg: Config{ChunkSize: 16 << 10},
		recvCfg: Config{DestRoot: dstDir, StateDir: filepath.Join(t.TempDir(), "state")},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := l.sendCap.Send(ctx, l.sendSess, items(srcs)); err != nil {
		t.Fatal(err)
	}

	offer := l.sendSess.awaitSent(t, MsgTransferOffer, time.Second)
	var m openairv1.TransferOffer
	if err := proto.Unmarshal(offer.payload, &m); err != nil {
		t.Fatal(err)
	}
	if m.GetStreamCount() != DefaultStreams {
		t.Errorf("offer declared %d data streams, want %d", m.GetStreamCount(), DefaultStreams)
	}
	verifyDelivered(t, dstDir, srcs)
}

// TestRoundTripLargeFile is the throughput-shaped case: one big file, digests
// compared end to end.
//
// The gigabyte is opt-in. A plain `go test` moves 32 MiB, which is fast enough
// for CI; `-short` drops to 4 MiB; OPENAIR_BIG_TRANSFER=1 runs the full 1 GiB,
// which needs about 2 GiB of scratch disk and is not something to inflict on
// every run of the suite.
func TestRoundTripLargeFile(t *testing.T) {
	size := 32 << 20
	if testing.Short() {
		size = 4 << 20
	}
	if os.Getenv("OPENAIR_BIG_TRANSFER") == "1" {
		size = 1 << 30
	}

	srcDir, dstDir := t.TempDir(), t.TempDir()
	srcs := writeSources(t, srcDir, map[string]int{"big.bin": size + 12345})

	l := newLink(t, linkOpts{
		sendCfg: Config{ChunkSize: DefaultChunkSize},
		recvCfg: Config{DestRoot: dstDir, StateDir: filepath.Join(t.TempDir(), "state")},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := l.sendCap.Send(ctx, l.sendSess, items(srcs)); err != nil {
		t.Fatal(err)
	}
	verifyDelivered(t, dstDir, srcs)
}

// A session.Stream is an io.ReadWriteCloser and may return fewer bytes than
// asked for in either direction. Every read and write in this run is chopped.
func TestRoundTripUnderShortReadsAndWrites(t *testing.T) {
	srcDir, dstDir := t.TempDir(), t.TempDir()
	srcs := writeSources(t, srcDir, map[string]int{
		"a.bin": 200*1024 + 3,
		"b.bin": 40 * 1024,
	})

	l := newLink(t, linkOpts{
		sendCfg:    Config{ChunkSize: 8 << 10, Streams: 2},
		recvCfg:    Config{DestRoot: dstDir, StateDir: filepath.Join(t.TempDir(), "state")},
		chunkRead:  333, // never a whole header, never a whole chunk
		chunkWrite: 101,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := l.sendCap.Send(ctx, l.sendSess, items(srcs)); err != nil {
		t.Fatal(err)
	}
	verifyDelivered(t, dstDir, srcs)
}

// ---------------------------------------------------------------- resume

func TestResumeLandsTheRightBytesAtTheRightOffsets(t *testing.T) {
	srcDir, dstDir := t.TempDir(), t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	srcs := writeSources(t, srcDir, map[string]int{
		"a.bin": 256 << 10,
		"b.bin": 96*1024 + 5,
	})
	id := NewTransferID()

	// First attempt: one data stream that breaks partway through.
	first := newLink(t, linkOpts{
		sendCfg: Config{ChunkSize: 8 << 10, Streams: 1},
		recvCfg: Config{DestRoot: dstDir, StateDir: stateDir},
		limit:   64 << 10,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := first.sendCap.SendWithID(ctx, first.sendSess, id, items(srcs)); err == nil {
		t.Fatal("interrupted transfer reported success")
	}
	first.recvSess.awaitSent(t, MsgTransferComplete, 10*time.Second)

	// Nothing is presented as complete, and the partial data is kept, because
	// resume is what makes retrying a flaky link cheap (§8.5).
	for _, s := range srcs {
		dst := filepath.Join(dstDir, s.item.RelPath)
		if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists after an interrupted transfer", s.item.RelPath)
		}
		if _, err := os.Stat(dst + PartSuffix); err != nil {
			t.Fatalf("%s: partial data was discarded: %v", s.item.RelPath, err)
		}
	}
	first.cancel()

	// Second attempt: same transfer id, fresh capabilities on both ends, which
	// is what a reconnect looks like.
	second := newLink(t, linkOpts{
		sendCfg: Config{ChunkSize: 8 << 10, Streams: 1},
		recvCfg: Config{DestRoot: dstDir, StateDir: stateDir},
	})
	if err := second.sendCap.SendWithID(ctx, second.sendSess, id, items(srcs)); err != nil {
		t.Fatal(err)
	}

	accept := second.recvSess.awaitSent(t, MsgTransferAccept, time.Second)
	var acc openairv1.TransferAccept
	if err := proto.Unmarshal(accept.payload, &acc); err != nil {
		t.Fatal(err)
	}
	if len(acc.GetHaveChunks()) == 0 {
		t.Fatal("resume offered no have_chunks; the first attempt's work was thrown away")
	}
	t.Logf("resumed with %d chunks already verified", len(acc.GetHaveChunks()))

	verifyDelivered(t, dstDir, srcs)

	// A completed transfer drops its resume state.
	if entries, err := os.ReadDir(stateDir); err == nil && len(entries) != 0 {
		t.Errorf("resume state survived completion: %v", entries)
	}
}

// A sender that reconnects on the same session re-offers the same id while the
// old transfer is still live. The replacement must own the part files, and the
// superseded state must not report a completion of its own.
func TestReofferSupersedesTheLiveTransfer(t *testing.T) {
	dstRoot := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	const chunkSize = 4096
	h := newRecvHarness(t, Config{DestRoot: dstRoot, StateDir: stateDir}, 8*chunkSize, chunkSize)

	st := h.openStream(t)
	for i := uint64(0); i < 3; i++ {
		h.sendChunk(t, st, i)
	}
	h.awaitVerified(t, 3)
	before := h.sess.sentCount(MsgTransferComplete)

	// Re-offer the same id.
	ctx := context.Background()
	offer := &openairv1.TransferOffer{
		TransferId: h.id, Files: h.metas, TotalBytes: uint64(8 * chunkSize),
		StreamCount: 1, ChunkSize: chunkSize,
	}
	if err := h.cap.Serve(ctx, h.sess, MsgTransferOffer, mustMarshal(t, offer)); err != nil {
		t.Fatal(err)
	}
	if got := h.sess.sentCount(MsgTransferComplete); got != before {
		t.Errorf("superseded transfer sent %d completions, want %d", got-before, 0)
	}

	// The replacement picks up the three chunks the first attempt verified.
	accept := h.sess.awaitSentNth(t, MsgTransferAccept, 2, time.Second)
	var acc openairv1.TransferAccept
	if err := proto.Unmarshal(accept.payload, &acc); err != nil {
		t.Fatal(err)
	}
	if len(acc.GetHaveChunks()) != 3 {
		t.Fatalf("re-offer have_chunks = %v, want 3 entries", acc.GetHaveChunks())
	}
	h.sendManifest(t)

	// Finish it on a fresh stream.
	st.Reset(0)
	st2 := h.openStream(t)
	for i := uint64(3); i < h.plan.Count(); i++ {
		h.sendChunk(t, st2, i)
	}
	st2.Close()

	h.sess.awaitSent(t, MsgTransferComplete, 5*time.Second)
	final := h.dest(dstRoot)
	if got := digestFile(t, final); got != sha256.Sum256(h.src) {
		t.Fatalf("resumed file digest %x, want %x", got, sha256.Sum256(h.src))
	}
}

// ---------------------------------------------------------------- receiver harness

// recvHarness drives a receiver directly, which makes cancellation timing
// deterministic in a way that racing a live sender is not.
type recvHarness struct {
	cap   *Capability
	sess  *fakeSession
	id    string
	plan  *Plan
	src   []byte
	metas []*openairv1.FileMeta
}

func newRecvHarness(t *testing.T, cfg Config, size int, chunkSize uint64) *recvHarness {
	t.Helper()
	c := New(cfg)
	sess := &fakeSession{
		t:     t,
		peer:  identity.Peer{DeviceID: "peer0000000000aa", Level: identity.LevelOwned},
		local: c,
		inbox: make(chan sentMsg, 16),
	}
	src := make([]byte, size)
	rand.New(rand.NewSource(7)).Read(src)

	plan, err := NewPlan([]uint64{uint64(size)}, chunkSize)
	if err != nil {
		t.Fatal(err)
	}
	h := &recvHarness{
		cap: c, sess: sess, id: NewTransferID(), plan: plan, src: src,
		metas: []*openairv1.FileMeta{{Path: "victim.bin", Size: uint64(size)}},
	}

	ctx := context.Background()
	offer := &openairv1.TransferOffer{
		TransferId: h.id, Files: h.metas, TotalBytes: uint64(size),
		StreamCount: 1, ChunkSize: chunkSize,
	}
	if err := c.Serve(ctx, sess, MsgTransferOffer, mustMarshal(t, offer)); err != nil {
		t.Fatal(err)
	}

	h.sendManifest(t)
	return h
}

// sendManifest delivers the per-chunk digests. A re-offer needs its own: the
// manifest belongs to the transfer state the offer created, so a sender that
// re-offers must re-send it, which SendWithID does.
func (h *recvHarness) sendManifest(t *testing.T) {
	t.Helper()
	digests := make([][]byte, h.plan.Count())
	for i := uint64(0); i < h.plan.Count(); i++ {
		ch, _ := h.plan.Chunk(i)
		sum := sha256.Sum256(h.src[ch.Offset : ch.Offset+uint64(ch.Size)])
		digests[i] = sum[:]
	}
	if err := h.cap.Serve(context.Background(), h.sess, MsgChunkManifest, mustMarshal(t,
		&openairv1.ChunkManifest{
			TransferId: h.id, ChunkSize: h.plan.ChunkSize(), ChunkSha256: digests,
		})); err != nil {
		t.Fatal(err)
	}
}

// openStream starts a data stream into the receiver and returns the writing end.
func (h *recvHarness) openStream(t *testing.T) *fakeStream {
	t.Helper()
	near, far := newStreamPair()
	init := mustMarshal(t, &openairv1.StreamInit{TransferId: h.id})
	go func() {
		_ = h.cap.ServeStream(context.Background(), h.sess, far, MsgStreamInit, init)
	}()
	return near
}

func (h *recvHarness) sendChunk(t *testing.T, st *fakeStream, index uint64) {
	t.Helper()
	ch, ok := h.plan.Chunk(index)
	if !ok {
		t.Fatalf("no chunk %d", index)
	}
	buf := make([]byte, ChunkHeaderSize+int(ch.Size))
	putChunkHeader(buf, ch.Offset, ch.Size)
	copy(buf[ChunkHeaderSize:], h.src[ch.Offset:ch.Offset+uint64(ch.Size)])
	if err := writeFull(st, buf); err != nil {
		t.Fatal(err)
	}
}

func (h *recvHarness) awaitVerified(t *testing.T, n uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if rs := h.cap.lookupRecv(h.id); rs != nil && rs.set.marked() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("only %d chunks verified, want %d", h.verified(), n)
}

func (h *recvHarness) verified() uint64 {
	if rs := h.cap.lookupRecv(h.id); rs != nil {
		return rs.set.marked()
	}
	return 0
}

func (h *recvHarness) dest(root string) string {
	return filepath.Join(root, "victim.bin")
}

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ---------------------------------------------------------------- cancellation

func TestCancelMidTransferNeverPresentsAPartialFile(t *testing.T) {
	dstRoot := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	const chunkSize = 4096
	h := newRecvHarness(t, Config{DestRoot: dstRoot, StateDir: stateDir}, 16*chunkSize, chunkSize)

	st := h.openStream(t)
	for i := uint64(0); i < 4; i++ {
		h.sendChunk(t, st, i)
	}
	h.awaitVerified(t, 4)

	if err := h.cap.Serve(context.Background(), h.sess, MsgTransferCancel, mustMarshal(t,
		&openairv1.TransferCancel{TransferId: h.id, Reason: "user aborted"})); err != nil {
		t.Fatal(err)
	}

	final := h.dest(dstRoot)
	if _, err := os.Stat(final); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s exists after cancellation: a partial file was presented as complete", final)
	}
	if _, err := os.Stat(final + PartSuffix); err != nil {
		t.Fatalf("partial data was discarded on a cancel that did not ask for it: %v", err)
	}

	// The peer is told the transfer did not complete.
	m := h.sess.awaitSent(t, MsgTransferComplete, 5*time.Second)
	var done openairv1.TransferComplete
	if err := proto.Unmarshal(m.payload, &done); err != nil {
		t.Fatal(err)
	}
	if done.GetOk() {
		t.Error("TransferComplete reported ok after a cancellation")
	}

	// The verified-chunk set survives, because a cancel is usually a prelude to
	// retrying and resume is what makes that cheap (§8.5).
	entries, err := os.ReadDir(stateDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("resume state missing after cancel: %v %v", entries, err)
	}
	st.Close()
}

func TestCancelWithDiscardPartialRemovesEverything(t *testing.T) {
	dstRoot := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "state")
	const chunkSize = 4096
	h := newRecvHarness(t, Config{DestRoot: dstRoot, StateDir: stateDir}, 8*chunkSize, chunkSize)

	st := h.openStream(t)
	for i := uint64(0); i < 3; i++ {
		h.sendChunk(t, st, i)
	}
	h.awaitVerified(t, 3)

	if err := h.cap.Serve(context.Background(), h.sess, MsgTransferCancel, mustMarshal(t,
		&openairv1.TransferCancel{TransferId: h.id, DiscardPartial: true})); err != nil {
		t.Fatal(err)
	}

	final := h.dest(dstRoot)
	for _, p := range []string{final, final + PartSuffix} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived a discard_partial cancel", p)
		}
	}
	if entries, err := os.ReadDir(stateDir); err == nil && len(entries) != 0 {
		t.Errorf("resume state survived a discard_partial cancel: %v", entries)
	}
	st.Close()
}

// ---------------------------------------------------------------- integrity

// §8.4: the receiver MUST verify each chunk against the manifest and MUST
// report mismatches rather than silently accepting corrupt data.
func TestCorruptChunkIsRejectedAndReported(t *testing.T) {
	dstRoot := t.TempDir()
	const chunkSize = 4096
	h := newRecvHarness(t, Config{DestRoot: dstRoot, StateDir: filepath.Join(t.TempDir(), "s")},
		2*chunkSize, chunkSize)

	st := h.openStream(t)
	h.sendChunk(t, st, 0)
	h.awaitVerified(t, 1)

	// Chunk 1 with the right header and the wrong bytes.
	ch, _ := h.plan.Chunk(1)
	buf := make([]byte, ChunkHeaderSize+int(ch.Size))
	putChunkHeader(buf, ch.Offset, ch.Size)
	for i := range buf[ChunkHeaderSize:] {
		buf[ChunkHeaderSize+i] = 0xAA
	}
	if err := writeFull(st, buf); err != nil {
		t.Fatal(err)
	}
	st.Close()

	m := h.sess.awaitSent(t, MsgTransferComplete, 5*time.Second)
	var done openairv1.TransferComplete
	if err := proto.Unmarshal(m.payload, &done); err != nil {
		t.Fatal(err)
	}
	if done.GetOk() {
		t.Error("TransferComplete reported ok despite a corrupt chunk")
	}
	if len(done.GetFailedChunks()) != 1 || done.GetFailedChunks()[0] != 1 {
		t.Errorf("failed_chunks = %v, want [1]", done.GetFailedChunks())
	}
	if _, err := os.Stat(h.dest(dstRoot)); !errors.Is(err, os.ErrNotExist) {
		t.Error("a transfer with a corrupt chunk was committed")
	}
}

// A frame whose offset is not a chunk boundary would let a peer write
// arbitrary bytes at an arbitrary place in the destination.
func TestUnalignedFrameIsRefused(t *testing.T) {
	dstRoot := t.TempDir()
	const chunkSize = 4096
	h := newRecvHarness(t, Config{DestRoot: dstRoot, StateDir: filepath.Join(t.TempDir(), "s")},
		4*chunkSize, chunkSize)

	st := h.openStream(t)
	body := make([]byte, 16)
	buf := make([]byte, ChunkHeaderSize+len(body))
	putChunkHeader(buf, 37, uint32(len(body)))
	copy(buf[ChunkHeaderSize:], body)
	if err := writeFull(st, buf); err != nil {
		t.Fatal(err)
	}
	st.Close()

	h.sess.awaitSent(t, MsgTransferComplete, 5*time.Second)
	if h.verified() != 0 {
		t.Error("an unaligned frame was accepted")
	}
}

// ---------------------------------------------------------------- offers

func TestOfferWithTraversalPathIsRefused(t *testing.T) {
	dstRoot := t.TempDir()
	c := New(Config{DestRoot: dstRoot, StateDir: filepath.Join(t.TempDir(), "s")})
	sess := &fakeSession{
		t:     t,
		peer:  identity.Peer{DeviceID: "peer0000000000aa", Level: identity.LevelOwned},
		local: c,
		inbox: make(chan sentMsg, 4),
	}

	offer := &openairv1.TransferOffer{
		TransferId:  NewTransferID(),
		Files:       []*openairv1.FileMeta{{Path: "../../escape.bin", Size: 16}},
		TotalBytes:  16,
		StreamCount: 1,
		ChunkSize:   4096,
	}
	err := c.Serve(context.Background(), sess, MsgTransferOffer, mustMarshal(t, offer))
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v, want ErrUnsafePath", err)
	}

	m := sess.awaitSent(t, MsgTransferAccept, time.Second)
	var acc openairv1.TransferAccept
	if err := proto.Unmarshal(m.payload, &acc); err != nil {
		t.Fatal(err)
	}
	if acc.GetAccepted() {
		t.Error("a traversal offer was accepted")
	}

	// Nothing was created anywhere, including the part file.
	entries, err := os.ReadDir(filepath.Dir(dstRoot))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "escape.bin" || e.Name() == "escape.bin"+PartSuffix {
			t.Fatalf("traversal offer created %s outside the destination root", e.Name())
		}
	}
}

// Trusted peers require explicit consent; only Owned auto-accepts (§8.1, PRD R11).
func TestUnownedPeerIsNotAutoAccepted(t *testing.T) {
	c := New(Config{DestRoot: t.TempDir()})
	sess := &fakeSession{
		t:     t,
		peer:  identity.Peer{DeviceID: "peer0000000000aa", Level: identity.LevelTrusted},
		local: c,
		inbox: make(chan sentMsg, 4),
	}
	offer := &openairv1.TransferOffer{
		TransferId: NewTransferID(),
		Files:      []*openairv1.FileMeta{{Path: "x.bin", Size: 4}},
		TotalBytes: 4, StreamCount: 1, ChunkSize: 4096,
	}
	if err := c.Serve(context.Background(), sess, MsgTransferOffer, mustMarshal(t, offer)); err != nil {
		t.Fatal(err)
	}
	m := sess.awaitSent(t, MsgTransferAccept, time.Second)
	var acc openairv1.TransferAccept
	if err := proto.Unmarshal(m.payload, &acc); err != nil {
		t.Fatal(err)
	}
	if acc.GetAccepted() {
		t.Error("a Trusted peer's offer was auto-accepted")
	}
}

func TestRejectedOfferStopsTheSender(t *testing.T) {
	srcDir := t.TempDir()
	srcs := writeSources(t, srcDir, map[string]int{"a.bin": 4096})
	l := newLink(t, linkOpts{
		sendCfg: Config{ChunkSize: 4096},
		recvCfg: Config{
			DestRoot: t.TempDir(),
			Accept: func(context.Context, identity.Peer, Offer) (bool, error) {
				return false, nil
			},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := l.sendCap.Send(ctx, l.sendSess, items(srcs)); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
	if n := l.sendSess.sentCount(MsgChunkManifest); n != 0 {
		t.Errorf("sender sent %d manifests after a rejection", n)
	}
}

// A receiver that refuses the peer closes the connection instead of answering
// the offer (§10, conn.listener), so the sender's wait for TransferAccept must
// end when the session does. Waiting only on the reply hangs until the caller's
// context expires, and a mobile SendFiles has no deadline of its own.
func TestSessionClosedWhileWaitingForAcceptStopsTheSender(t *testing.T) {
	srcDir := t.TempDir()
	srcs := writeSources(t, srcDir, map[string]int{"a.bin": 4096})

	// No remote: the offer is sent and nothing ever replies to it.
	c := New(Config{ChunkSize: 4096})
	sess := &fakeSession{
		t:     t,
		peer:  identity.Peer{DeviceID: "peer0000000000aa", Level: identity.LevelOwned},
		local: c,
		inbox: make(chan sentMsg, 1),
		done:  make(chan struct{}),
	}

	errc := make(chan error, 1)
	go func() {
		_, err := c.Send(context.Background(), sess, items(srcs))
		errc <- err
	}()

	sess.awaitSent(t, MsgTransferOffer, time.Second)
	sess.endSession()

	select {
	case err := <-errc:
		if !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("err = %v, want ErrSessionClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after the session closed")
	}
}

// §3.1: an unrecognised msgType is ignored and never fatal, which is what makes
// mixed-version fleets viable.
func TestUnknownMessageTypeIsIgnored(t *testing.T) {
	c := New(Config{DestRoot: t.TempDir()})
	sess := &fakeSession{t: t, local: c, inbox: make(chan sentMsg, 1)}
	err := c.Serve(context.Background(), sess, 4242, []byte{1, 2, 3})
	if !errors.Is(err, session.ErrUnknownMsgType) {
		t.Errorf("unknown msgType returned %v, want session.ErrUnknownMsgType", err)
	}
	// The sentinel is what the session dispatcher treats as "ignore and
	// continue"; anything else would be logged as a capability failure.
	if err := c.ServeStream(context.Background(), sess, nil, 4242, nil); !errors.Is(err, session.ErrUnknownMsgType) {
		t.Errorf("unknown stream msgType returned %v, want session.ErrUnknownMsgType", err)
	}
}

// The capability byte is 1 even though the generated enum is 2 (D-34).
func TestCapIDIsTheWireByteNotTheEnum(t *testing.T) {
	if CapID != 1 {
		t.Errorf("CapID = %d, want 1", CapID)
	}
	if got := capIDWire(openairv1.CapabilityId_CAPABILITY_ID_FILES); got != CapID {
		t.Errorf("capIDWire(CAPABILITY_ID_FILES) = %d, want %d", got, CapID)
	}
	if openairv1.CapabilityId_CAPABILITY_ID_FILES != 2 {
		t.Errorf("generated enum changed: %d", openairv1.CapabilityId_CAPABILITY_ID_FILES)
	}
}

// The opening message on a data stream is a §3 envelope; everything after it is
// raw. This pins that the stream framing this package writes is the session
// layer's, not a second implementation of it.
func TestDataStreamOpensWithAnEnvelope(t *testing.T) {
	near, far := newStreamPair()
	payload := mustMarshal(t, &openairv1.StreamInit{TransferId: "abc"})
	go func() {
		_ = encodeEnvelope(near, session.Envelope{
			Version: 1, CapID: CapID, MsgType: MsgStreamInit, Payload: payload,
		})
		_ = writeFull(near, []byte{1, 2, 3, 4})
		near.Close()
	}()

	env, err := decodeEnvelope(far)
	if err != nil {
		t.Fatal(err)
	}
	if env.CapID != CapID || env.MsgType != MsgStreamInit {
		t.Fatalf("envelope = %+v", env)
	}
	var si openairv1.StreamInit
	if err := proto.Unmarshal(env.Payload, &si); err != nil {
		t.Fatal(err)
	}
	if si.GetTransferId() != "abc" {
		t.Fatalf("transfer id = %q", si.GetTransferId())
	}
	rest, err := io.ReadAll(far)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("raw bytes after the envelope = % x", rest)
	}
}

// A guard on the one number this task exists to keep small.
func TestChunkHeaderStaysTwelveBytes(t *testing.T) {
	if ChunkHeaderSize != 12 {
		t.Fatalf("ChunkHeaderSize = %d, want 12 (§8.3)", ChunkHeaderSize)
	}
	var buf [ChunkHeaderSize]byte
	putChunkHeader(buf[:], 1, 2)
	if binary.LittleEndian.Uint64(buf[:8]) != 1 || binary.LittleEndian.Uint32(buf[8:]) != 2 {
		t.Fatal("header layout changed")
	}
}
