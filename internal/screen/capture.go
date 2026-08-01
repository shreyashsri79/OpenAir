// Package screen captures and encodes this device's screen for PROTOCOL.md
// §14 (M15).
//
// It runs an external encoder and reads Annex-B H.264 from its standard output.
// That is the same shape D-54 chose for the clipboard, for a stronger version
// of the same reason: a cgo binding to libavcodec would put a large C codebase
// with a long CVE history inside a process that holds this device's keys, and
// would have to be built per platform for a feature not every device needs. The
// cost is a runtime dependency the daemon does not control, which is why a
// device with no encoder says so plainly rather than failing at the moment
// somebody tries to watch it (D-85).
//
// # Frames out of a byte stream
//
// An encoder writes a stream, and §14.2 needs frames. H.264's Annex-B format
// makes the split possible without parsing much: a frame is a run of NAL units,
// and an access unit delimiter (`nal_unit_type` 9) marks where one frame's
// units end and the next begin. The encoder is asked to emit them; where it
// will not, the splitter falls back on the first slice NAL of a picture, which
// is the same boundary by a different route.
package screen

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/mirror"
)

// ErrNoEncoder reports that this device cannot encode its screen.
var ErrNoEncoder = errors.New("screen: no encoder found; install ffmpeg to share this screen")

// ErrNoCapture reports that nothing here knows how to capture this display.
var ErrNoCapture = errors.New("screen: no way to capture this display")

const (
	// startCode is Annex-B's three-byte NAL start code. Four-byte start codes
	// begin with an extra zero and are handled by the same scan.
	nalAUD   = 9 // access unit delimiter: the frame boundary
	nalIDR   = 5 // a keyframe's slice
	nalSPS   = 7
	nalPPS   = 8
	nalSlice = 1 // a non-keyframe slice

	// readChunk is how much is pulled from the encoder at a time.
	readChunk = 64 << 10

	// maxFrame bounds one assembled frame, matching the capability's own cap.
	maxFrame = mirror.MaxFrameBytes
)

// Config configures a capturer.
type Config struct {
	// Command overrides the encoder command line entirely. The first element
	// is the program. It must write Annex-B H.264 to standard output.
	//
	// This exists because screen capture is the least portable thing in this
	// project — X11, Wayland's portals, KMS, Windows' DXGI and macOS all differ
	// — and a user with a working ffmpeg incantation should not have to wait
	// for this package to grow their case.
	Command []string

	// Display overrides which display is captured, in whatever form the
	// platform's capture device wants (":0.0", "0", a KMS device).
	Display string

	Logf func(format string, args ...any)
}

// Capturer is a mirror.Capturer backed by an encoder subprocess.
type Capturer struct {
	cfg Config

	mu      sync.Mutex
	cmd     *exec.Cmd
	bitrate int
	// keyframe is closed and replaced to ask for an IDR; the encoder cannot be
	// asked directly, so it is restarted, which produces one.
	restart chan struct{}
}

var _ mirror.Capturer = (*Capturer)(nil)

// New builds a capturer. Nothing is started until Start.
func New(cfg Config) *Capturer {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Capturer{cfg: cfg, restart: make(chan struct{})}
}

// Available reports whether this device can share its screen at all, so a
// daemon can refuse a mirror session with a reason rather than at the moment
// somebody asks.
func Available(cfg Config) error {
	if len(cfg.Command) > 0 {
		if _, err := exec.LookPath(cfg.Command[0]); err != nil {
			return fmt.Errorf("screen: %s: %w", cfg.Command[0], err)
		}
		return nil
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return ErrNoEncoder
	}
	if _, err := captureInput(cfg.Display); err != nil {
		return err
	}
	return nil
}

// Start runs the encoder and returns its frames.
func (c *Capturer) Start(ctx context.Context, opts mirror.CaptureOptions) (<-chan mirror.Frame, error) {
	c.mu.Lock()
	c.bitrate = opts.Bitrate
	c.mu.Unlock()

	frames := make(chan mirror.Frame, 8)
	go func() {
		defer close(frames)
		// The encoder is restarted when a keyframe is demanded or the bitrate
		// changes, because ffmpeg takes neither on a running process. A restart
		// costs a few hundred milliseconds and produces exactly the keyframe
		// the sink asked for, which is the thing it wanted.
		for ctx.Err() == nil {
			if err := c.run(ctx, opts, frames); err != nil && ctx.Err() == nil {
				c.cfg.Logf("screen: capture ended: %v", err)
				return
			}
		}
	}()
	return frames, nil
}

// run is one encoder process.
func (c *Capturer) run(ctx context.Context, opts mirror.CaptureOptions, frames chan<- mirror.Frame) error {
	c.mu.Lock()
	bitrate := c.bitrate
	restart := c.restart
	c.mu.Unlock()

	args, err := c.commandLine(opts, bitrate)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(runCtx, args[0], args[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// The encoder's diagnostics are useful exactly once -- when it refuses to
	// start -- so they are kept and reported with the failure rather than
	// streamed to the log at 30 fps.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("screen: starting %s: %w", args[0], err)
	}
	c.mu.Lock()
	c.cmd = cmd
	c.mu.Unlock()

	go func() {
		select {
		case <-restart:
			cancel()
		case <-runCtx.Done():
		}
	}()

	err = splitFrames(bufio.NewReaderSize(stdout, readChunk), frames, runCtx)
	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	select {
	case <-restart:
		// A deliberate restart: not a failure.
		return nil
	default:
	}
	if err != nil {
		return err
	}
	if waitErr != nil {
		return fmt.Errorf("screen: %s exited: %w\n%s", args[0], waitErr, tail(stderr.String(), 400))
	}
	return nil
}

// RequestKeyframe restarts the encoder, which is how a subprocess encoder is
// asked for an IDR (§14.1).
func (c *Capturer) RequestKeyframe() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	close(c.restart)
	c.restart = make(chan struct{})
	return nil
}

// SetBitrate changes the encoder's target. It takes effect at the next restart,
// which RequestKeyframe or the next loss will cause; forcing one here would
// make a bitrate hint cost a keyframe every time.
func (c *Capturer) SetBitrate(bytesPerSec int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bitrate = bytesPerSec
	return nil
}

// commandLine builds the encoder invocation for this platform.
func (c *Capturer) commandLine(opts mirror.CaptureOptions, bitrate int) ([]string, error) {
	if len(c.cfg.Command) > 0 {
		return c.cfg.Command, nil
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, ErrNoEncoder
	}
	input, err := captureInput(c.cfg.Display)
	if err != nil {
		return nil, err
	}

	fps := opts.FPS
	if fps <= 0 {
		fps = mirror.DefaultFPS
	}
	kbps := bitrate * 8 / 1000
	if kbps <= 0 {
		kbps = mirror.DefaultBitrate * 8 / 1000
	}

	args := []string{"ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, "-framerate", strconv.Itoa(fps))
	args = append(args, input...)
	args = append(args,
		"-c:v", encoder(),
		"-b:v", strconv.Itoa(kbps)+"k",
		// A keyframe every two seconds, so a sink joining late or recovering
		// from loss never waits long even if RequestIDR is lost.
		"-g", strconv.Itoa(fps*2),
		"-pix_fmt", "yuv420p",
		// Latency, not compression: no lookahead, no B-frames, and flush every
		// packet. Without these an encoder happily buffers a second of video.
		"-tune", "zerolatency",
		"-bf", "0",
		"-flush_packets", "1",
	)
	if opts.Width > 0 && opts.Height > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d", opts.Width, opts.Height))
	}
	// Access unit delimiters are what makes the byte stream splittable into
	// frames without a full parser.
	args = append(args, "-bsf:v", "h264_metadata=aud=insert")
	args = append(args, "-f", "h264", "pipe:1")
	return args, nil
}

// encoder picks an H.264 encoder. libx264 is not always built in; openh264 is
// the common Fedora/Debian alternative, and the hardware encoders are faster
// where they exist but fail on machines without the device.
func encoder() string {
	for _, candidate := range []string{"libx264", "libopenh264"} {
		if hasEncoder(candidate) {
			return candidate
		}
	}
	return "libopenh264"
}

var (
	encodersOnce sync.Once
	encoders     string
)

func hasEncoder(name string) bool {
	encodersOnce.Do(func() {
		out, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
		if err == nil {
			encoders = string(out)
		}
	})
	return strings.Contains(encoders, " "+name+" ")
}

// captureInput is the platform's screen-capture input arguments.
func captureInput(display string) ([]string, error) {
	switch runtime.GOOS {
	case "linux":
		// X11, including XWayland. A pure Wayland session needs a portal, which
		// ffmpeg cannot open on its own; those users pass --mirror-command with
		// wf-recorder or a pipewire pipeline, which is why that flag exists.
		d := display
		if d == "" {
			d = ":0.0"
		}
		return []string{"-f", "x11grab", "-i", d}, nil
	case "windows":
		return []string{"-f", "gdigrab", "-i", "desktop"}, nil
	case "darwin":
		d := display
		if d == "" {
			d = "1"
		}
		return []string{"-f", "avfoundation", "-i", d + ":none"}, nil
	}
	return nil, ErrNoCapture
}

// splitFrames turns the encoder's Annex-B byte stream into frames.
func splitFrames(r *bufio.Reader, frames chan<- mirror.Frame, ctx context.Context) error {
	var (
		buf     []byte
		current []byte
		haveIDR bool
	)
	emit := func(captured time.Time) {
		if len(current) == 0 {
			return
		}
		f := mirror.Frame{
			Data:       append([]byte(nil), current...),
			Keyframe:   haveIDR,
			CapturedAt: captured,
		}
		select {
		case frames <- f:
		case <-ctx.Done():
		default:
			// The sender is behind. Dropping here rather than blocking is the
			// same judgement §14.2 makes on the wire: the newest frame is the
			// one worth having.
		}
		current = current[:0]
		haveIDR = false
	}

	chunk := make([]byte, readChunk)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			captured := time.Now()

			for {
				start, next, kind, ok := nextNAL(buf)
				if !ok {
					break
				}
				if kind == nalAUD && len(current) > 0 {
					emit(captured)
				}
				if kind == nalIDR || kind == nalSPS {
					haveIDR = true
				}
				current = append(current, buf[start:next]...)
				buf = buf[next:]

				if len(current) > maxFrame {
					// Something is wrong -- no boundaries at all, or a stream
					// this is not equipped to split. Failing loudly beats
					// growing until the machine notices.
					return errors.New("screen: no frame boundary in an oversized stream")
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				emit(time.Now())
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// nextNAL finds the next complete NAL unit in b, returning its bounds and type.
// It reports ok=false when the buffer does not yet hold a whole one.
func nextNAL(b []byte) (start, end int, kind byte, ok bool) {
	first := bytes.Index(b, []byte{0, 0, 1})
	if first < 0 {
		return 0, 0, 0, false
	}
	headerAt := first + 3
	if headerAt >= len(b) {
		return 0, 0, 0, false
	}
	kind = b[headerAt] & 0x1f

	nextStart := bytes.Index(b[headerAt:], []byte{0, 0, 1})
	if nextStart < 0 {
		return 0, 0, 0, false
	}
	end = headerAt + nextStart
	// A four-byte start code is a three-byte one with a leading zero; give the
	// zero to the next unit rather than the current one.
	if end > 0 && b[end-1] == 0 {
		end--
	}
	return first, end, kind, true
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
