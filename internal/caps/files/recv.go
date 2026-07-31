package files

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// maxReportedFailures bounds TransferComplete.failed_chunks. A transfer that
// failed a million chunks does not need a million indices on the wire to say
// so, and an unbounded list would let a bad transfer produce a message the
// envelope cannot carry.
const maxReportedFailures = 1024

// destFile is one destination: data lands in part and is renamed to final only
// on verified completion.
type destFile struct {
	final string
	part  string
	f     *os.File
}

// recvState is one inbound transfer.
type recvState struct {
	cap  *Capability
	sess session.Session
	id   string
	plan *Plan
	dest []destFile
	set  *chunkSet

	manifest      [][]byte
	manifestOnce  sync.Once
	manifestReady chan struct{}

	expectStreams int
	mu            sync.Mutex
	streamsSeen   int
	streamsActive int
	failed        []uint64

	bytes atomic.Uint64

	ctx        context.Context
	abort      context.CancelFunc
	finishOnce sync.Once
	finished   chan struct{}
	cancelled  atomic.Bool
	superseded atomic.Bool
}

// supersede retires a transfer that a re-offer has replaced: its streams stop,
// its file handles close so the replacement is the only writer, and it reports
// nothing, because the replacement will.
func (rs *recvState) supersede() {
	rs.superseded.Store(true)
	rs.abort()
	_ = rs.set.flush()
	rs.finish()
}

func (c *Capability) stateDir() string {
	if c.cfg.StateDir != "" {
		return c.cfg.StateDir
	}
	return filepath.Join(c.cfg.DestRoot, ".openair-state")
}

// defaultAccept is PRD R11: Owned peers auto-accept, everyone else needs an
// explicit decision that this build does not have a UI for, so refuses.
func defaultAccept(peer identity.Peer) bool { return peer.Level >= identity.LevelOwned }

func (c *Capability) onOffer(ctx context.Context, sess session.Session, m *openairv1.TransferOffer) error {
	id := m.GetTransferId()
	if id == "" {
		return errors.New("files: offer with empty transfer_id")
	}
	if c.cfg.DestRoot == "" {
		return c.refuse(ctx, sess, id, errors.New("files: no destination root configured"))
	}

	chunkSize := m.GetChunkSize()
	if chunkSize == 0 || chunkSize > MaxChunkSize {
		return c.refuse(ctx, sess, id, fmt.Errorf("%w: offered chunk size %d", ErrChunkTooLarge, chunkSize))
	}

	ok := true
	if c.cfg.Accept != nil {
		var err error
		ok, err = c.cfg.Accept(ctx, sess.Peer(), Offer{
			TransferID: id,
			Files:      m.GetFiles(),
			TotalBytes: m.GetTotalBytes(),
			ChunkSize:  chunkSize,
			Streams:    m.GetStreamCount(),
		})
		if err != nil {
			return c.refuse(ctx, sess, id, err)
		}
	} else {
		ok = defaultAccept(sess.Peer())
	}
	if !ok {
		return sess.Send(ctx, CapID, MsgTransferAccept, &openairv1.TransferAccept{
			TransferId: id, Accepted: false,
		})
	}

	// A re-offer of a live id supersedes it; the old streams are abandoned but
	// its verified chunks survive on disk and are reloaded below. Retiring it
	// before the replacement opens the part files keeps a single writer per
	// destination.
	if old := c.lookupRecv(id); old != nil {
		old.supersede()
		c.mu.Lock()
		if c.in[id] == old {
			delete(c.in, id)
		}
		c.mu.Unlock()
	}

	rs, err := c.newRecvState(sess, m, chunkSize)
	if err != nil {
		return c.refuse(ctx, sess, id, err)
	}

	c.mu.Lock()
	c.in[id] = rs
	c.mu.Unlock()

	if err := sess.Send(ctx, CapID, MsgTransferAccept, &openairv1.TransferAccept{
		TransferId: id,
		Accepted:   true,
		HaveChunks: rs.set.list(),
	}); err != nil {
		rs.close()
		return err
	}

	// A resume that has nothing left to fetch is already done.
	if rs.set.complete() {
		go rs.finish()
		return nil
	}
	go rs.reportProgress()
	return nil
}

func (c *Capability) refuse(ctx context.Context, sess session.Session, id string, cause error) error {
	_ = sess.Send(ctx, CapID, MsgTransferAccept, &openairv1.TransferAccept{
		TransferId: id, Accepted: false,
	})
	return cause
}

func (c *Capability) newRecvState(sess session.Session, m *openairv1.TransferOffer, chunkSize uint64) (*recvState, error) {
	metas := m.GetFiles()
	if len(metas) == 0 {
		return nil, errors.New("files: offer contains no files")
	}
	sizes := make([]uint64, len(metas))
	for i, fm := range metas {
		sizes[i] = fm.GetSize()
	}
	plan, err := NewPlan(sizes, chunkSize)
	if err != nil {
		return nil, err
	}
	if total := m.GetTotalBytes(); total != 0 && total != plan.TotalBytes() {
		return nil, fmt.Errorf("files: offer says %d bytes, file sizes sum to %d", total, plan.TotalBytes())
	}
	streams := int(m.GetStreamCount())
	if streams <= 0 {
		streams = 1
	}
	if streams > MaxStreams {
		return nil, fmt.Errorf("files: offer wants %d data streams, max is %d", streams, MaxStreams)
	}

	rs := &recvState{
		cap:           c,
		sess:          sess,
		id:            m.GetTransferId(),
		plan:          plan,
		expectStreams: streams,
		manifestReady: make(chan struct{}),
		finished:      make(chan struct{}),
	}
	rs.ctx, rs.abort = context.WithCancel(context.Background())

	// Every path is validated and resolved before a single byte is accepted
	// (§8.1). Rejecting here means a hostile offer never creates a file.
	for _, fm := range metas {
		full, err := resolve(c.cfg.DestRoot, fm.GetPath())
		if err != nil {
			rs.close()
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			rs.close()
			return nil, err
		}
		part := full + PartSuffix
		f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			rs.close()
			return nil, err
		}
		if err := f.Truncate(int64(fm.GetSize())); err != nil {
			f.Close()
			rs.close()
			return nil, err
		}
		rs.dest = append(rs.dest, destFile{final: full, part: part, f: f})
	}

	rs.set = newChunkSet(c.stateDir(), rs.id, plan.Count(), chunkSize, offerHash(metas, chunkSize))
	rs.set.load()
	rs.bytes.Store(rs.verifiedBytes())
	return rs, nil
}

func (rs *recvState) verifiedBytes() uint64 {
	var n uint64
	for _, i := range rs.set.list() {
		if c, ok := rs.plan.Chunk(i); ok {
			n += uint64(c.Size)
		}
	}
	return n
}

func (c *Capability) onManifest(m *openairv1.ChunkManifest) error {
	rs := c.lookupRecv(m.GetTransferId())
	if rs == nil {
		return fmt.Errorf("%w: %q", ErrUnknownTransfer, m.GetTransferId())
	}
	if m.GetChunkSize() != rs.plan.ChunkSize() {
		return fmt.Errorf("files: manifest chunk size %d, offer said %d",
			m.GetChunkSize(), rs.plan.ChunkSize())
	}
	if uint64(len(m.GetChunkSha256())) != rs.plan.Count() {
		return fmt.Errorf("files: manifest has %d digests, plan has %d chunks",
			len(m.GetChunkSha256()), rs.plan.Count())
	}
	rs.manifestOnce.Do(func() {
		rs.manifest = m.GetChunkSha256()
		close(rs.manifestReady)
	})
	return nil
}

// serveStream reads raw chunk frames until the peer closes the stream (§8.2,
// §8.3). Nothing here parses protobuf: the opening envelope was the last of it.
func (rs *recvState) serveStream(ctx context.Context, st session.Stream) error {
	rs.mu.Lock()
	rs.streamsSeen++
	rs.streamsActive++
	rs.mu.Unlock()
	defer rs.streamDone()

	// The manifest is sent on the control stream before or during transfer
	// (§8.4). Verification cannot start without it, so wait rather than accept
	// unverified bytes.
	select {
	case <-rs.manifestReady:
	case <-rs.ctx.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(manifestWait):
		return errors.New("files: no chunk manifest arrived")
	}

	buf := make([]byte, ChunkHeaderSize+rs.plan.ChunkSize())
	for {
		select {
		case <-rs.ctx.Done():
			return nil
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		offset, size, err := readChunkHeader(st, buf)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		if uint64(size) > rs.plan.ChunkSize() {
			return fmt.Errorf("%w: %d bytes", ErrChunkTooLarge, size)
		}
		body := buf[ChunkHeaderSize : ChunkHeaderSize+int(size)]
		// io.ReadFull is what makes short reads a non-event; a stream may
		// return fewer bytes than asked for at any boundary.
		if _, err := io.ReadFull(st, body); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		if err := rs.acceptChunk(offset, size, body); err != nil {
			return err
		}
		if rs.set.complete() {
			rs.finish()
			return nil
		}
	}
}

// manifestWait bounds how long a data stream will sit waiting for the manifest
// before giving up. Generous: it is a liveness backstop, not a timeout anyone
// should hit.
const manifestWait = 60 * time.Second

func (rs *recvState) acceptChunk(offset uint64, size uint32, body []byte) error {
	ch, err := rs.plan.Locate(offset, size)
	if err != nil {
		return err
	}
	if rs.set.has(ch.Index) {
		return nil // duplicate, e.g. a resend racing a resume
	}

	// §8.4: verify against the manifest and report mismatches rather than
	// silently accepting corrupt data.
	sum := sha256.Sum256(body)
	want := rs.manifest[ch.Index]
	if len(want) != sha256.Size || string(sum[:]) != string(want) {
		rs.mu.Lock()
		if len(rs.failed) < maxReportedFailures {
			rs.failed = append(rs.failed, ch.Index)
		}
		rs.mu.Unlock()
		return nil
	}

	if err := writeAtFull(rs.dest[ch.FileIndex].f, body, int64(ch.FileOffset)); err != nil {
		return err
	}
	rs.set.mark(ch.Index)
	rs.bytes.Add(uint64(size))
	return nil
}

func (rs *recvState) streamDone() {
	rs.mu.Lock()
	rs.streamsActive--
	seen, active := rs.streamsSeen, rs.streamsActive
	rs.mu.Unlock()
	// Every stream the offer promised has opened and drained. Whatever is
	// missing is not arriving.
	if seen >= rs.expectStreams && active == 0 {
		rs.finish()
	}
}

// reportProgress sends TransferProgress at roughly 1 Hz (§8.5) and flushes the
// resume bitmap on the same tick, so a crash loses at most a second of
// verified work rather than the whole transfer.
func (rs *recvState) reportProgress() {
	t := time.NewTicker(ProgressInterval)
	defer t.Stop()
	for {
		select {
		case <-rs.finished:
			return
		case <-rs.ctx.Done():
			return
		case <-t.C:
			_ = rs.set.flush()
			p := Progress{
				TransferID:     rs.id,
				BytesReceived:  rs.bytes.Load(),
				ChunksVerified: rs.set.marked(),
				TotalBytes:     rs.plan.TotalBytes(),
			}
			if rs.cap.cfg.OnProgress != nil {
				rs.cap.cfg.OnProgress(p)
			}
			_ = rs.sess.Send(rs.ctx, CapID, MsgTransferProgress, &openairv1.TransferProgress{
				TransferId:     rs.id,
				BytesReceived:  p.BytesReceived,
				ChunksVerified: p.ChunksVerified,
			})
		}
	}
}

// finish closes out the transfer exactly once. A partial file is never renamed
// into place, so nothing incomplete is ever presented as complete (§8.5).
func (rs *recvState) finish() {
	rs.finishOnce.Do(func() {
		close(rs.finished)
		defer rs.abort()

		if rs.superseded.Load() {
			_ = rs.set.flush()
			rs.close()
			return
		}

		rs.mu.Lock()
		failed := append([]uint64(nil), rs.failed...)
		rs.mu.Unlock()

		complete := rs.set.complete() && len(failed) == 0 && !rs.cancelled.Load()
		_ = rs.set.flush()

		var err error
		if complete {
			err = rs.commit()
		} else {
			rs.close()
		}

		// Only deregister if this is still the live transfer for that id. A
		// re-offer supersedes an in-flight one, and the superseded state
		// finishing afterwards must not evict its replacement.
		rs.cap.mu.Lock()
		if rs.cap.in[rs.id] == rs {
			delete(rs.cap.in, rs.id)
		}
		rs.cap.mu.Unlock()

		ok := complete && err == nil
		if rs.cap.cfg.OnComplete != nil {
			rs.cap.cfg.OnComplete(rs.id, ok)
		}

		_ = rs.sess.Send(context.Background(), CapID, MsgTransferComplete, &openairv1.TransferComplete{
			TransferId:   rs.id,
			Ok:           ok,
			FailedChunks: failed,
		})
	})
}

// commit renames every part file into place and drops the resume state. Only
// reached when every chunk has verified against the manifest.
func (rs *recvState) commit() error {
	var first error
	for i := range rs.dest {
		d := &rs.dest[i]
		if d.f == nil {
			continue
		}
		if err := d.f.Sync(); err != nil && first == nil {
			first = err
		}
		if err := d.f.Close(); err != nil && first == nil {
			first = err
		}
		d.f = nil
		if err := os.Rename(d.part, d.final); err != nil && first == nil {
			first = err
		}
	}
	if first == nil {
		_ = rs.set.discard()
	}
	return first
}

// close releases handles and leaves the part files where they are, because a
// cancel on a flaky link is usually a prelude to retrying and resume is the
// feature that makes that cheap (§8.5).
func (rs *recvState) close() {
	for i := range rs.dest {
		if rs.dest[i].f != nil {
			rs.dest[i].f.Close()
			rs.dest[i].f = nil
		}
	}
}

// discardPartial removes the part files and the resume state. Only on an
// explicit TransferCancel{discard_partial: true}.
func (rs *recvState) discardPartial() {
	rs.close()
	for i := range rs.dest {
		_ = os.Remove(rs.dest[i].part)
	}
	if rs.set != nil {
		_ = rs.set.discard()
	}
}

func (rs *recvState) cancel(m *openairv1.TransferCancel) {
	rs.cancelled.Store(true)
	rs.abort()
	_ = rs.set.flush()
	rs.finish()
	if m.GetDiscardPartial() {
		rs.discardPartial()
	}
}
