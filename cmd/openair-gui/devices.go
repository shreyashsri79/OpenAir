package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

func displayName(d *openairv1.DaemonDevice) string {
	if n := d.GetDisplayName(); n != "" {
		return n
	}
	return "(unnamed)"
}

// stateOf is the device list's second line: where this device stands right now.
func stateOf(d *openairv1.DaemonDevice) string {
	switch {
	case d.GetSessionOpen():
		return "connected"
	case d.GetPaired():
		return "paired"
	default:
		return "seen, not paired"
	}
}

// accessOf renders two facts that point in opposite directions, and so are not
// merged into one word. The trust level is what that device may do here; the
// unlock is what this device may do there (D-30).
func accessOf(d *openairv1.DaemonDevice) string {
	if !d.GetPaired() {
		return "not paired"
	}
	level := "trusted"
	if d.GetLevel() == openairv1.TrustLevel_TRUST_LEVEL_OWNED {
		level = "owned"
	}
	switch until := d.GetUnlockedUntilUnixMs(); {
	case until < 0:
		return level + ", unlocked (never expires)"
	case until == 0:
		return level + ", locked"
	default:
		return level + ", unlocked for " + time.Until(time.UnixMilli(until)).Round(time.Minute).String()
	}
}

// current returns the selected device, or nil.
func (u *ui) current() *openairv1.DaemonDevice {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.selected < 0 || u.selected >= len(u.devices) {
		return nil
	}
	return u.devices[u.selected]
}

// showDetail rebuilds the right-hand panel for the selected device.
//
// It is rebuilt rather than updated in place because what a device offers
// depends on what it is: an unpaired device gets one button, and an owned,
// unlocked one gets the whole set. Hiding rows that cannot work is clearer than
// showing buttons that answer with a refusal.
func (u *ui) showDetail() {
	d := u.current()
	fyne.Do(func() {
		if d == nil {
			u.detail.Objects = []fyne.CanvasObject{widget.NewLabel("Select a device.")}
			u.detail.Refresh()
			return
		}
		u.detail.Objects = u.detailFor(d)
		u.detail.Refresh()
	})
}

func (u *ui) detailFor(d *openairv1.DaemonDevice) []fyne.CanvasObject {
	id := d.GetDeviceId()

	head := widget.NewLabelWithStyle(displayName(d), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	meta := widget.NewLabel(fmt.Sprintf("%s\n%s · %s\n%s",
		fingerprintOf(d), d.GetPlatform(), accessOf(d), strings.Join(d.GetAddrs(), "  ")))
	meta.Wrapping = fyne.TextWrapWord

	objs := []fyne.CanvasObject{head, meta, widget.NewSeparator()}

	if !d.GetPaired() {
		objs = append(objs,
			widget.NewLabel("This device is not paired. Files, clipboard and browsing are all refused until it is."),
			widget.NewButtonWithIcon("Pair with this device", theme.ContentAddIcon(), u.showPair),
		)
		return objs
	}

	// ── Send files ────────────────────────────────────────────────────────
	queue := widget.NewLabel(u.queueText())
	queue.Wrapping = fyne.TextWrapWord
	u.queueLabel = queue

	add := widget.NewButtonWithIcon("Add file…", theme.FolderOpenIcon(), func() {
		dialog.ShowFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			r.Close()
			u.enqueue(path)
		}, u.win)
	})
	clear := widget.NewButton("Clear", func() {
		u.mu.Lock()
		u.queued = nil
		u.mu.Unlock()
		u.refreshQueue()
	})
	send := widget.NewButtonWithIcon("Send", theme.MailSendIcon(), func() { go u.sendQueued(id) })
	send.Importance = widget.HighImportance

	objs = append(objs,
		widget.NewLabelWithStyle("Send files", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel("Add files, or drop them anywhere on this window."),
		queue,
		container.NewHBox(add, clear, send),
		widget.NewSeparator(),
	)

	// ── Clipboard and notifications ───────────────────────────────────────
	objs = append(objs,
		widget.NewLabelWithStyle("Clipboard", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(
			widget.NewButtonWithIcon("Push my clipboard", theme.ContentCopyIcon(), func() {
				go u.pushClipboard(id, u.app.Clipboard().Content())
			}),
			widget.NewButton("Push text…", func() { u.askText(id) }),
		),
		widget.NewLabelWithStyle("Notify", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewButtonWithIcon("Send a notification…", theme.InfoIcon(), func() { u.askNotify(id) }),
		widget.NewSeparator(),
	)

	// ── Owned access ──────────────────────────────────────────────────────
	// Browsing, streaming and mirroring are Owned-level, and an unlock is what
	// makes them work unattended. Both halves are here because a user who is
	// refused by one cannot tell which from the error alone.
	trust := widget.NewButton("Make owned", func() {
		go u.setTrust(id, openairv1.TrustLevel_TRUST_LEVEL_OWNED)
	})
	if d.GetLevel() == openairv1.TrustLevel_TRUST_LEVEL_OWNED {
		trust = widget.NewButton("Demote to trusted", func() {
			go u.setTrust(id, openairv1.TrustLevel_TRUST_LEVEL_TRUSTED)
		})
	}
	unlock := widget.NewButtonWithIcon("Unlock…", theme.VisibilityIcon(), func() { u.askUnlock(id) })
	lock := widget.NewButton("Lock", func() { go u.lock(id) })

	objs = append(objs,
		widget.NewLabelWithStyle("Owned access", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(trust, unlock, lock),
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Remote files and screen", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(
			widget.NewButtonWithIcon("Browse…", theme.FolderIcon(), func() { u.showBrowser(d) }),
			widget.NewButtonWithIcon("Watch screen", theme.ComputerIcon(), func() { go u.mirror(id) }),
			widget.NewButton("Stop watching", func() { go u.stopMirror(id) }),
		),
	)
	return objs
}

func fingerprintOf(d *openairv1.DaemonDevice) string {
	return deviceFingerprint(d.GetDeviceId())
}

// ── the send queue ───────────────────────────────────────────────────────────

func (u *ui) enqueue(paths ...string) {
	u.mu.Lock()
	for _, p := range paths {
		if p != "" && exists(p) {
			u.queued = append(u.queued, p)
		}
	}
	u.mu.Unlock()
	u.refreshQueue()
}

func (u *ui) queueText() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.queued) == 0 {
		return "nothing queued"
	}
	names := make([]string, 0, len(u.queued))
	for _, p := range u.queued {
		names = append(names, filepath.Base(p))
	}
	return fmt.Sprintf("%d file(s): %s", len(names), strings.Join(names, ", "))
}

func (u *ui) refreshQueue() {
	text := u.queueText()
	fyne.Do(func() {
		if u.queueLabel != nil {
			u.queueLabel.SetText(text)
		}
	})
}

// onDropped queues files dropped on the window. Directories are queued as they
// are: the daemon walks them, the same as `openair send` does.
func (u *ui) onDropped(_ fyne.Position, uris []fyne.URI) {
	paths := make([]string, 0, len(uris))
	for _, uri := range uris {
		if uri != nil {
			paths = append(paths, uri.Path())
		}
	}
	u.enqueue(paths...)
	u.logf("queued %d dropped item(s)", len(paths))
}

// sendQueued runs the transfer. It blocks for its whole duration, which is why
// it must be called on its own goroutine, and why the result is the only honest
// report that the bytes arrived.
func (u *ui) sendQueued(deviceID string) {
	c := u.need()
	if c == nil {
		return
	}
	u.mu.Lock()
	paths := append([]string(nil), u.queued...)
	u.mu.Unlock()
	if len(paths) == 0 {
		u.info("Nothing to send", "Add files first, or drop them on the window.")
		return
	}

	u.logf("sending %d file(s) to %s", len(paths), deviceFingerprint(deviceID))
	// No timeout: a large transfer over a slow link is not a stuck call, and the
	// daemon ends it on failure.
	resp, err := c.Send(context.Background(), deviceID, paths)
	if err != nil {
		u.fail(err)
		return
	}
	u.logf("transfer %s to %s complete", resp.GetTransferId(), deviceFingerprint(resp.GetDeviceId()))
	u.mu.Lock()
	u.queued = nil
	u.mu.Unlock()
	u.refreshQueue()
}

// ── clipboard, notifications, trust ──────────────────────────────────────────

func (u *ui) pushClipboard(deviceID, text string) {
	if strings.TrimSpace(text) == "" {
		u.info("Clipboard is empty", "There is nothing to push.")
		return
	}
	c := u.need()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	push := &openairv1.ClipboardPush{
		Mime:     "text/plain; charset=utf-8",
		Content:  []byte(text),
		OriginTs: time.Now().UnixMilli(),
	}
	if err := c.Clipboard(ctx, deviceID, push); err != nil {
		u.fail(err)
		return
	}
	u.logf("pushed %d byte(s) of clipboard to %s", len(text), deviceFingerprint(deviceID))
}

func (u *ui) askText(deviceID string) {
	entry := widget.NewMultiLineEntry()
	entry.SetPlaceHolder("text to send to the other device's clipboard")
	d := dialog.NewCustomConfirm("Push text", "Push", "Cancel", entry, func(ok bool) {
		if ok {
			go u.pushClipboard(deviceID, entry.Text)
		}
	}, u.win)
	d.Resize(fyne.NewSize(460, 260))
	d.Show()
}

func (u *ui) askNotify(deviceID string) {
	title := widget.NewEntry()
	title.SetPlaceHolder("title")
	body := widget.NewMultiLineEntry()
	body.SetPlaceHolder("body (optional)")
	form := container.NewVBox(title, body)

	d := dialog.NewCustomConfirm("Send a notification", "Send", "Cancel", form, func(ok bool) {
		if !ok || strings.TrimSpace(title.Text) == "" {
			return
		}
		go u.notify(deviceID, title.Text, body.Text)
	}, u.win)
	d.Resize(fyne.NewSize(460, 260))
	d.Show()
}

func (u *ui) notify(deviceID, title, body string) {
	c := u.need()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	n := &openairv1.Posted{
		Key:      fmt.Sprintf("gui-%d", time.Now().UnixNano()),
		AppId:    "com.openair.gui",
		AppName:  "OpenAir",
		Title:    title,
		Body:     body,
		PostedAt: time.Now().UnixMilli(),
	}
	delivered, filtered, err := c.Notify(ctx, deviceID, n)
	if err != nil {
		u.fail(err)
		return
	}
	if filtered {
		// Not an error (PRD R22): this device's own policy dropped it before it
		// reached the wire, and saying so is the difference between a policy and
		// a bug.
		u.logf("notification filtered by local policy; nothing was sent")
		return
	}
	u.logf("notification delivered to %d device(s)", delivered)
}

func (u *ui) setTrust(deviceID string, level openairv1.TrustLevel) {
	c := u.need()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	got, err := c.Trust(ctx, deviceID, level, "")
	if err != nil {
		u.fail(err)
		return
	}
	u.logf("%s is now %s", deviceFingerprint(deviceID), strings.ToLower(strings.TrimPrefix(got.String(), "TRUST_LEVEL_")))
	u.refresh()
}

// askUnlock takes the passphrase that opens this device's privilege key. The
// credential goes over the local socket to the daemon, which is where the sealed
// key lives; this window never sees the key itself.
func (u *ui) askUnlock(deviceID string) {
	pass := widget.NewPasswordEntry()
	pass.SetPlaceHolder("passphrase")
	note := widget.NewLabel("Unlocks a six-hour session in which this machine may act on " +
		deviceFingerprint(deviceID) + " unattended.")
	note.Wrapping = fyne.TextWrapWord

	d := dialog.NewCustomConfirm("Unlock", "Unlock", "Cancel",
		container.NewVBox(note, pass), func(ok bool) {
			if !ok {
				return
			}
			go u.unlock(deviceID, pass.Text)
		}, u.win)
	d.Resize(fyne.NewSize(460, 220))
	d.Show()
}

func (u *ui) unlock(deviceID, passphrase string) {
	c := u.need()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	resp, err := c.Unlock(ctx, deviceID, []byte(passphrase), nil, false, 0)
	if err != nil {
		u.fail(err)
		return
	}
	if resp.GetExpiresUnixMs() == 0 {
		u.logf("unlocked %s until you lock it", deviceFingerprint(deviceID))
	} else {
		u.logf("unlocked %s for %s", deviceFingerprint(deviceID),
			time.Until(time.UnixMilli(resp.GetExpiresUnixMs())).Round(time.Minute))
	}
	if resp.GetKeySwappable() {
		u.logf("warning: this machine refused to lock the key into RAM, so it may reach swap")
	}
	u.refresh()
}

func (u *ui) lock(deviceID string) {
	c := u.need()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if err := c.Lock(ctx, deviceID); err != nil {
		u.fail(err)
		return
	}
	u.logf("locked %s", deviceFingerprint(deviceID))
	u.refresh()
}

// ── mirroring ────────────────────────────────────────────────────────────────

// mirror asks for the other device's screen and shows the loopback URL it is
// published at. The URL is the deliverable, the same as it is for `openair
// mirror`: a player that speaks HTTP is a better viewer than anything this
// window could draw, and the frames never leave this machine to reach it.
func (u *ui) mirror(deviceID string) {
	c := u.need()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	url, err := c.Mirror(ctx, deviceID, 0, 0, 0, 0)
	if err != nil {
		u.fail(err)
		return
	}
	u.logf("mirroring %s at %s", deviceFingerprint(deviceID), url)
	u.showURL("Watching a screen", url,
		"Open this in a player that speaks HTTP — `mpv "+url+"` works. Stop watching to lower the indicator on the other device.")
}

func (u *ui) stopMirror(deviceID string) {
	c := u.need()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if err := c.StopMirror(ctx, deviceID); err != nil {
		u.fail(err)
		return
	}
	u.logf("stopped mirroring %s", deviceFingerprint(deviceID))
}

// showURL presents a loopback URL with a button that copies it, which is the
// only thing a user actually wants to do with one.
func (u *ui) showURL(title, url, note string) {
	fyne.Do(func() {
		link := widget.NewEntry()
		link.SetText(url)
		hint := widget.NewLabel(note)
		hint.Wrapping = fyne.TextWrapWord
		copyBtn := widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), func() {
			u.app.Clipboard().SetContent(url)
		})
		d := dialog.NewCustom(title, "Close",
			container.NewVBox(link, copyBtn, hint), u.win)
		d.Resize(fyne.NewSize(520, 240))
		d.Show()
	})
}
