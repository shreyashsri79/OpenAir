package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/mirror"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/ipc"
	"github.com/shreyashsri79/openair/internal/screen"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// M15 through the daemon (§14, PRD R24, R26).
//
// The watching side reassembles frames and publishes them at a loopback URL,
// which is M11's arrangement applied to a live stream: every media player
// already decodes H.264 from a URL, so the alternative — building a video
// window — is work this project would be doing for the second time and worse.
// `openair mirror laptop --with mpv` is the whole viewer.
//
// The shared side is deliberately harder to turn on than anything else here.
// Sharing a screen needs `--share-screen`, an Owned peer, and a live §6.3
// announcement, and it raises the same indicator input does. PRD R26 asks for a
// visible sign; this is the capability where that matters most.

// mirrorIdle ends a session nobody is reading frames out of.
const mirrorIdle = 2 * time.Minute

// mirrorView is one screen this device is watching, published locally.
type mirrorView struct {
	peer  identity.DeviceID
	token string

	mu      sync.Mutex
	viewers []chan []byte
	last    []byte // the most recent keyframe, so a viewer joining mid-session sees something
	used    time.Time
}

// onMirror answers a shell's mirror request.
func (d *Daemon) onMirror(ctx context.Context, c *client, payload []byte) {
	var req openairv1.DaemonMirrorRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	reply := &openairv1.DaemonMirrorResponse{RequestId: req.GetRequestId()}

	url, err := d.mirror(ctx, &req)
	if err != nil {
		reply.Error = err.Error()
	} else {
		reply.Url = url
	}
	_ = c.peer.Send(ipc.MsgMirrorResponse, reply)
}

// mirror starts or stops watching a device's screen.
func (d *Daemon) mirror(ctx context.Context, req *openairv1.DaemonMirrorRequest) (string, error) {
	sess, err := d.sessionTo(ctx, req.GetDevice())
	if err != nil {
		return "", err
	}
	peer := sess.Peer().DeviceID

	if req.GetStop() {
		d.mu.Lock()
		view := d.views[peer]
		delete(d.views, peer)
		d.mu.Unlock()
		if view == nil {
			return "", fmt.Errorf("not watching %s", peer.Fingerprint())
		}
		view.close()
		if err := d.mirrors.StopWatching(ctx, sess); err != nil {
			return "", err
		}
		d.endMirrorAnnouncement(ctx, sess, peer)
		d.cfg.Logf("stopped watching %s", peer.Fingerprint())
		return "", nil
	}

	d.mu.Lock()
	existing := d.views[peer]
	d.mu.Unlock()
	if existing != nil {
		srv, err := d.streamer()
		if err != nil {
			return "", err
		}
		return srv.url(existing.token), nil
	}

	// §6.3 again: mirroring is announced before it starts, the announcement
	// carries the Owned proof, and the far end acknowledges before anything
	// begins (D-82, D-83). Input and mirror share this path exactly.
	id, killed, err := d.announce(ctx, sess, []byte{mirror.CapID}, "screen")
	if err != nil {
		return "", err
	}

	srv, err := d.streamer()
	if err != nil {
		return "", err
	}
	token, err := streamToken()
	if err != nil {
		return "", err
	}
	view := &mirrorView{peer: peer, token: token, used: time.Now()}

	d.mu.Lock()
	if d.views == nil {
		d.views = map[identity.DeviceID]*mirrorView{}
	}
	d.views[peer] = view
	if d.mirrorSessions == nil {
		d.mirrorSessions = map[identity.DeviceID]string{}
	}
	d.mirrorSessions[peer] = id
	d.mu.Unlock()

	srv.publishLive(token, view)

	opts := mirror.CaptureOptions{
		Width:   int(req.GetWidth()),
		Height:  int(req.GetHeight()),
		FPS:     int(req.GetFps()),
		Bitrate: int(req.GetBitrate()),
	}
	if err := d.mirrors.Watch(ctx, sess, opts); err != nil {
		d.mu.Lock()
		delete(d.views, peer)
		d.mu.Unlock()
		view.close()
		return "", mirrorError(err)
	}

	go func() {
		<-killed
		d.cfg.Logf("%s stopped sharing its screen", peer.Fingerprint())
		d.mu.Lock()
		v := d.views[peer]
		delete(d.views, peer)
		d.mu.Unlock()
		if v != nil {
			v.close()
		}
	}()

	d.cfg.Logf("watching %s at %s", peer.Fingerprint(), srv.url(token))
	return srv.url(token), nil
}

// endMirrorAnnouncement withdraws the §6.3 session a mirror opened.
func (d *Daemon) endMirrorAnnouncement(ctx context.Context, sess interface {
	Peer() identity.Peer
}, peer identity.DeviceID) {
	d.mu.Lock()
	id := d.mirrorSessions[peer]
	delete(d.mirrorSessions, peer)
	d.mu.Unlock()
	if id == "" {
		return
	}
	if s, ok := d.sessionFor(peer); ok {
		d.endAnnounced(ctx, s, id, "finished")
	}
}

// onMirrorFrame is called for each frame that arrives from a peer.
func (d *Daemon) onMirrorFrame(peer identity.DeviceID, f mirror.Frame) {
	d.mu.Lock()
	view := d.views[peer]
	d.mu.Unlock()
	if view == nil {
		return
	}
	view.publish(f)
}

// onMirrorStart and onMirrorStop are the indicator on the *shared* side: this
// device's screen is being watched, and someone in front of it should be able
// to tell (PRD R26).
func (d *Daemon) onMirrorStart(peer identity.DeviceID, opts mirror.CaptureOptions) {
	d.cfg.Logf("** %s is watching this screen (%dx%d, %d fps)",
		peer.Fingerprint(), opts.Width, opts.Height, opts.FPS)
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_ANNOUNCED,
		DeviceId: string(peer),
		Text:     fmt.Sprintf("%s is watching this screen", peer.Fingerprint()),
	})
	d.logAuth("mirror-start", peer, "screen shared")
}

func (d *Daemon) onMirrorStop(peer identity.DeviceID) {
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_ENDED,
		DeviceId: string(peer),
		Text:     "stopped watching this screen",
	})
	d.logAuth("mirror-stop", peer, "")
}

// mirrorAllowed is the source-side gate: this device must have opted in, and
// the peer must have a live announcement naming mirror (D-82).
func (d *Daemon) mirrorAllowed(peer identity.DeviceID) bool {
	if !d.cfg.ShareScreen {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range d.announcements {
		if a.peer.DeviceID != peer || !a.owned {
			continue
		}
		for _, c := range a.capIDs {
			if c == mirror.CapID {
				a.touch()
				return true
			}
		}
	}
	return false
}

// newCapturer builds the screen capturer for one session.
func (d *Daemon) newCapturer() (mirror.Capturer, error) {
	cfg := screen.Config{
		Command: d.cfg.MirrorCommand,
		Display: d.cfg.MirrorDisplay,
		Logf:    d.cfg.Logf,
	}
	if err := screen.Available(cfg); err != nil {
		return nil, err
	}
	return screen.New(cfg), nil
}

// publish hands a frame to every viewer reading this device's local URL.
func (v *mirrorView) publish(f mirror.Frame) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.used = time.Now()
	if f.Keyframe {
		// Kept so a player that connects mid-session has something decodable
		// to start from rather than a stream that begins in the middle of a
		// group of pictures.
		v.last = f.Data
	}
	for _, ch := range v.viewers {
		select {
		case ch <- f.Data:
		default:
			// This viewer is behind. Realtime video does not wait for it.
		}
	}
}

// subscribe adds a viewer, returning its channel and the keyframe to start on.
func (v *mirrorView) subscribe() (<-chan []byte, []byte, func()) {
	ch := make(chan []byte, 32)

	v.mu.Lock()
	v.viewers = append(v.viewers, ch)
	first := v.last
	v.mu.Unlock()

	return ch, first, func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		for i, other := range v.viewers {
			if other == ch {
				v.viewers = append(v.viewers[:i], v.viewers[i+1:]...)
				break
			}
		}
	}
}

func (v *mirrorView) close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, ch := range v.viewers {
		close(ch)
	}
	v.viewers = nil
}

// serveLive writes a mirror session's frames to an HTTP response for as long as
// the player keeps reading.
func (v *mirrorView) serveLive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "video/h264")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	frames, first, unsubscribe := v.subscribe()
	defer unsubscribe()

	if len(first) > 0 {
		if _, err := w.Write(first); err != nil {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case data, ok := <-frames:
			if !ok {
				return
			}
			if _, err := w.Write(data); err != nil {
				return
			}
			// Without a flush per frame the transport buffers, and a viewer
			// watching "live" is a second behind for no reason.
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// mirrorError turns the refusals a user can act on into advice.
func mirrorError(err error) error {
	switch {
	case strings.Contains(err.Error(), "not sharing its screen"):
		return fmt.Errorf("%w\n\nthat device is not sharing its screen: start its daemon with "+
			"`openaird --share-screen`, which is off by default", err)
	case strings.Contains(err.Error(), "unlock"):
		return fmt.Errorf("%w\n\nwatching a screen is Owned-level: run `openair unlock` for that device first", err)
	}
	return err
}

var errNoMirror = errors.New("mirror: not watching that device")
