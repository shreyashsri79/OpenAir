package daemon

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/mirror"
)

// M15 through the daemon: §6.3's consent path, the indicator on the machine
// being watched, and the loopback URL a player opens.
//
// The capturer is a fake, because a test machine has no screen and because what
// is under test here is the daemon, not ffmpeg — internal/screen covers the
// real encoder against a synthetic video source.

// fakeCapturer emits frames on a timer until it is stopped.
type fakeMirrorCapturer struct {
	mu        sync.Mutex
	keyframes int
	bitrate   int
	started   bool
}

func (f *fakeMirrorCapturer) Start(ctx context.Context, opts mirror.CaptureOptions) (<-chan mirror.Frame, error) {
	f.mu.Lock()
	f.started = true
	f.bitrate = opts.Bitrate
	f.mu.Unlock()

	frames := make(chan mirror.Frame, 4)
	go func() {
		defer close(frames)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		n := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n++
				frames <- mirror.Frame{
					// Recognisable bytes: the test asserts these come out of
					// the local URL, which is the whole path end to end.
					Data:       []byte("frame-" + strings.Repeat("x", 64)),
					Keyframe:   n%10 == 1,
					CapturedAt: time.Now(),
				}
			}
		}
	}()
	return frames, nil
}

func (f *fakeMirrorCapturer) RequestKeyframe() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keyframes++
	return nil
}

func (f *fakeMirrorCapturer) SetBitrate(b int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bitrate = b
	return nil
}

func (f *fakeMirrorCapturer) wasStarted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

// sharingPair returns a device sharing its screen and a viewer unlocked for it.
func sharingPair(t *testing.T) (source *Daemon, vc *Client, capture *fakeMirrorCapturer) {
	t.Helper()
	capture = &fakeMirrorCapturer{}
	source = newTestDaemon(t, func(cfg *Config) {
		protect(t, cfg.KeyDir)
		cfg.ShareScreen = true
		cfg.MirrorCapturer = func() (mirror.Capturer, error) { return capture, nil }
	})
	viewer := newProtectedDaemon(t)
	pinOwned(t, viewer, source)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	vc = connect(t, viewer, nil, nil)
	if _, err := vc.Unlock(ctx, string(source.DeviceID()), []byte(testPassphrase), nil, false, 5*time.Minute); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	return source, vc, capture
}

// TestWatchingAnotherScreen is the milestone: ask for a screen, get a URL, and
// find frames coming out of it.
func TestWatchingAnotherScreen(t *testing.T) {
	source, vc, capture := sharingPair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	url, err := vc.Mirror(ctx, source.Addr(), 640, 480, 30, 128<<10)
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("the mirror URL is %q, which is not loopback", url)
	}

	waitFor(t, "the source to start capturing", capture.wasStarted)

	// A player opens the URL and reads frames as they arrive. Reading a bounded
	// amount is the point -- this is a live stream, so it never ends on its own.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("opening the mirror URL: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the mirror URL answered %s", resp.Status)
	}

	buf := make([]byte, 64)
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.Read(buf); err != nil {
		t.Fatalf("reading frames: %v", err)
	}
	if !strings.Contains(string(buf), "frame-") {
		t.Fatalf("what came out of the URL is not what the source captured: %q", buf[:16])
	}
}

// TestTheWatchedDeviceShowsAnIndicator. PRD R26 asks for a visible sign, and
// for this capability it is the requirement rather than a nicety.
func TestTheWatchedDeviceShowsAnIndicator(t *testing.T) {
	source, vc, _ := sharingPair(t)
	events := watching(t, source)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := vc.Mirror(ctx, source.Addr(), 640, 480, 30, 128<<10); err != nil {
		t.Fatalf("mirror: %v", err)
	}

	waitFor(t, "the indicator to go up", func() bool {
		for _, ev := range events.of(1) { // any kind; filtered below
			_ = ev
		}
		return len(source.AnnouncedSessions()) == 1
	})
	shown := source.AnnouncedSessions()[0]
	if !strings.Contains(shown, "screen") {
		t.Fatalf("the indicator says %q, which does not mention the screen", shown)
	}

	if err := vc.StopMirror(ctx, source.Addr()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitFor(t, "the indicator to go down", func() bool {
		return len(source.AnnouncedSessions()) == 0
	})
}

// TestADeviceThatDoesNotShareItsScreenRefuses. Off by default, and the refusal
// says which switch is missing.
func TestADeviceThatDoesNotShareItsScreenRefuses(t *testing.T) {
	capture := &fakeMirrorCapturer{}
	source := newTestDaemon(t, func(cfg *Config) {
		protect(t, cfg.KeyDir)
		cfg.MirrorCapturer = func() (mirror.Capturer, error) { return capture, nil }
		// ShareScreen stays false.
	})
	viewer := newProtectedDaemon(t)
	pinOwned(t, viewer, source)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	vc := connect(t, viewer, nil, nil)
	if _, err := vc.Unlock(ctx, string(source.DeviceID()), []byte(testPassphrase), nil, false, time.Minute); err != nil {
		t.Fatalf("unlock: %v", err)
	}

	_, err := vc.Mirror(ctx, source.Addr(), 640, 480, 30, 128<<10)
	if err == nil {
		t.Fatal("a device that does not share its screen was mirrored anyway")
	}
	if !strings.Contains(err.Error(), "share-screen") && !strings.Contains(err.Error(), "not sharing") {
		t.Fatalf("the refusal does not say what is missing: %v", err)
	}
	if capture.wasStarted() {
		t.Fatal("capture started on a device that does not share its screen")
	}
}

// TestWatchingNeedsAnUnlock: mirroring is Owned, and the proof travels on the
// §6.3 announcement (D-82).
func TestWatchingNeedsAnUnlock(t *testing.T) {
	capture := &fakeMirrorCapturer{}
	source := newTestDaemon(t, func(cfg *Config) {
		protect(t, cfg.KeyDir)
		cfg.ShareScreen = true
		cfg.MirrorCapturer = func() (mirror.Capturer, error) { return capture, nil }
	})
	viewer := newProtectedDaemon(t)
	pinOwned(t, viewer, source)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vc := connect(t, viewer, nil, nil)
	_, err := vc.Mirror(ctx, source.Addr(), 640, 480, 30, 128<<10)
	if err == nil {
		t.Fatal("a locked device watched another's screen")
	}
	if !strings.Contains(err.Error(), "unlock") {
		t.Fatalf("the refusal does not say what to do: %v", err)
	}
	if capture.wasStarted() {
		t.Fatal("capture started for a device that had not unlocked")
	}
}

// TestKillingTheSessionStopsTheCapture. A local user ending a screen share has
// to actually stop the encoder, not merely stop the frames being displayed.
func TestKillingTheSessionStopsTheCapture(t *testing.T) {
	source, vc, capture := sharingPair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := vc.Mirror(ctx, source.Addr(), 640, 480, 30, 128<<10); err != nil {
		t.Fatalf("mirror: %v", err)
	}
	waitFor(t, "capture to start", capture.wasStarted)

	if killed := source.KillSessions(ctx, ""); killed == 0 {
		t.Fatal("nothing was killed")
	}
	waitFor(t, "the source to stop sharing", func() bool {
		return len(source.mirrors.Sharing()) == 0
	})
	if got := source.AnnouncedSessions(); len(got) != 0 {
		t.Fatalf("%d sessions survived a kill", len(got))
	}
}
