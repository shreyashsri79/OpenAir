package mirror

import (
	"context"
	"sort"
	"time"

	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// The sink half of §14: the device doing the watching.
//
// It asks for a session, receives frames on their own streams, and sends back
// the two things a source can act on — a keyframe request when it has lost its
// footing, and a bitrate hint when the path cannot carry what is being sent.
// D-84 measured what happens when the second one is ignored: on a relayed path
// the source abandons more than half its frames, and the viewer sees a
// slideshow while the source believes it is sending 30 fps.

// Watch asks a peer to start mirroring, and keeps the session until Stop.
func (c *Capability) Watch(ctx context.Context, sess session.Session, opts CaptureOptions) error {
	if opts.FPS <= 0 {
		opts.FPS = DefaultFPS
	}
	if opts.Bitrate <= 0 {
		opts.Bitrate = DefaultBitrate
	}

	peer := sess.Peer().DeviceID
	c.mu.Lock()
	if _, already := c.watching[peer]; already {
		c.mu.Unlock()
		return nil
	}
	watchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	w := &watchSession{peer: peer, sess: sess, cancel: cancel, opts: opts}
	c.watching[peer] = w
	c.mu.Unlock()

	err := sess.Send(ctx, CapID, MsgStart, &openairv1.MirrorStart{
		Width:      uint32(opts.Width),
		Height:     uint32(opts.Height),
		Fps:        uint32(opts.FPS),
		Codec:      "h264",
		MaxBitrate: uint32(opts.Bitrate),
		DisplayId:  uint32(opts.DisplayID),
	})
	if err != nil {
		cancel()
		c.mu.Lock()
		delete(c.watching, peer)
		c.mu.Unlock()
		return err
	}

	go c.feedback(watchCtx, w)
	return nil
}

// StopWatching ends a session this device started (§14.1's MirrorStop).
func (c *Capability) StopWatching(ctx context.Context, sess session.Session) error {
	peer := sess.Peer().DeviceID

	c.mu.Lock()
	w := c.watching[peer]
	delete(c.watching, peer)
	c.mu.Unlock()
	if w == nil {
		return nil
	}
	w.cancel()

	return sess.Send(ctx, CapID, MsgStop, &openairv1.MirrorStop{})
}

// Watching reports which peers' screens this device is showing.
func (c *Capability) Watching() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.watching))
	for peer := range c.watching {
		out = append(out, string(peer))
	}
	return out
}

// feedback is the sink's half of the control loop: stats at 1 Hz (§14.1), a
// keyframe request when frames were lost, and a bitrate hint when latency says
// the path cannot carry what is arriving.
func (c *Capability) feedback(ctx context.Context, w *watchSession) {
	ticker := time.NewTicker(statsInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats, needIDR, p95 := w.take()

			if err := w.sess.Send(ctx, CapID, MsgStats, stats); err != nil {
				return
			}
			if needIDR {
				// One request per interval, not one per lost frame: a request
				// per loss is a storm exactly when the path is worst.
				_ = w.sess.Send(ctx, CapID, MsgRequestIDR, &openairv1.RequestIdr{})
			}
			if hint := w.suggestBitrate(p95); hint > 0 {
				_ = w.sess.Send(ctx, CapID, MsgBitrateHint,
					&openairv1.BitrateHint{BytesPerSec: uint32(hint)})
			}
		}
	}
}

// take collects and resets the interval's counters.
func (w *watchSession) take() (*openairv1.MirrorStats, bool, time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()

	sorted := append([]time.Duration(nil), w.latency...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var p95 time.Duration
	if n := len(sorted); n > 0 {
		p95 = sorted[(n*95)/100]
		if idx := (n * 95) / 100; idx >= n {
			p95 = sorted[n-1]
		}
	}

	var jitter time.Duration
	if n := len(sorted); n > 1 {
		jitter = sorted[n-1] - sorted[0]
	}

	stats := &openairv1.MirrorStats{
		FramesDecoded: w.decoded,
		FramesDropped: w.dropped,
		DecodeMsP95:   uint32(p95.Milliseconds()),
		JitterMs:      uint32(jitter.Milliseconds()),
	}
	needIDR := w.needsIDR
	w.decoded, w.dropped = 0, 0
	w.latency = w.latency[:0]
	return stats, needIDR, p95
}

// suggestBitrate is the sink's adaptation. Latency growing past a couple of
// frame intervals means the path is not carrying what is being sent, and the
// only lever a sink has is to ask for less (§14.1, D-84).
func (w *watchSession) suggestBitrate(p95 time.Duration) int {
	w.mu.Lock()
	defer w.mu.Unlock()

	if p95 == 0 || w.opts.FPS == 0 {
		return 0
	}
	interval := time.Second / time.Duration(w.opts.FPS)
	switch {
	case p95 > 4*interval && w.opts.Bitrate > 128<<10:
		// Well behind: halve, floored at something still watchable.
		w.opts.Bitrate /= 2
		return w.opts.Bitrate
	case p95 < interval && w.opts.Bitrate < DefaultBitrate:
		// Comfortably ahead: give some back, slowly, so this does not
		// oscillate against the halving above.
		w.opts.Bitrate += w.opts.Bitrate / 4
		if w.opts.Bitrate > DefaultBitrate {
			w.opts.Bitrate = DefaultBitrate
		}
		return w.opts.Bitrate
	}
	return 0
}
