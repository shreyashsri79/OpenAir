package screen

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/mirror"
)

// The part of capture worth testing without a screen is the splitter: an
// encoder writes a byte stream and §14.2 needs frames, and getting the boundary
// wrong produces video that looks fine in a decoder and is wrong on the wire —
// frames merged together, or a keyframe flag on the wrong one.

// annexB builds a NAL unit with a start code.
func annexB(kind byte, payload string) []byte {
	b := []byte{0, 0, 0, 1, kind}
	return append(b, payload...)
}

// TestFramesAreSplitOnAccessUnitDelimiters is the ordinary case: the encoder
// emits AUDs, and each one starts a frame.
func TestFramesAreSplitOnAccessUnitDelimiters(t *testing.T) {
	var stream []byte
	stream = append(stream, annexB(nalAUD, "")...)
	stream = append(stream, annexB(nalSPS, "sps")...)
	stream = append(stream, annexB(nalIDR, "keyframe-picture")...)
	stream = append(stream, annexB(nalAUD, "")...)
	stream = append(stream, annexB(nalSlice, "delta-one")...)
	stream = append(stream, annexB(nalAUD, "")...)
	stream = append(stream, annexB(nalSlice, "delta-two")...)
	// A trailing unit with no following start code cannot be split yet, which
	// is correct: the frame is not complete until the next one begins.
	stream = append(stream, annexB(nalAUD, "")...)

	frames := make(chan mirror.Frame, 8)
	if err := splitFrames(bufio.NewReader(bytes.NewReader(stream)), frames, context.Background()); err != nil {
		t.Fatalf("split: %v", err)
	}
	close(frames)

	var got []mirror.Frame
	for f := range frames {
		got = append(got, f)
	}
	if len(got) != 3 {
		t.Fatalf("%d frames out of a three-frame stream", len(got))
	}
	if !got[0].Keyframe {
		t.Fatal("the frame carrying an IDR was not marked as a keyframe")
	}
	if got[1].Keyframe || got[2].Keyframe {
		t.Fatal("a delta frame was marked as a keyframe, so a sink would stop asking for one it needs")
	}
	if !bytes.Contains(got[0].Data, []byte("keyframe-picture")) {
		t.Fatal("the first frame does not contain its picture")
	}
	if bytes.Contains(got[0].Data, []byte("delta-one")) {
		t.Fatal("two frames were merged into one")
	}
	for i, f := range got {
		if f.CapturedAt.IsZero() {
			t.Fatalf("frame %d has no capture time", i)
		}
	}
}

// TestASplitAcrossReadsStillFindsTheBoundary. An encoder's output arrives in
// whatever sizes the pipe feels like, including one that lands in the middle of
// a start code.
func TestASplitAcrossReadsStillFindsTheBoundary(t *testing.T) {
	var stream []byte
	stream = append(stream, annexB(nalAUD, "")...)
	stream = append(stream, annexB(nalIDR, "first")...)
	stream = append(stream, annexB(nalAUD, "")...)
	stream = append(stream, annexB(nalSlice, "second")...)
	stream = append(stream, annexB(nalAUD, "")...)

	frames := make(chan mirror.Frame, 8)
	// A reader that hands over three bytes at a time, so every start code is
	// split across at least two reads.
	if err := splitFrames(bufio.NewReaderSize(&trickle{b: stream, n: 3}, 16), frames, context.Background()); err != nil {
		t.Fatalf("split: %v", err)
	}
	close(frames)

	var got []mirror.Frame
	for f := range frames {
		got = append(got, f)
	}
	if len(got) != 2 {
		t.Fatalf("%d frames when the stream was delivered three bytes at a time", len(got))
	}
	if !bytes.Contains(got[0].Data, []byte("first")) || !bytes.Contains(got[1].Data, []byte("second")) {
		t.Fatal("the frames are not the ones that went in")
	}
}

// trickle hands out a few bytes at a time.
type trickle struct {
	b []byte
	n int
}

func (t *trickle) Read(p []byte) (int, error) {
	if len(t.b) == 0 {
		return 0, io.EOF
	}
	n := t.n
	if n > len(t.b) {
		n = len(t.b)
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, t.b[:n])
	t.b = t.b[n:]
	return n, nil
}

// TestARealEncoderProducesSplittableFrames runs the actual pipeline against a
// synthetic video source, so the ffmpeg invocation this package builds is
// exercised rather than only the splitter. It is skipped where ffmpeg is not
// installed, which is also the case the daemon has to survive.
func TestARealEncoderProducesSplittableFrames(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg here; the daemon reports the same condition rather than failing later")
	}

	// lavfi rather than a screen: a test machine has no display, and what is
	// under test is the encoder arguments and the splitting, not X11.
	c := New(Config{
		Command: []string{
			"ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin",
			"-f", "lavfi", "-i", "testsrc=size=320x240:rate=10",
			"-t", "2",
			"-c:v", encoder(),
			"-b:v", "500k", "-g", "20", "-pix_fmt", "yuv420p",
			"-tune", "zerolatency", "-bf", "0", "-flush_packets", "1",
			"-bsf:v", "h264_metadata=aud=insert",
			"-f", "h264", "pipe:1",
		},
		Logf: t.Logf,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	frames, err := c.Start(ctx, mirror.CaptureOptions{FPS: 10, Bitrate: 64 << 10})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	var (
		count     int
		keyframes int
	)
	deadline := time.After(45 * time.Second)
	for count < 10 {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("capture ended after %d frames", count)
			}
			if len(f.Data) == 0 {
				t.Fatal("an empty frame")
			}
			count++
			if f.Keyframe {
				keyframes++
			}
		case <-deadline:
			t.Fatalf("only %d frames in 45s", count)
		}
	}
	cancel()

	if keyframes == 0 {
		t.Fatal("no keyframe in the first ten frames, so a sink would never get a decodable picture")
	}
}
