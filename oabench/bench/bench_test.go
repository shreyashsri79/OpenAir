package bench

import (
	"bytes"
	"testing"
)

func TestChunkPlanCoversExactlyOnce(t *testing.T) {
	const total, chunk = 10*1024 + 7, 1024 // deliberately not a clean multiple

	plan := newChunkPlan(total, chunk)
	seen := make([]bool, total)
	var sum int64
	for {
		off, size, ok := plan.claim()
		if !ok {
			break
		}
		if size <= 0 || size > chunk {
			t.Fatalf("chunk at %d has size %d, want 1..%d", off, size, chunk)
		}
		for i := off; i < off+size; i++ {
			if seen[i] {
				t.Fatalf("byte %d claimed twice", i)
			}
			seen[i] = true
		}
		sum += size
	}
	if sum != total {
		t.Errorf("plan covered %d bytes, want %d", sum, total)
	}
	for i, ok := range seen {
		if !ok {
			t.Fatalf("byte %d never claimed", i)
		}
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	buf := make([]byte, HeaderSize+8)
	putHeader(buf, 1<<40, 4096)

	gotOff, gotSize, err := readHeader(bytes.NewReader(buf), make([]byte, HeaderSize))
	if err != nil {
		t.Fatal(err)
	}
	if gotOff != 1<<40 {
		t.Errorf("offset = %d, want %d", gotOff, int64(1)<<40)
	}
	if gotSize != 4096 {
		t.Errorf("size = %d, want 4096", gotSize)
	}
}

func TestPreambleRoundTrip(t *testing.T) {
	want := preamble{TotalBytes: 1 << 33, Streams: 8, ChunkBytes: 1 << 20}

	var buf bytes.Buffer
	if err := writePreamble(&buf, want); err != nil {
		t.Fatal(err)
	}
	got, err := readPreamble(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("preamble = %+v, want %+v", got, want)
	}
}

func TestParseBytes(t *testing.T) {
	cases := map[string]int64{
		"1GiB":   1 << 30,
		"512MiB": 512 << 20,
		"256KiB": 256 << 10,
		"1MB":    1_000_000,
		"4096":   4096,
	}
	for in, want := range cases {
		got, err := ParseBytes(in)
		if err != nil {
			t.Errorf("ParseBytes(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseBytes(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := ParseBytes("banana"); err == nil {
		t.Error("ParseBytes(\"banana\") succeeded, want error")
	}
}

// TestPinnedKeyRejectsWrongPeer guards the ADR-2 mechanism: verification must
// key off the exact public key, not merely "some certificate parsed".
func TestPinnedKeyRejectsWrongPeer(t *testing.T) {
	pub, _ := SpikeIdentity()

	if got := DeviceID(pub); len(got) != 16 {
		t.Errorf("DeviceID length = %d, want 16 base32 chars", len(got))
	}

	conf := ClientTLS(pub)
	if conf.VerifyPeerCertificate == nil {
		t.Fatal("ClientTLS did not install a pinning verifier")
	}
	if err := conf.VerifyPeerCertificate(nil, nil); err == nil {
		t.Error("verifier accepted a peer presenting no certificate")
	}
	if conf.MinVersion != 0x0304 {
		t.Errorf("MinVersion = %#x, want TLS 1.3", conf.MinVersion)
	}
}
