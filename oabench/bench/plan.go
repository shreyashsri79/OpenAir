package bench

import (
	"crypto/rand"
	"sync/atomic"
)

// chunkPlan hands out chunk offsets to workers. v1.0 used a buffered channel
// fed by a producer goroutine; an atomic counter is equivalent work
// distribution with less scheduler noise in the hot path, which matters when
// the thing under test is the transport.
type chunkPlan struct {
	total     int64
	chunkSize int64
	next      atomic.Int64
}

func newChunkPlan(total, chunkSize int64) *chunkPlan {
	return &chunkPlan{total: total, chunkSize: chunkSize}
}

// claim returns the next chunk, or ok=false when the file is exhausted.
func (p *chunkPlan) claim() (offset, size int64, ok bool) {
	off := p.next.Add(p.chunkSize) - p.chunkSize
	if off >= p.total {
		return 0, 0, false
	}
	sz := p.chunkSize
	if remaining := p.total - off; remaining < sz {
		sz = remaining
	}
	return off, sz, true
}

// payload is a single reusable buffer of pseudo-random bytes. Content is never
// verified by the benchmark, so every worker sends the same bytes from its own
// copy. Random rather than zeroes: zero pages can be handled differently by
// NIC offload paths and would flatter one transport over the other.
func payload(n int) []byte {
	buf := make([]byte, n)
	rand.Read(buf)
	return buf
}
