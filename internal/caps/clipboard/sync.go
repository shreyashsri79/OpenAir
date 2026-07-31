package clipboard

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"
)

// Automatic clipboard sync, M13 (PRD R19, §9).
//
// The wire format does not change: auto-sync is manual push, sent by a watcher
// instead of by a person. What is new is deciding *when* to send, and not
// sending back what just arrived.
//
// # Why polling
//
// Neither X11, Wayland nor Windows offers a portable, daemon-friendly "the
// clipboard changed" signal that survives the helper-subprocess arrangement
// D-54 settled on: wl-paste --watch exists but is Wayland-only, and the others
// want an event loop with a window handle. A poll every second is a subprocess
// per second, which is measurable and small, and it is the only mechanism that
// works everywhere the daemon runs. It is also why auto-sync is opt-in and the
// manual path stays the guaranteed one (PRD R19, K3).
//
// # Loop suppression
//
// Two devices watching each other's clipboards is a loop waiting to happen:
// A applies what B sent, its own watcher sees a change, and it sends it back.
// Three rules stop that, and the third is the one that matters under a genuine
// race:
//
//  1. Content that arrived from a peer is recorded before it is written to the
//     OS clipboard, and the watcher never sends content it recognises as the
//     last thing applied.
//  2. The peer's own `origin_tag` (§9) suppresses a push that came from this
//     device in the first place -- already implemented for manual push.
//  3. Simultaneous edits are settled by `origin_ts`, last writer wins. Without
//     it, two devices that each copy something at the same moment swap
//     clipboards and each keeps re-sending; with it, both converge on the later
//     one and stop. It leans on clocks being roughly right, which for choosing
//     between two edits seconds apart is a reasonable thing to lean on, and the
//     failure mode is a clipboard that loses one copy rather than a loop.
const (
	// DefaultPollInterval is how often the watcher looks. A second is under
	// what a person notices between copying here and pasting there, and is one
	// short-lived subprocess per second.
	DefaultPollInterval = time.Second

	// idleBackoff is the interval used after a read fails. A clipboard that
	// cannot be read is usually a display that has gone away, and retrying it
	// every second forever is a subprocess per second for nothing.
	idleBackoff = 15 * time.Second
)

// SyncState is the loop-suppression bookkeeping, shared between the watcher
// and the receive path.
//
// It is separate from Capability because the capability is the wire and this is
// local policy: what the OS clipboard currently holds, where it came from, and
// when.
type SyncState struct {
	mu sync.Mutex

	// applied is the digest of the content most recently written to the OS
	// clipboard from a peer. The watcher will see exactly this on its next
	// poll and must not send it back.
	applied [32]byte

	// localAt is when this device last saw a *local* change, used to settle a
	// simultaneous edit against an incoming origin_ts.
	localAt time.Time
}

// NewSyncState returns an empty state.
func NewSyncState() *SyncState { return &SyncState{} }

// Applied records content received from a peer, before it is written to the
// clipboard.
//
// Before, not after: the write is a subprocess, and a watcher poll landing
// between the write and the bookkeeping would see peer content it had no record
// of and send it straight back.
func (s *SyncState) Applied(content string) {
	sum := sha256.Sum256([]byte(content))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applied = sum
}

// ShouldSend reports whether locally observed content is worth pushing, and
// records it as the current local state when it is.
func (s *SyncState) ShouldSend(content string) bool {
	if content == "" {
		return false
	}
	sum := sha256.Sum256([]byte(content))

	s.mu.Lock()
	defer s.mu.Unlock()
	if sum == s.applied {
		// This is what a peer just sent. Sending it back is the loop.
		return false
	}
	s.applied = sum
	s.localAt = time.Now()
	return true
}

// ShouldApply reports whether incoming content should replace what is here.
//
// originTS is §9's origin_ts. Content older than this device's own last local
// change loses: that is the simultaneous-edit case, and without the rule both
// devices keep overwriting each other.
func (s *SyncState) ShouldApply(content string, originTS time.Time) bool {
	if content == "" {
		return false
	}
	sum := sha256.Sum256([]byte(content))

	s.mu.Lock()
	defer s.mu.Unlock()
	if sum == s.applied {
		// Already what we have -- either we sent it, or we applied it before.
		return false
	}
	if !originTS.IsZero() && !s.localAt.IsZero() && originTS.Before(s.localAt) {
		return false
	}
	return true
}

// Watcher polls the system clipboard and reports changes worth sending.
type Watcher struct {
	state    *SyncState
	interval time.Duration
	read     func(context.Context) (string, error)
	onChange func(ctx context.Context, content string)
	logf     func(format string, args ...any)
}

// WatchConfig configures a Watcher.
type WatchConfig struct {
	State *SyncState

	// Interval is how often to poll. Zero means DefaultPollInterval.
	Interval time.Duration

	// Read overrides the clipboard read, for tests. Nil means ReadOS.
	Read func(context.Context) (string, error)

	// OnChange is called with content this device should push. It is called on
	// the watcher's own goroutine, so a slow implementation delays the next
	// poll rather than piling up.
	OnChange func(ctx context.Context, content string)

	Logf func(format string, args ...any)
}

// NewWatcher builds a watcher. It reads nothing until Run.
func NewWatcher(cfg WatchConfig) *Watcher {
	w := &Watcher{
		state:    cfg.State,
		interval: cfg.Interval,
		read:     cfg.Read,
		onChange: cfg.OnChange,
		logf:     cfg.Logf,
	}
	if w.state == nil {
		w.state = NewSyncState()
	}
	if w.interval <= 0 {
		w.interval = DefaultPollInterval
	}
	if w.read == nil {
		w.read = ReadOS
	}
	if w.logf == nil {
		w.logf = func(string, ...any) {}
	}
	return w
}

// State is the suppression state this watcher shares with the receive path.
func (w *Watcher) State() *SyncState { return w.state }

// Interval is the poll period actually in use, after defaults.
func (w *Watcher) Interval() time.Duration { return w.interval }

// Run polls until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	interval := w.interval
	var failures int

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}

		content, err := w.read(ctx)
		if err != nil {
			failures++
			if failures == 1 {
				// Once, not every second: a display that has gone away
				// produces this until it comes back.
				w.logf("clipboard sync paused: %v", err)
			}
			interval = idleBackoff
			continue
		}
		if failures > 0 {
			w.logf("clipboard sync resumed")
			failures = 0
			interval = w.interval
		}
		if !w.state.ShouldSend(content) {
			continue
		}
		if w.onChange != nil {
			w.onChange(ctx, content)
		}
	}
}
