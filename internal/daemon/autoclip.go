package daemon

import (
	"context"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/clipboard"
)

// Automatic clipboard sync, M13 (PRD R19, §9).
//
// Nothing new on the wire: this is M5's push, sent by a watcher rather than by
// a person typing a command. What the daemon adds is when to send it, who to
// send it to, and not sending back what just arrived.
//
// Opt-in, because it is the feature most able to surprise someone: a device
// that mirrors every copy is a device that mirrors a password manager's
// clipboard to whatever else is paired. `--auto-clipboard` is the whole
// consent, and the manual path stays the guaranteed one (PRD R19, K3).

// startAutoClipboard runs the watcher until ctx ends, if it is enabled.
func (d *Daemon) startAutoClipboard(ctx context.Context) {
	if !d.cfg.AutoClipboard {
		return
	}
	if !clipboard.HaveOS() {
		// Said once, at start, rather than every poll: a headless machine has
		// no clipboard to watch and never will within this process's life.
		d.cfg.Logf("automatic clipboard sync is on, but this device has no system clipboard to read")
		return
	}

	watcher := clipboard.NewWatcher(clipboard.WatchConfig{
		State:    d.clipState,
		Interval: d.cfg.ClipboardInterval,
		OnChange: d.pushClipboard,
		Logf:     d.cfg.Logf,
	})
	d.cfg.Logf("automatic clipboard sync on, polling every %s", watcher.Interval())
	go watcher.Run(ctx)
}

// pushClipboard sends locally copied content to every connected device.
//
// Connected, not every paired device: dialling every machine a person owns
// because they copied a word would turn a clipboard into a connection storm,
// and a device that is not connected is not one they are about to paste into.
func (d *Daemon) pushClipboard(ctx context.Context, content string) {
	sessions := d.liveSessions()
	if len(sessions) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	for _, sess := range sessions {
		if err := d.clip.PushText(ctx, sess, content); err != nil {
			d.cfg.Logf("clipboard sync to %s: %v", sess.Peer().DeviceID.Fingerprint(), err)
		}
	}
}
