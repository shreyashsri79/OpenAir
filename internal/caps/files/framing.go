package files

import (
	"encoding/binary"
	"errors"
	"io"
)

// ChunkHeaderSize is the raw chunk header: little-endian uint64 offset followed
// by uint32 size (PROTOCOL.md §8.3). Byte-identical to OpenAir v1.0's, and
// deliberately not protobuf -- a 1 MiB chunk must never meet a parser.
const ChunkHeaderSize = 12

// MaxChunkSize bounds what a receiver will allocate for one frame. A peer that
// declares a larger size is either broken or hostile; either way we refuse
// rather than allocate.
const MaxChunkSize = 16 << 20

// DefaultChunkSize matches v1.0 and every oabench run behind D-13 and D-33.
const DefaultChunkSize = 1 << 20

// ErrChunkTooLarge is returned when a frame declares more than MaxChunkSize or
// more than the negotiated chunk size for the transfer.
var ErrChunkTooLarge = errors.New("files: chunk larger than negotiated size")

// putChunkHeader writes the header into the first ChunkHeaderSize bytes of buf.
// Header and payload share one buffer so a chunk costs exactly one Write;
// v1.0 issued three per chunk and measurably paid for it (§8.3).
func putChunkHeader(buf []byte, offset uint64, size uint32) {
	binary.LittleEndian.PutUint64(buf[0:8], offset)
	binary.LittleEndian.PutUint32(buf[8:12], size)
}

// readChunkHeader reads one header off r using buf as scratch.
func readChunkHeader(r io.Reader, buf []byte) (offset uint64, size uint32, err error) {
	if _, err := io.ReadFull(r, buf[:ChunkHeaderSize]); err != nil {
		return 0, 0, err
	}
	return binary.LittleEndian.Uint64(buf[0:8]),
		binary.LittleEndian.Uint32(buf[8:12]), nil
}

// writeFull writes all of p, looping over short writes.
//
// io.Writer's contract says a short write must come with an error, but a
// session.Stream is an interface any transport may implement and a chunk engine
// that trusts the contract corrupts files under exactly the conditions that are
// hardest to reproduce. The loop costs one comparison per chunk.
func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n > 0 {
			p = p[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// writeAtFull is writeFull for a WriterAt, looping over short writes at an
// advancing offset.
func writeAtFull(w io.WriterAt, p []byte, off int64) error {
	for len(p) > 0 {
		n, err := w.WriteAt(p, off)
		if n > 0 {
			p = p[n:]
			off += int64(n)
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
