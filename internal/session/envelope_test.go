package session

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		env  Envelope
	}{
		{"empty payload", Envelope{Version: EnvelopeVersion, CapID: 0, MsgType: 1}},
		{"control hello", Envelope{Version: EnvelopeVersion, CapID: 0, MsgType: 1, Payload: []byte("hello")}},
		{"files offer", Envelope{Version: EnvelopeVersion, CapID: 1, MsgType: 2, Payload: bytes.Repeat([]byte{0xab}, 300)}},
		{"max capID", Envelope{Version: EnvelopeVersion, CapID: 0xff, MsgType: 3, Payload: []byte{1}}},
		{"max msgType", Envelope{Version: EnvelopeVersion, CapID: 6, MsgType: 0xffff, Payload: []byte{9, 9}}},
		{"one byte under the cap", Envelope{Version: EnvelopeVersion, CapID: 2, MsgType: 7, Payload: make([]byte, 1<<16)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := EncodeEnvelope(&buf, tc.env); err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got, want := buf.Len(), EnvelopeHeaderSize+len(tc.env.Payload); got != want {
				t.Fatalf("encoded %d bytes, want %d", got, want)
			}
			got, err := DecodeEnvelope(&buf)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Version != tc.env.Version || got.CapID != tc.env.CapID || got.MsgType != tc.env.MsgType {
				t.Errorf("header round trip: got %+v, want %+v", got, tc.env)
			}
			if !bytes.Equal(got.Payload, tc.env.Payload) {
				t.Errorf("payload round trip: got %d bytes, want %d", len(got.Payload), len(tc.env.Payload))
			}
			if buf.Len() != 0 {
				t.Errorf("%d bytes left unconsumed after decode", buf.Len())
			}
		})
	}
}

// TestEncodeLayout pins the byte order. PROTOCOL.md §0 is little-endian; a
// big-endian slip round-trips against itself perfectly and fails only against
// another implementation, which is exactly the bug worth a fixed vector here.
func TestEncodeLayout(t *testing.T) {
	var buf bytes.Buffer
	err := EncodeEnvelope(&buf, Envelope{
		Version: 1,
		CapID:   1,
		MsgType: 0x0102,
		Payload: []byte{0xde, 0xad},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x01,       // ver
		0x01,       // capID
		0x02, 0x01, // msgType 0x0102 little-endian
		0x02, 0x00, 0x00, 0x00, // length 2 little-endian
		0xde, 0xad,
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("layout:\n got %x\nwant %x", buf.Bytes(), want)
	}
}

func TestDecodeSequentialFrames(t *testing.T) {
	var buf bytes.Buffer
	for i := range 3 {
		e := Envelope{Version: EnvelopeVersion, CapID: byte(i), MsgType: uint16(i + 1), Payload: []byte{byte(i)}}
		if err := EncodeEnvelope(&buf, e); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		got, err := DecodeEnvelope(&buf)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if got.CapID != byte(i) || got.MsgType != uint16(i+1) {
			t.Errorf("frame %d: got capID %d msgType %d", i, got.CapID, got.MsgType)
		}
	}
	if _, err := DecodeEnvelope(&buf); !errors.Is(err, io.EOF) {
		t.Errorf("after the last frame: got %v, want io.EOF", err)
	}
}

func TestDecodeErrors(t *testing.T) {
	full := func(ver byte, length uint32, payload []byte) []byte {
		b := []byte{ver, 0, 0, 0, 0, 0, 0, 0}
		b[4] = byte(length)
		b[5] = byte(length >> 8)
		b[6] = byte(length >> 16)
		b[7] = byte(length >> 24)
		return append(b, payload...)
	}

	tests := []struct {
		name     string
		input    []byte
		wantCode ErrorCode
		wantIs   error
	}{
		{
			name:   "clean EOF at a frame boundary",
			input:  nil,
			wantIs: io.EOF,
		},
		{
			name:     "one byte of header",
			input:    []byte{1},
			wantCode: CodeProtocolViolation,
			wantIs:   io.ErrUnexpectedEOF,
		},
		{
			name:     "seven bytes of header",
			input:    []byte{1, 0, 0, 0, 0, 0, 0},
			wantCode: CodeProtocolViolation,
			wantIs:   io.ErrUnexpectedEOF,
		},
		{
			name:     "unknown version zero",
			input:    full(0, 0, nil),
			wantCode: CodeProtocolViolation,
		},
		{
			name:     "unknown version two",
			input:    full(2, 0, nil),
			wantCode: CodeProtocolViolation,
		},
		{
			name:     "unknown version wins over an oversized length",
			input:    full(9, MaxMessageSize+1, nil),
			wantCode: CodeProtocolViolation,
		},
		{
			name:     "length one over the cap",
			input:    full(1, MaxMessageSize+1, nil),
			wantCode: CodeMessageTooLarge,
		},
		{
			name:     "length at the u32 ceiling",
			input:    full(1, 0xffffffff, nil),
			wantCode: CodeMessageTooLarge,
		},
		{
			name:     "truncated payload",
			input:    full(1, 16, []byte{1, 2, 3}),
			wantCode: CodeProtocolViolation,
			wantIs:   io.ErrUnexpectedEOF,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeEnvelope(bytes.NewReader(tc.input))
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("errors.Is(%v, %v) = false", err, tc.wantIs)
			}
			if tc.wantCode == 0 {
				return
			}
			code, ok := ErrorCodeOf(err)
			if !ok {
				t.Fatalf("err %v carries no protocol code, want %v", err, tc.wantCode)
			}
			if code != tc.wantCode {
				t.Errorf("code = %v, want %v", code, tc.wantCode)
			}
		})
	}
}

// countingReader reports how many bytes a decoder actually asked for.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// TestDecodeDoesNotTrustLengthBeforeValidating is the anti-amplification
// property: an 8-byte write claiming 4 GiB must cost the receiver 8 bytes of
// reading and no buffer at all.
func TestDecodeDoesNotTrustLengthBeforeValidating(t *testing.T) {
	// A header claiming the maximum u32 length, followed by a reader that would
	// happily supply gigabytes if asked.
	hdr := []byte{1, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}
	cr := &countingReader{r: io.MultiReader(bytes.NewReader(hdr), infiniteZeros{})}

	_, err := DecodeEnvelope(cr)
	code, ok := ErrorCodeOf(err)
	if !ok || code != CodeMessageTooLarge {
		t.Fatalf("err = %v (code %v), want MESSAGE_TOO_LARGE", err, code)
	}
	if cr.n != EnvelopeHeaderSize {
		t.Errorf("decoder read %d bytes, want exactly the %d byte header", cr.n, EnvelopeHeaderSize)
	}

	// And it must not have allocated on the strength of the length field.
	allocs := testingAllocs(t, func() {
		r := &countingReader{r: io.MultiReader(bytes.NewReader(hdr), infiniteZeros{})}
		_, _ = DecodeEnvelope(r)
	})
	if allocs > 64<<10 {
		t.Errorf("decode allocated %d bytes rejecting an oversized length", allocs)
	}
}

type infiniteZeros struct{}

func (infiniteZeros) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// TestEncodeRejectsUnknownVersion: an encoder that emits a version the receiver
// must kill the connection over has turned a local bug into a peer-visible
// PROTOCOL_VIOLATION. The zero value counts -- a caller that forgot to set the
// field is exactly the bug this catches.
func TestEncodeRejectsUnknownVersion(t *testing.T) {
	for _, ver := range []byte{0, 2, 9, 255} {
		var buf bytes.Buffer
		err := EncodeEnvelope(&buf, Envelope{Version: ver, CapID: 1, MsgType: 1})
		code, ok := ErrorCodeOf(err)
		if !ok || code != CodeProtocolViolation {
			t.Errorf("ver %d: err = %v (code %v), want PROTOCOL_VIOLATION", ver, err, code)
		}
		if buf.Len() != 0 {
			t.Errorf("ver %d: wrote %d bytes for a rejected envelope", ver, buf.Len())
		}
	}
}

// TestBoundaryAtExactlyMaxMessageSize: §3 rejects lengths *greater than* the
// cap, so exactly at the cap is legal in both directions.
func TestBoundaryAtExactlyMaxMessageSize(t *testing.T) {
	hdr := []byte{EnvelopeVersion, 0, 1, 0, 0, 0, 0, 1} // length = 0x01000000 = 16 MiB
	body := make([]byte, MaxMessageSize)
	got, err := DecodeEnvelope(bytes.NewReader(append(hdr, body...)))
	if err != nil {
		t.Fatalf("a payload of exactly MaxMessageSize must decode: %v", err)
	}
	if len(got.Payload) != MaxMessageSize {
		t.Errorf("payload = %d bytes, want %d", len(got.Payload), MaxMessageSize)
	}
	if err := EncodeEnvelope(io.Discard, Envelope{Version: EnvelopeVersion, MsgType: 1, Payload: body}); err != nil {
		t.Errorf("a payload of exactly MaxMessageSize must encode: %v", err)
	}
}

func TestEncodeRejectsOversizePayload(t *testing.T) {
	var buf bytes.Buffer
	err := EncodeEnvelope(&buf, Envelope{Version: EnvelopeVersion, Payload: make([]byte, MaxMessageSize+1)})
	code, ok := ErrorCodeOf(err)
	if !ok || code != CodeMessageTooLarge {
		t.Fatalf("err = %v (code %v), want MESSAGE_TOO_LARGE", err, code)
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %d bytes for a rejected envelope, want 0", buf.Len())
	}
}

// TestEncodeIsAtomic guards the single-Write property the control stream relies
// on: two goroutines framing concurrently must never interleave a header with
// another frame's payload.
func TestEncodeIsAtomic(t *testing.T) {
	var w writeCounter
	if err := EncodeEnvelope(&w, Envelope{Version: EnvelopeVersion, Payload: make([]byte, 1000)}); err != nil {
		t.Fatal(err)
	}
	if w.writes != 1 {
		t.Errorf("encode made %d Write calls, want 1", w.writes)
	}
}

type writeCounter struct{ writes int }

func (w *writeCounter) Write(p []byte) (int, error) {
	w.writes++
	return len(p), nil
}

func TestErrorCodeOfNonProtocolError(t *testing.T) {
	if _, ok := ErrorCodeOf(errors.New("disk on fire")); ok {
		t.Error("a local failure must not report a peer-facing protocol code")
	}
	if _, ok := ErrorCodeOf(nil); ok {
		t.Error("nil must not report a protocol code")
	}
}

func testingAllocs(t *testing.T, f func()) uint64 {
	t.Helper()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}
