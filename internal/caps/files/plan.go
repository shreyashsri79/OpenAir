package files

import (
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
)

// The transfer's byte space is the concatenation of the offered files, in offer
// order. A chunk frame carries only an offset and a size (§8.3, 12 bytes, no
// file identifier), so that concatenation is the only thing that lets a
// receiver tell which file a chunk belongs to. Chunks therefore never span a
// file boundary: chunk index i maps to exactly one file, and the last chunk of
// each file is short. See the report note on §8.3.

// Chunk is one unit of work. Offset is the transfer-global offset that goes on
// the wire; FileIndex and FileOffset are what the receiver writes with.
type Chunk struct {
	Index      uint64
	Offset     uint64
	Size       uint32
	FileIndex  int
	FileOffset uint64
}

// planFile is one file's position in the concatenated byte space.
type planFile struct {
	start      uint64 // global offset of this file's first byte
	size       uint64
	firstChunk uint64
	chunks     uint64
}

// Plan hands out chunks. v1.0 used a buffered channel fed by a producer
// goroutine; an atomic counter is equivalent work distribution with less
// scheduler noise in the hot path (ported from oabench/bench/plan.go).
//
// Chunks are computed from file boundaries rather than materialised, so a
// terabyte transfer costs one small slice rather than a million structs.
type Plan struct {
	chunkSize uint64
	total     uint64
	count     uint64
	files     []planFile
	skip      []bool // resume: chunk already verified by the receiver

	next atomic.Uint64
}

// ErrBadPlan reports an offer that cannot be turned into a chunk plan.
var ErrBadPlan = errors.New("files: invalid chunk plan")

// NewPlan builds the plan for files of the given sizes, in offer order.
func NewPlan(sizes []uint64, chunkSize uint64) (*Plan, error) {
	if chunkSize == 0 || chunkSize > MaxChunkSize {
		return nil, fmt.Errorf("%w: chunk size %d", ErrBadPlan, chunkSize)
	}
	p := &Plan{chunkSize: chunkSize, files: make([]planFile, 0, len(sizes))}
	for _, sz := range sizes {
		n := sz / chunkSize
		if sz%chunkSize != 0 {
			n++
		}
		p.files = append(p.files, planFile{
			start:      p.total,
			size:       sz,
			firstChunk: p.count,
			chunks:     n,
		})
		p.total += sz
		p.count += n
	}
	return p, nil
}

// TotalBytes is the size of the concatenated byte space.
func (p *Plan) TotalBytes() uint64 { return p.total }

// Count is the number of chunks in the plan.
func (p *Plan) Count() uint64 { return p.count }

// ChunkSize is the negotiated chunk size.
func (p *Plan) ChunkSize() uint64 { return p.chunkSize }

// Chunk returns the chunk at index i.
func (p *Plan) Chunk(i uint64) (Chunk, bool) {
	if i >= p.count {
		return Chunk{}, false
	}
	// Files are ordered by firstChunk, so the owning file is the last one
	// whose firstChunk is <= i.
	fi := sort.Search(len(p.files), func(k int) bool {
		return p.files[k].firstChunk > i
	}) - 1
	// Zero-byte files contribute no chunks and share a firstChunk with their
	// successor; step forward off them.
	for fi >= 0 && fi < len(p.files) && p.files[fi].chunks == 0 {
		fi++
	}
	if fi < 0 || fi >= len(p.files) {
		return Chunk{}, false
	}
	f := p.files[fi]
	fileOff := (i - f.firstChunk) * p.chunkSize
	size := p.chunkSize
	if rem := f.size - fileOff; rem < size {
		size = rem
	}
	return Chunk{
		Index:      i,
		Offset:     f.start + fileOff,
		Size:       uint32(size),
		FileIndex:  fi,
		FileOffset: fileOff,
	}, true
}

// SetHave marks chunk indices the receiver has already verified, so Claim skips
// them. Resume per §8.4: the receiver returns these in TransferAccept.have_chunks
// and the sender does not resend them.
func (p *Plan) SetHave(have []uint64) {
	if len(have) == 0 {
		return
	}
	if p.skip == nil {
		p.skip = make([]bool, p.count)
	}
	for _, i := range have {
		if i < p.count {
			p.skip[i] = true
		}
	}
}

// Claim returns the next chunk to send, or ok=false when the plan is drained.
// Safe for concurrent use by every data stream.
func (p *Plan) Claim() (Chunk, bool) {
	for {
		i := p.next.Add(1) - 1
		if i >= p.count {
			return Chunk{}, false
		}
		if p.skip != nil && p.skip[i] {
			continue
		}
		return p.Chunk(i)
	}
}

// Remaining is the number of bytes Claim will still hand out, counting from a
// fresh plan. Used for progress denominators, not for control flow.
func (p *Plan) Remaining() uint64 {
	if p.skip == nil {
		return p.total
	}
	var n uint64
	for i := range p.skip {
		if !p.skip[i] {
			c, _ := p.Chunk(uint64(i))
			n += uint64(c.Size)
		}
	}
	return n
}

// Locate maps a received frame's global offset and size back to a chunk. It
// rejects anything that is not exactly a chunk of this plan: a frame at an
// unaligned offset, one that overruns its file, or one whose size disagrees
// with the plan. That check is what stops a peer writing arbitrary bytes at
// arbitrary places in the destination files.
func (p *Plan) Locate(offset uint64, size uint32) (Chunk, error) {
	if size == 0 || uint64(size) > p.chunkSize {
		return Chunk{}, fmt.Errorf("%w: size %d", ErrChunkTooLarge, size)
	}
	if offset >= p.total {
		return Chunk{}, fmt.Errorf("%w: offset %d beyond total %d", ErrBadPlan, offset, p.total)
	}
	fi := sort.Search(len(p.files), func(k int) bool {
		return p.files[k].start+p.files[k].size > offset
	})
	for fi < len(p.files) && p.files[fi].size == 0 {
		fi++
	}
	if fi >= len(p.files) {
		return Chunk{}, fmt.Errorf("%w: offset %d in no file", ErrBadPlan, offset)
	}
	f := p.files[fi]
	fileOff := offset - f.start
	if fileOff%p.chunkSize != 0 {
		return Chunk{}, fmt.Errorf("%w: offset %d is not chunk-aligned", ErrBadPlan, offset)
	}
	c, ok := p.Chunk(f.firstChunk + fileOff/p.chunkSize)
	if !ok {
		return Chunk{}, fmt.Errorf("%w: offset %d has no chunk", ErrBadPlan, offset)
	}
	if c.Size != size {
		return Chunk{}, fmt.Errorf("%w: chunk %d declared %d bytes, plan says %d",
			ErrBadPlan, c.Index, size, c.Size)
	}
	return c, nil
}
