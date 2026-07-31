package remotefs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/session"
)

// Streaming reads, M11 (§11.2, §11.4). This is the "smart client" half of §11's
// bargain: the source still answers one range per stream and knows nothing about
// what it is serving, and everything that makes a 40 GB file feel like a local
// one happens here.
//
// # What a player actually does
//
// A media player opens a file, reads a little from the front, seeks to the
// index at the end, seeks back, and then reads forward at roughly the bitrate.
// Two things make that painful over a network: every read costs a round trip,
// and a seek arrives while the previous position's read-ahead is still in
// flight. So this Reader does two things and no more:
//
//   - Reads ahead by whole blocks, several at once, so sequential playback is
//     never waiting on a round trip it could have taken earlier.
//   - Cancels the read-ahead on a seek. That is the part that matters: without
//     it, a seek on a relayed path queues behind however many megabytes were
//     already requested for the old position, and the time to first byte is the
//     time to drain them. §11.2's one-request-per-stream shape is what makes
//     cancelling cheap — a stream reset drops exactly one range.
//
// # Window size
//
// §11.4 says the window should be roughly RTT × observed bitrate. That product
// is the bandwidth-delay product: the amount that has to be in flight to keep
// the path busy at all. Read-ahead wants a multiple of it, because it also has
// to cover the jitter between a player's reads, so the window is prefetchFactor
// times the BDP, floored and capped in blocks. The floor matters on a LAN,
// where the BDP rounds to nothing and a player still wants a few megabytes of
// slack; the cap matters on a relayed path, where the BDP can be large and
// prefetching 200 MB of a file someone is about to seek away from is waste.
//
// The bitrate is measured here rather than taken from PathInfo when we have
// measured one: PathInfo's bandwidth estimate is the transport's view of the
// whole connection, and what this needs is the rate range reads are actually
// arriving at.

const (
	// blockSize is the read-ahead quantum. It is MaxReadLength because that is
	// the largest range the source will answer in one response, so a block is
	// exactly one wire read and never two.
	blockSize = MaxReadLength

	// minWindowBlocks is the read-ahead floor. Four blocks is 4 MiB, which is
	// several seconds of most video and small enough to throw away on a seek.
	minWindowBlocks = 4

	// maxWindowBlocks caps the window. Sixteen blocks is 16 MiB in flight and
	// at most that much wasted by a seek.
	maxWindowBlocks = 16

	// prefetchFactor multiplies the bandwidth-delay product to get the window.
	// The BDP alone keeps the path busy; a player needs more than that, because
	// it reads in bursts.
	prefetchFactor = 4

	// defaultParallel is how many range reads may be in flight at once. Each is
	// its own stream (§11.2), so this is concurrency without head-of-line
	// blocking; four is enough to cover the round trip on a relayed path without
	// making a seek expensive to cancel.
	defaultParallel = 4

	// rateAlpha is the EWMA weight for the observed bitrate. Weighted towards
	// history, because one slow block on a shared network is not a new bitrate.
	rateAlpha = 0.25
)

// OpenOptions tune a Reader. The zero value is the right thing for a media
// player.
type OpenOptions struct {
	// Size is the file's size when the caller already knows it. Zero means Open
	// stats the path first.
	Size uint64

	// ModifiedAt is used with Size to key the cache; zero means Open stats.
	ModifiedAt int64

	// Cache is an optional block cache. Nil means every read goes to the wire.
	Cache *Cache

	// Parallel overrides how many range reads may be in flight. Zero means
	// defaultParallel.
	Parallel int

	// WindowBytes fixes the read-ahead window instead of adapting it to the
	// path. Zero means adaptive, which is what §11.4 asks for.
	WindowBytes int
}

// Reader is a seekable reader over one remote file.
//
// It satisfies io.ReadSeekCloser and io.ReaderAt, which is exactly what
// http.ServeContent wants — that is how a Range request from a media player
// becomes a range read on the wire with nothing in between.
type Reader struct {
	c    *Capability
	sess session.Session
	path string
	size uint64
	key  string

	parallel    int
	fixedWindow int
	cache       *Cache

	// slots bounds in-flight range reads. A fetch holds one for as long as its
	// stream is open.
	slots chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	pos    uint64
	blocks map[uint64]*block
	rate   float64 // observed bytes per second, EWMA; 0 until the first block
	closed bool
}

// block is one blockSize-aligned range, in flight or resident.
type block struct {
	idx   uint64
	ready chan struct{}

	cancel context.CancelFunc

	// waiting counts readers blocked on ready. A block with a waiter is never
	// cancelled by a seek: the seek is racing a read that already committed to
	// it, and cancelling would fail that read for no gain.
	waiting int

	data []byte
	err  error
}

var _ io.ReadSeekCloser = (*Reader)(nil)
var _ io.ReaderAt = (*Reader)(nil)

// Open prepares a streaming read of one remote path.
//
// It stats the path unless the caller passed a size, because a seek relative to
// the end and the cache key both need one.
func (c *Capability) Open(ctx context.Context, sess session.Session, path string, opts OpenOptions) (*Reader, error) {
	size, modified := opts.Size, opts.ModifiedAt
	if size == 0 || modified == 0 {
		st, err := c.Stat(ctx, sess, path)
		if err != nil {
			return nil, err
		}
		if st.IsDir {
			return nil, fmt.Errorf("remotefs: %s is a directory", path)
		}
		size, modified = st.Size, st.ModifiedAt
	}

	parallel := opts.Parallel
	if parallel <= 0 {
		parallel = defaultParallel
	}

	// The Reader's own context outlives the call that opened it: read-ahead
	// keeps running between Reads, and Close is what stops it.
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r := &Reader{
		c:           c,
		sess:        sess,
		path:        path,
		size:        size,
		key:         cacheKey(sess.Peer().DeviceID, path, size, modified),
		parallel:    parallel,
		fixedWindow: opts.WindowBytes,
		cache:       opts.Cache,
		slots:       make(chan struct{}, parallel),
		ctx:         rctx,
		cancel:      cancel,
		blocks:      make(map[uint64]*block),
	}
	return r, nil
}

// Size is the file's size as of Open.
func (r *Reader) Size() uint64 { return r.size }

// Read fills p from the current position.
func (r *Reader) Read(p []byte) (int, error) {
	r.mu.Lock()
	pos := r.pos
	r.mu.Unlock()

	n, err := r.readAt(p, pos)
	if n > 0 {
		r.mu.Lock()
		r.pos = pos + uint64(n)
		r.mu.Unlock()
	}
	return n, err
}

// ReadAt reads a range without moving the position, and without disturbing the
// read-ahead window: a caller doing both is usually a player reading an index
// while playback continues.
func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("remotefs: negative offset")
	}
	// io.ReaderAt owes the caller a full buffer or an error, unlike Read.
	total := 0
	for total < len(p) {
		n, err := r.readAt(p[total:], uint64(off)+uint64(total))
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}

// Seek moves the position. It is cheap: nothing is fetched until the next read,
// and read-ahead for the old position is abandoned then.
func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = int64(r.pos) + offset
	case io.SeekEnd:
		next = int64(r.size) + offset
	default:
		return 0, fmt.Errorf("remotefs: unknown whence %d", whence)
	}
	if next < 0 {
		return 0, errors.New("remotefs: seek before the start of the file")
	}
	r.pos = uint64(next)
	return next, nil
}

// Close abandons every read in flight and releases the reader's blocks.
func (r *Reader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.blocks = map[uint64]*block{}
	r.mu.Unlock()

	r.cancel()
	return nil
}

// readAt serves one read from the block window, starting whatever fetches it
// needs and whatever read-ahead the path deserves.
func (r *Reader) readAt(p []byte, off uint64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.size {
		return 0, io.EOF
	}

	idx := off / blockSize
	b, err := r.demand(idx)
	if err != nil {
		return 0, err
	}

	select {
	case <-b.ready:
	case <-r.ctx.Done():
		r.release(b)
		return 0, io.ErrClosedPipe
	}
	r.release(b)

	if b.err != nil {
		return 0, b.err
	}

	start := off - idx*blockSize
	if start >= uint64(len(b.data)) {
		// Short of what the block should hold: the file ended earlier than the
		// size we were told, which a source is allowed to do between the stat
		// and the read.
		return 0, io.EOF
	}
	n := copy(p, b.data[start:])
	return n, nil
}

// demand returns the block for idx, starting it if necessary, and brings the
// read-ahead window in line with a reader now sitting at idx.
func (r *Reader) demand(idx uint64) (*block, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errors.New("remotefs: read on a closed reader")
	}

	b := r.startLocked(idx)
	b.waiting++

	// Everything outside the window is either finished work worth dropping or
	// read-ahead for a position this reader has left. Cancelling it is what
	// makes a seek cost one round trip instead of a window's worth.
	window := r.windowBlocksLocked()
	last := idx + uint64(window) - 1
	for i, other := range r.blocks {
		if i >= idx && i <= last {
			continue
		}
		delete(r.blocks, i)
		if other.waiting == 0 && other.cancel != nil {
			other.cancel()
		}
	}
	for i := idx + 1; i <= last && i*blockSize < r.size; i++ {
		r.startLocked(i)
	}
	return b, nil
}

// startLocked returns the block for idx, starting a fetch if it is not already
// resident or in flight. r.mu must be held.
func (r *Reader) startLocked(idx uint64) *block {
	if b, ok := r.blocks[idx]; ok {
		return b
	}
	ctx, cancel := context.WithCancel(r.ctx)
	b := &block{idx: idx, ready: make(chan struct{}), cancel: cancel}
	r.blocks[idx] = b
	go r.fetch(ctx, b)
	return b
}

// release drops a reader's claim on a block.
func (r *Reader) release(b *block) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b.waiting > 0 {
		b.waiting--
	}
}

// fetch fills one block, from the cache if it is there and from the wire if it
// is not.
func (r *Reader) fetch(ctx context.Context, b *block) {
	defer close(b.ready)

	if r.cache != nil {
		if data, ok := r.cache.Get(r.key, b.idx); ok {
			b.data = data
			return
		}
	}

	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		b.err = ctx.Err()
		return
	}

	offset := b.idx * blockSize
	want := uint64(blockSize)
	if offset+want > r.size {
		want = r.size - offset
	}
	buf := make([]byte, want)

	started := time.Now()
	n, err := r.readRange(ctx, offset, buf)
	if err != nil {
		b.err = err
		return
	}
	b.data = buf[:n]
	r.observe(n, time.Since(started))

	if r.cache != nil && n > 0 {
		r.cache.Put(r.key, b.idx, b.data)
	}
}

// readRange issues range reads until the block is full or the file ends. §11.2
// allows a shorter answer than asked for, so one block can be several reads --
// which is exactly the short-read case the protocol requires clients to handle.
func (r *Reader) readRange(ctx context.Context, offset uint64, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, eof, err := r.c.ReadAt(ctx, r.sess, r.path, offset+uint64(total), buf[total:])
		if err != nil {
			return total, err
		}
		total += n
		if eof {
			return total, nil
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}

// observe folds one completed block into the bitrate estimate.
func (r *Reader) observe(n int, took time.Duration) {
	if n <= 0 || took <= 0 {
		return
	}
	rate := float64(n) / took.Seconds()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rate == 0 {
		r.rate = rate
		return
	}
	r.rate = rateAlpha*rate + (1-rateAlpha)*r.rate
}

// windowBlocksLocked is the read-ahead window, in blocks. r.mu must be held.
func (r *Reader) windowBlocksLocked() int {
	if r.fixedWindow > 0 {
		return clampBlocks((r.fixedWindow + blockSize - 1) / blockSize)
	}

	info := r.sess.PathInfo()
	rate := r.rate
	if rate == 0 {
		// Nothing measured yet: the transport's estimate is better than
		// pretending the path is infinitely fast.
		rate = float64(info.BandwidthBytes)
	}
	rtt := time.Duration(info.RTTMillis) * time.Millisecond
	bdp := rate * rtt.Seconds()
	return clampBlocks(int(math.Ceil(bdp * prefetchFactor / blockSize)))
}

// WindowBytes is the read-ahead window currently in use. It exists so a caller
// can log what the path talked it into.
func (r *Reader) WindowBytes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.windowBlocksLocked() * blockSize
}

func clampBlocks(n int) int {
	if n < minWindowBlocks {
		return minWindowBlocks
	}
	if n > maxWindowBlocks {
		return maxWindowBlocks
	}
	return n
}
