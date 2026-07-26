package bench

import (
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// Config describes one benchmark run. The same values drive both transports so
// the only variable between rows of the matrix is the transport itself.
type Config struct {
	Addr       string
	Streams    int
	ChunkBytes int64
	TotalBytes int64
	Profile    string
	Label      string

	// SinkPath, if set, makes the receiver reconstruct into a real file using
	// WriteAt at each chunk's offset -- v1.0's out-of-order write path. Left
	// empty the receiver discards, which is the default because the question
	// under test is transport cost, not disk.
	SinkPath string

	// Probe runs a latency probe alongside the bulk transfer: a small
	// request/response ping on a stream, and on QUIC also over datagrams,
	// sampled before and during the transfer. It answers what a congestion
	// controller costs interactive traffic sharing the connection -- the
	// question D-16 raises about BBRv1's standing queue.
	Probe bool

	// QUIC flow-control windows. Defaults are deliberately generous: quic-go's
	// stock windows throttle a high bandwidth-delay-product path, and a run
	// that measures default flow control instead of the transport would answer
	// the wrong question.
	StreamWindow uint64
	ConnWindow   uint64
}

// sendChunks drains the plan onto one writer. Header and payload go out in a
// single Write per chunk.
func sendChunks(w io.Writer, plan *chunkPlan, buf []byte, src []byte) error {
	for {
		offset, size, ok := plan.claim()
		if !ok {
			return nil
		}
		putHeader(buf, offset, int32(size))
		copy(buf[HeaderSize:HeaderSize+size], src[:size])
		if _, err := w.Write(buf[:HeaderSize+size]); err != nil {
			return err
		}
	}
}

// sink absorbs received chunks, either discarding them or writing each at its
// declared offset.
type sink struct {
	file  *os.File
	count atomic.Int64
	total int64
	once  sync.Once
	done  chan struct{}
}

func newSink(total int64, path string) (*sink, error) {
	s := &sink{total: total, done: make(chan struct{})}
	if path == "" {
		return s, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(total); err != nil {
		f.Close()
		return nil, err
	}
	s.file = f
	return s, nil
}

func (s *sink) close() {
	if s.file != nil {
		s.file.Close()
	}
}

// recvChunks reads framed chunks off one reader until the peer closes it or the
// transfer completes. A short read at the end of a stream is normal: the sender
// closes as soon as the plan is drained.
func (s *sink) recvChunks(r io.Reader, buf []byte) error {
	for {
		offset, size, err := readHeader(r, buf)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		if int64(size) > int64(len(buf))-HeaderSize {
			return errors.New("chunk larger than receive buffer")
		}
		if _, err := io.ReadFull(r, buf[HeaderSize:HeaderSize+int(size)]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		if s.file != nil {
			if _, err := s.file.WriteAt(buf[HeaderSize:HeaderSize+int(size)], offset); err != nil {
				return err
			}
		}
		if s.count.Add(int64(size)) >= s.total {
			s.once.Do(func() { close(s.done) })
			return nil
		}
	}
}

func (s *sink) wait() <-chan struct{} { return s.done }
