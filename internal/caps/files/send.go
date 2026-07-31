package files

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// Item is one file to send: the local file to read, and the relative path the
// receiver should write it to (forward slashes, §8.1).
type Item struct {
	LocalPath string
	RelPath   string
}

// sendState is one outbound transfer's control-plane rendezvous. Serve runs on
// the session's control loop and must not block, so it only ever closes a
// channel or stores a result.
type sendState struct {
	id   string
	plan *Plan

	acceptOnce   sync.Once
	accepted     chan struct{}
	acceptMsg    *openairv1.TransferAccept
	completeOnce sync.Once
	completed    chan struct{}
	completeMsg  *openairv1.TransferComplete
	cancelOnce   sync.Once
	cancelled    chan struct{}
	cancelMsg    *openairv1.TransferCancel
}

func newSendState(id string, plan *Plan) *sendState {
	return &sendState{
		id:        id,
		plan:      plan,
		accepted:  make(chan struct{}),
		completed: make(chan struct{}),
		cancelled: make(chan struct{}),
	}
}

func (s *sendState) accept(m *openairv1.TransferAccept) {
	s.acceptOnce.Do(func() { s.acceptMsg = m; close(s.accepted) })
}

func (s *sendState) complete(m *openairv1.TransferComplete) {
	s.completeOnce.Do(func() { s.completeMsg = m; close(s.completed) })
}

func (s *sendState) cancel(m *openairv1.TransferCancel) {
	s.cancelOnce.Do(func() { s.cancelMsg = m; close(s.cancelled) })
}

// NewTransferID returns a fresh transfer identifier (§8.1).
func NewTransferID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("files: crypto/rand failed: " + err.Error())
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}

// Send offers items to the peer and, if accepted, transfers them.
//
// It returns when the peer reports completion, when either side cancels, or
// when ctx is done. The stream count comes from Config and defaults to two --
// see D-13 and D-33 before raising it.
func (c *Capability) Send(ctx context.Context, sess session.Session, items []Item) (string, error) {
	id := NewTransferID()
	return id, c.SendWithID(ctx, sess, id, items)
}

// SendWithID is Send with a caller-chosen transfer id. Resume re-offers the
// same id, which is what lets the receiver return its verified-chunk set
// (§8.4).
func (c *Capability) SendWithID(ctx context.Context, sess session.Session, id string, items []Item) error {
	ctx, abort := context.WithCancel(ctx)
	defer abort()

	srcs, metas, err := openSources(items)
	if err != nil {
		return err
	}
	defer func() {
		for _, f := range srcs {
			f.Close()
		}
	}()

	chunkSize := c.cfg.chunkSize()
	sizes := make([]uint64, len(metas))
	for i, m := range metas {
		sizes[i] = m.GetSize()
	}
	plan, err := NewPlan(sizes, chunkSize)
	if err != nil {
		return err
	}

	// One pass over the sources fills both the per-file digests carried in the
	// offer and the per-chunk digests carried in the manifest. Digests live in
	// the manifest rather than in the chunk frame so the hot path stays 12
	// bytes (§8.4).
	chunkDigests, err := digestSources(plan, srcs, metas)
	if err != nil {
		return err
	}

	st := newSendState(id, plan)
	c.mu.Lock()
	c.out[id] = st
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.out, id)
		c.mu.Unlock()
	}()

	streams := c.cfg.streams()
	offer := &openairv1.TransferOffer{
		TransferId:  id,
		Files:       metas,
		TotalBytes:  plan.TotalBytes(),
		StreamCount: uint32(streams),
		ChunkSize:   chunkSize,
	}
	// An offer carries an AuthProof when this device has an unlock session live
	// for the peer (§6). Nothing changes for the transfer itself; what changes
	// is that an Owned receiver may accept it with nobody watching, rather than
	// waiting for a human who is not there (M6, PRD R11).
	if _, err := session.SendOwnedIfUnlocked(ctx, sess, CapID, MsgTransferOffer, offer); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-st.cancelled:
		return cancelErr(st.cancelMsg)
	case <-st.accepted:
	}
	if !st.acceptMsg.GetAccepted() {
		return ErrRejected
	}
	plan.SetHave(st.acceptMsg.GetHaveChunks())

	if err := sess.Send(ctx, CapID, MsgChunkManifest, &openairv1.ChunkManifest{
		TransferId:  id,
		ChunkSize:   chunkSize,
		ChunkSha256: chunkDigests,
	}); err != nil {
		return err
	}

	// A cancel arriving mid-transfer must stop the data streams, not just the
	// caller: abort() unblocks every worker's context check.
	go func() {
		select {
		case <-st.cancelled:
			abort()
		case <-ctx.Done():
		}
	}()

	if err := c.pump(ctx, sess, id, plan, srcs, streams); err != nil {
		select {
		case <-st.cancelled:
			return cancelErr(st.cancelMsg)
		default:
		}
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-st.cancelled:
		return cancelErr(st.cancelMsg)
	case <-st.completed:
	}
	if !st.completeMsg.GetOk() {
		return fmt.Errorf("%w: %d chunks", ErrVerification, len(st.completeMsg.GetFailedChunks()))
	}
	return nil
}

func cancelErr(m *openairv1.TransferCancel) error {
	if m == nil || m.GetReason() == "" {
		return ErrCancelled
	}
	return fmt.Errorf("%w: %s", ErrCancelled, m.GetReason())
}

// pump opens the data streams and drains the plan across them.
func (c *Capability) pump(ctx context.Context, sess session.Session, id string, plan *Plan, srcs []*os.File, streams int) error {
	init, err := proto.Marshal(&openairv1.StreamInit{TransferId: id})
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	errs := make(chan error, streams)
	for i := 0; i < streams; i++ {
		stream, err := sess.OpenStream(ctx)
		if err != nil {
			wg.Wait()
			return fmt.Errorf("files: open data stream %d: %w", i, err)
		}
		// §8.2: each data stream opens with one enveloped StreamInit and then
		// carries raw chunk frames only.
		if err := encodeEnvelope(stream, session.Envelope{
			Version: 1,
			CapID:   CapID,
			MsgType: MsgStreamInit,
			Payload: init,
		}); err != nil {
			stream.Close()
			wg.Wait()
			return err
		}
		wg.Add(1)
		go func(s session.Stream) {
			defer wg.Done()
			err := sendChunks(ctx, s, plan, srcs, plan.ChunkSize())
			if err != nil {
				s.Reset(0)
				errs <- err
				return
			}
			// Closing the send direction signals EOF to the peer.
			s.Close()
		}(stream)
	}
	wg.Wait()
	close(errs)
	return <-errs
}

// sendChunks drains the plan onto one stream. Header and payload go out in a
// single Write per chunk; v1.0 issued three and measurably paid for it (§8.3).
func sendChunks(ctx context.Context, w session.Stream, plan *Plan, srcs []*os.File, chunkSize uint64) error {
	buf := make([]byte, ChunkHeaderSize+chunkSize)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		ch, ok := plan.Claim()
		if !ok {
			return nil
		}
		body := buf[ChunkHeaderSize : ChunkHeaderSize+int(ch.Size)]
		if _, err := srcs[ch.FileIndex].ReadAt(body, int64(ch.FileOffset)); err != nil {
			return fmt.Errorf("files: read chunk %d: %w", ch.Index, err)
		}
		putChunkHeader(buf, ch.Offset, ch.Size)
		if err := writeFull(w, buf[:ChunkHeaderSize+int(ch.Size)]); err != nil {
			return err
		}
	}
}

// openSources opens every item and builds its FileMeta. Relative paths are
// validated on the sending side too, so a bad path is caught before it is
// offered rather than only when the peer refuses it.
func openSources(items []Item) ([]*os.File, []*openairv1.FileMeta, error) {
	srcs := make([]*os.File, 0, len(items))
	metas := make([]*openairv1.FileMeta, 0, len(items))
	fail := func(err error) ([]*os.File, []*openairv1.FileMeta, error) {
		for _, f := range srcs {
			f.Close()
		}
		return nil, nil, err
	}
	if len(items) == 0 {
		return nil, nil, errors.New("files: nothing to send")
	}
	for _, it := range items {
		rel, err := safeRel(it.RelPath)
		if err != nil {
			return fail(err)
		}
		f, err := os.Open(it.LocalPath)
		if err != nil {
			return fail(err)
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return fail(err)
		}
		if fi.IsDir() {
			f.Close()
			return fail(fmt.Errorf("files: %s is a directory", it.LocalPath))
		}
		srcs = append(srcs, f)
		metas = append(metas, &openairv1.FileMeta{
			Path:       rel,
			Size:       uint64(fi.Size()),
			ModifiedAt: fi.ModTime().Unix(),
		})
	}
	return srcs, metas, nil
}

// digestSources computes the per-chunk digests for the manifest and the
// whole-file digests for the offer in a single pass.
func digestSources(plan *Plan, srcs []*os.File, metas []*openairv1.FileMeta) ([][]byte, error) {
	digests := make([][]byte, plan.Count())
	buf := make([]byte, plan.ChunkSize())
	whole := sha256.New()
	current := -1
	for i := uint64(0); i < plan.Count(); i++ {
		ch, ok := plan.Chunk(i)
		if !ok {
			return nil, fmt.Errorf("%w: chunk %d missing", ErrBadPlan, i)
		}
		if ch.FileIndex != current {
			if current >= 0 {
				metas[current].Sha256 = whole.Sum(nil)
			}
			whole.Reset()
			current = ch.FileIndex
		}
		body := buf[:ch.Size]
		if _, err := srcs[ch.FileIndex].ReadAt(body, int64(ch.FileOffset)); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		sum := sha256.Sum256(body)
		digests[i] = sum[:]
		whole.Write(body)
	}
	if current >= 0 {
		metas[current].Sha256 = whole.Sum(nil)
	}
	// Files with no chunks (empty files) still get their digest.
	for _, m := range metas {
		if m.GetSize() == 0 && len(m.GetSha256()) == 0 {
			sum := sha256.Sum256(nil)
			m.Sha256 = sum[:]
		}
	}
	return digests, nil
}
