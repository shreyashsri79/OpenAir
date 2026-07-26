package bench

import (
	"encoding/binary"
	"io"
)

// HeaderSize is OpenAir v1.0's on-wire chunk header: int64 offset + int32 size,
// little-endian. Kept byte-identical here so the TCP baseline and the QUIC
// candidate are framed the same way and the measurement isolates transport.
const HeaderSize = 12

// Role tags are the first byte on every connection/stream so the receiver can
// tell the control channel from a bulk data channel without relying on
// accept ordering.
const (
	roleControl byte = 0
	roleData    byte = 1
	rolePing    byte = 2
)

// preambleFlagProbe asks the receiver to stand up the latency probe echo paths
// in addition to the bulk sink.
const preambleFlagProbe uint32 = 1 << 0

// preamble is what the sender declares on the control channel before any bulk
// data moves, so the receiver knows when the transfer is complete.
type preamble struct {
	TotalBytes int64
	Streams    int32
	ChunkBytes int32
	Flags      uint32
}

func (p preamble) probing() bool { return p.Flags&preambleFlagProbe != 0 }

func probeFlag(on bool) uint32 {
	if on {
		return preambleFlagProbe
	}
	return 0
}

func writePreamble(w io.Writer, p preamble) error {
	var b [20]byte
	binary.LittleEndian.PutUint64(b[0:8], uint64(p.TotalBytes))
	binary.LittleEndian.PutUint32(b[8:12], uint32(p.Streams))
	binary.LittleEndian.PutUint32(b[12:16], uint32(p.ChunkBytes))
	binary.LittleEndian.PutUint32(b[16:20], p.Flags)
	_, err := w.Write(b[:])
	return err
}

func readPreamble(r io.Reader) (preamble, error) {
	var b [20]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return preamble{}, err
	}
	return preamble{
		TotalBytes: int64(binary.LittleEndian.Uint64(b[0:8])),
		Streams:    int32(binary.LittleEndian.Uint32(b[8:12])),
		ChunkBytes: int32(binary.LittleEndian.Uint32(b[12:16])),
		Flags:      binary.LittleEndian.Uint32(b[16:20]),
	}, nil
}

// putHeader writes the chunk header into the first HeaderSize bytes of buf.
// Header and payload share one buffer so each chunk costs exactly one Write --
// v1.0 issued three (two binary.Write calls plus the data), which would have
// made the baseline artificially slow. See README "Fairness notes".
func putHeader(buf []byte, offset int64, size int32) {
	binary.LittleEndian.PutUint64(buf[0:8], uint64(offset))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(size))
}

func readHeader(r io.Reader, buf []byte) (offset int64, size int32, err error) {
	if _, err := io.ReadFull(r, buf[:HeaderSize]); err != nil {
		return 0, 0, err
	}
	return int64(binary.LittleEndian.Uint64(buf[0:8])),
		int32(binary.LittleEndian.Uint32(buf[8:12])), nil
}
