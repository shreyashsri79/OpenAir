package session

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// Golden vectors for the 8-byte envelope header (PROTOCOL.md §3; HLD §5 asks
// for these explicitly). The vectors live in testdata/envelope_vectors.json
// and were derived BY HAND from §3's field layout and §0's little-endian
// rule -- never by running EncodeEnvelope/DecodeEnvelope and recording their
// output. A vector produced that way can never fail and defeats the purpose
// of this test (track X4). See the JSON file's "_readme" for the full
// derivation notes, the D-34 offset rule as actually applied, and two
// spec ambiguities found while deriving exact bytes from prose.
//
// This file owns testdata/** and itself only. internal/session/envelope.go,
// convert.go, types.go and everything else in this package belong to task
// M1a.

type envelopeVectorFile struct {
	ProtocolVersion     int            `json:"protocol_version"`
	EnvelopeHeaderSize  int            `json:"envelope_header_size"`
	MaxMessageSizeBytes int64          `json:"max_message_size_bytes"`
	Cases               []envelopeCase `json:"cases"`
}

type envelopeCase struct {
	Name           string `json:"name"`
	ProtocolClause string `json:"protocol_clause"`
	Description    string `json:"description"`
	Fields         struct {
		Ver            int     `json:"ver"`
		CapID          int     `json:"cap_id"`
		CapIDNote      string  `json:"cap_id_note"`
		MsgType        int     `json:"msg_type"`
		MsgTypeNote    string  `json:"msg_type_note"`
		PayloadHex     *string `json:"payload_hex"`
		PayloadLength  *int64  `json:"payload_length"`
		PayloadPattern *string `json:"payload_pattern"`
	} `json:"fields"`
	Expect struct {
		DecodeOK    bool    `json:"decode_ok"`
		EncodeOK    bool    `json:"encode_ok"`
		HeaderHex   string  `json:"header_hex"`
		FullHex     *string `json:"full_hex"`
		ErrorCode   *string `json:"error_code"`
		ErrorReason *string `json:"error_reason"`
	} `json:"expect"`
}

// namedErrorCodes maps the JSON "error_code" strings to the ErrorCode
// constants exported by envelope.go (M1a), so a decode failure can be checked
// against the exact PROTOCOL.md §10 code, not just "some error happened".
var namedErrorCodes = map[string]ErrorCode{
	"PROTOCOL_VIOLATION": CodeProtocolViolation,
	"MESSAGE_TOO_LARGE":  CodeMessageTooLarge,
	"UNKNOWN_VERSION":    CodeUnknownVersion,
}

func loadEnvelopeVectors(t *testing.T) envelopeVectorFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/envelope_vectors.json")
	if err != nil {
		t.Fatalf("reading testdata/envelope_vectors.json: %v", err)
	}
	var vf envelopeVectorFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		t.Fatalf("parsing testdata/envelope_vectors.json: %v", err)
	}
	if vf.EnvelopeHeaderSize != EnvelopeHeaderSize {
		t.Fatalf("testdata assumes header size %d, code defines %d -- update the vectors, not this check",
			vf.EnvelopeHeaderSize, EnvelopeHeaderSize)
	}
	if vf.MaxMessageSizeBytes != MaxMessageSize {
		t.Fatalf("testdata assumes MaxMessageSize %d, code defines %d -- update the vectors, not this check",
			vf.MaxMessageSizeBytes, MaxMessageSize)
	}
	return vf
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("invalid hex %q: %v", s, err)
	}
	return b
}

// diffBytes reports a byte-level difference between got and want, windowed
// around the first mismatch so the multi-megabyte boundary case doesn't dump
// megabytes of hex into the failure output -- the whole point of this track
// is that a human can read the failure and tell what changed.
func diffBytes(t *testing.T, label string, got, want []byte) {
	t.Helper()
	if bytes.Equal(got, want) {
		return
	}
	n := len(got)
	if len(want) < n {
		n = len(want)
	}
	for i := 0; i < n; i++ {
		if got[i] != want[i] {
			lo := i - 4
			if lo < 0 {
				lo = 0
			}
			gHi := i + 5
			if gHi > len(got) {
				gHi = len(got)
			}
			wHi := i + 5
			if wHi > len(want) {
				wHi = len(want)
			}
			t.Errorf("%s: byte mismatch at offset %d (got %d bytes, want %d bytes)\n  got:  ...%s...\n  want: ...%s...",
				label, i, len(got), len(want), hex.EncodeToString(got[lo:gHi]), hex.EncodeToString(want[lo:wHi]))
			return
		}
	}
	t.Errorf("%s: length mismatch, common prefix (%d bytes) matches: got %d bytes, want %d bytes", label, n, len(got), len(want))
}

func TestEnvelopeGoldenVectors(t *testing.T) {
	vf := loadEnvelopeVectors(t)
	for _, c := range vf.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			t.Logf("pins: %s", c.ProtocolClause)
			if c.Fields.PayloadPattern != nil {
				runBoundaryVector(t, c)
				return
			}
			runFixedVector(t, c)
		})
	}
}

func runFixedVector(t *testing.T, c envelopeCase) {
	var payload []byte
	if c.Fields.PayloadHex != nil {
		payload = mustHex(t, *c.Fields.PayloadHex)
	}
	env := Envelope{
		Version: byte(c.Fields.Ver),
		CapID:   byte(c.Fields.CapID),
		MsgType: uint16(c.Fields.MsgType),
		Payload: payload,
	}

	if c.Expect.EncodeOK {
		if c.Expect.FullHex == nil {
			t.Fatalf("case %s: encode_ok true but full_hex missing", c.Name)
		}
		want := mustHex(t, *c.Expect.FullHex)
		var buf bytes.Buffer
		if err := EncodeEnvelope(&buf, env); err != nil {
			t.Errorf("EncodeEnvelope: unexpected error: %v", err)
		} else {
			diffBytes(t, "encode", buf.Bytes(), want)
		}
	}

	// Decode input: full_hex when present (success cases, and the
	// unknown-version case which still carries a payload), else header_hex
	// alone (the length-exceeds-max case, which must be rejected off the
	// header before any payload is read).
	decodeHex := c.Expect.HeaderHex
	if c.Expect.FullHex != nil {
		decodeHex = *c.Expect.FullHex
	}
	in := mustHex(t, decodeHex)
	got, err := DecodeEnvelope(bytes.NewReader(in))

	if c.Expect.DecodeOK {
		if err != nil {
			t.Errorf("DecodeEnvelope: unexpected error: %v", err)
			return
		}
		if got.Version != env.Version {
			t.Errorf("decode: Version = %d, want %d", got.Version, env.Version)
		}
		if got.CapID != env.CapID {
			t.Errorf("decode: CapID = %d, want %d", got.CapID, env.CapID)
		}
		if got.MsgType != env.MsgType {
			t.Errorf("decode: MsgType = %d, want %d", got.MsgType, env.MsgType)
		}
		diffBytes(t, "decode payload", got.Payload, payload)
		return
	}

	if err == nil {
		reason := ""
		if c.Expect.ErrorReason != nil {
			reason = *c.Expect.ErrorReason
		}
		t.Errorf("DecodeEnvelope: expected rejection (%s), got success: %+v", reason, got)
		return
	}
	if c.Expect.ErrorCode != nil {
		want, known := namedErrorCodes[*c.Expect.ErrorCode]
		if !known {
			t.Fatalf("case %s: testdata names unknown error_code %q", c.Name, *c.Expect.ErrorCode)
		}
		if code, ok := ErrorCodeOf(err); !ok || code != want {
			t.Errorf("DecodeEnvelope: error code = %v (ok=%v), want %v (%s)\n  err: %v",
				code, ok, want, *c.Expect.ErrorCode, err)
		}
	}
}

// runBoundaryVector handles the exactly-MaxMessageSize case, whose payload is
// generated from a pattern rather than stored literally (16 MiB of hex would
// make the manifest unreviewable). Only the 8-byte header is a literal golden
// value here; the payload is checked for round-trip fidelity, not compared to
// a hand-written vector.
func runBoundaryVector(t *testing.T, c envelopeCase) {
	if c.Fields.PayloadLength == nil {
		t.Fatalf("case %s: payload_pattern set without payload_length", c.Name)
	}
	n := *c.Fields.PayloadLength
	payload := make([]byte, n)
	for i := range payload {
		payload[i] = byte(i % 256)
	}
	env := Envelope{
		Version: byte(c.Fields.Ver),
		CapID:   byte(c.Fields.CapID),
		MsgType: uint16(c.Fields.MsgType),
		Payload: payload,
	}

	wantHeader := mustHex(t, c.Expect.HeaderHex)
	var buf bytes.Buffer
	if err := EncodeEnvelope(&buf, env); err != nil {
		t.Fatalf("EncodeEnvelope: unexpected error: %v", err)
	}
	got := buf.Bytes()
	if len(got) < EnvelopeHeaderSize {
		t.Fatalf("encoded output shorter than header size: %d bytes", len(got))
	}
	diffBytes(t, "encode header", got[:EnvelopeHeaderSize], wantHeader)
	diffBytes(t, "encode payload", got[EnvelopeHeaderSize:], payload)

	decoded, err := DecodeEnvelope(bytes.NewReader(got))
	if c.Expect.DecodeOK {
		if err != nil {
			t.Errorf("DecodeEnvelope: unexpected error: %v", err)
			return
		}
		diffBytes(t, "decode payload", decoded.Payload, payload)
	} else if err == nil {
		t.Errorf("DecodeEnvelope: expected rejection, got success")
	}
}
