package files

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// TestChunkHeaderRoundTrip is oabench/bench/bench_test.go's header test,
// ported. The 12-byte layout is inherited byte-for-byte from v1.0 (§8.3), so
// this also guards wire compatibility with it.
func TestChunkHeaderRoundTrip(t *testing.T) {
	buf := make([]byte, ChunkHeaderSize+8)
	putChunkHeader(buf, 1<<40, 4096)

	gotOff, gotSize, err := readChunkHeader(bytes.NewReader(buf), make([]byte, ChunkHeaderSize))
	if err != nil {
		t.Fatal(err)
	}
	if gotOff != 1<<40 {
		t.Errorf("offset = %d, want %d", gotOff, uint64(1)<<40)
	}
	if gotSize != 4096 {
		t.Errorf("size = %d, want 4096", gotSize)
	}
}

// TestChunkHeaderIsLittleEndian pins the byte order rather than trusting the
// round trip, which would pass just as well if both ends were wrong together.
func TestChunkHeaderIsLittleEndian(t *testing.T) {
	buf := make([]byte, ChunkHeaderSize)
	putChunkHeader(buf, 0x0102030405060708, 0x0a0b0c0d)
	want := []byte{8, 7, 6, 5, 4, 3, 2, 1, 0x0d, 0x0c, 0x0b, 0x0a}
	if !bytes.Equal(buf, want) {
		t.Errorf("header = % x, want % x", buf, want)
	}
}

// choppyWriter accepts at most n bytes per call and returns a nil error, which
// is what a stream implementation is not supposed to do and may do anyway.
type choppyWriter struct {
	n   int
	buf bytes.Buffer
}

func (w *choppyWriter) Write(p []byte) (int, error) {
	if len(p) > w.n {
		p = p[:w.n]
	}
	return w.buf.Write(p)
}

func TestWriteFullSurvivesPartialWrites(t *testing.T) {
	src := make([]byte, 10000)
	for i := range src {
		src[i] = byte(i)
	}
	w := &choppyWriter{n: 7}
	if err := writeFull(w, src); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w.buf.Bytes(), src) {
		t.Fatal("writeFull lost or reordered bytes under short writes")
	}
}

type stallWriter struct{}

func (stallWriter) Write(p []byte) (int, error) { return 0, nil }

// A writer that accepts nothing and reports no error must not spin forever.
func TestWriteFullRejectsStall(t *testing.T) {
	if err := writeFull(stallWriter{}, []byte{1}); !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("err = %v, want io.ErrShortWrite", err)
	}
}

// choppyWriterAt is the WriteAt equivalent: the destination file is written
// out of order across streams, so a short WriteAt that advanced the wrong
// offset would scatter data.
type choppyWriterAt struct {
	n   int
	buf []byte
}

func (w *choppyWriterAt) WriteAt(p []byte, off int64) (int, error) {
	if len(p) > w.n {
		p = p[:w.n]
	}
	if need := int(off) + len(p); need > len(w.buf) {
		w.buf = append(w.buf, make([]byte, need-len(w.buf))...)
	}
	copy(w.buf[off:], p)
	return len(p), nil
}

func TestWriteAtFullSurvivesPartialWrites(t *testing.T) {
	src := make([]byte, 5000)
	for i := range src {
		src[i] = byte(i * 3)
	}
	w := &choppyWriterAt{n: 13}
	if err := writeAtFull(w, src, 1000); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w.buf[1000:], src) {
		t.Fatal("writeAtFull scattered bytes under short writes")
	}
	for i, b := range w.buf[:1000] {
		if b != 0 {
			t.Fatalf("writeAtFull wrote %d before the requested offset (byte %d)", b, i)
		}
	}
}
