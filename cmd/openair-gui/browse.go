package main

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// browsePage is how many entries one listing asks for. §11.1 pages listings so
// that a directory of 100k entries is many small answers rather than one
// envelope nobody can send.
const browsePage = 500

// browser is one window onto another device's shared folders (M10, §11).
//
// It holds no cache and no state the daemon does not already have: every
// navigation is a fresh Browse call, because the other device's disk changes
// without telling us and a stale listing that looks live is worse than a
// re-fetch.
type browser struct {
	u      *ui
	device *openairv1.DaemonDevice
	win    fyne.Window

	pathLabel *widget.Label
	list      *widget.List

	cur       string // "" is the share list itself
	entries   []*openairv1.FileStat
	truncated bool
}

// showBrowser opens a browser window for a device. Browsing is Owned-level, so
// a device that is merely trusted, or one that is locked, will be refused here —
// the refusal is reported rather than hidden, because the fix (promote, then
// unlock) is something only the user can do.
func (u *ui) showBrowser(d *openairv1.DaemonDevice) {
	b := &browser{u: u, device: d}
	b.win = u.app.NewWindow("Files on " + displayName(d))

	b.pathLabel = widget.NewLabel("/")
	b.pathLabel.Wrapping = fyne.TextWrapWord

	up := widget.NewButtonWithIcon("Up", theme.NavigateBackIcon(), func() {
		if b.cur == "" {
			return
		}
		parent := path.Dir(b.cur)
		if parent == "." || parent == "/" {
			parent = ""
		}
		go b.load(parent)
	})
	shares := widget.NewButtonWithIcon("Shares", theme.HomeIcon(), func() { go b.load("") })
	reload := widget.NewButtonWithIcon("Reload", theme.ViewRefreshIcon(), func() { go b.load(b.cur) })

	b.list = widget.NewList(
		func() int { return len(b.entries) },
		func() fyne.CanvasObject {
			return container.NewBorder(nil, nil, widget.NewIcon(theme.FileIcon()), widget.NewLabel("size"),
				widget.NewLabel("name"))
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i < 0 || i >= len(b.entries) {
				return
			}
			e := b.entries[i]
			box := o.(*fyne.Container)
			icon := box.Objects[1].(*widget.Icon)
			size := box.Objects[2].(*widget.Label)
			name := box.Objects[0].(*widget.Label)
			if e.GetIsDir() {
				icon.SetResource(theme.FolderIcon())
				size.SetText("")
			} else {
				icon.SetResource(theme.FileIcon())
				size.SetText(humanBytes(e.GetSize()))
			}
			name.SetText(path.Base(e.GetPath()))
		},
	)
	b.list.OnSelected = func(i widget.ListItemID) {
		if i < 0 || i >= len(b.entries) {
			return
		}
		e := b.entries[i]
		b.list.Unselect(i)
		if e.GetIsDir() {
			go b.load(e.GetPath())
			return
		}
		b.fileActions(e)
	}

	top := container.NewVBox(
		container.NewHBox(shares, up, reload),
		b.pathLabel,
	)
	b.win.SetContent(container.NewBorder(top, nil, nil, nil, b.list))
	b.win.Resize(fyne.NewSize(720, 560))
	b.win.Show()

	go b.load("")
}

// load replaces the listing with what the device reports for p. An empty p asks
// for the share list itself.
func (b *browser) load(p string) {
	c := b.u.need()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	entries, truncated, err := c.Browse(ctx, b.device.GetDeviceId(), p, 0, browsePage)
	if err != nil {
		fyne.Do(func() { dialog.ShowError(err, b.win) })
		return
	}

	b.cur = p
	b.entries = entries
	b.truncated = truncated

	label := "/"
	if p != "" {
		label = p
	}
	if truncated {
		label += fmt.Sprintf("   (first %d entries; there are more)", len(entries))
	}
	fyne.Do(func() {
		b.pathLabel.SetText(label)
		b.list.Refresh()
		b.list.ScrollToTop()
	})
}

// fileActions offers the two things worth doing with a remote file: copy it
// here, or play it where it is.
func (b *browser) fileActions(e *openairv1.FileStat) {
	head := widget.NewLabel(fmt.Sprintf("%s\n%s%s",
		e.GetPath(), humanBytes(e.GetSize()), mimeSuffix(e.GetMime())))
	head.Wrapping = fyne.TextWrapWord

	var d *dialog.CustomDialog
	fetch := widget.NewButtonWithIcon("Save a copy…", theme.DownloadIcon(), func() {
		d.Hide()
		b.askFetch(e)
	})
	stream := widget.NewButtonWithIcon("Stream it", theme.MediaPlayIcon(), func() {
		d.Hide()
		go b.stream(e)
	})

	d = dialog.NewCustom(path.Base(e.GetPath()), "Close",
		container.NewVBox(head, fetch, stream), b.win)
	d.Resize(fyne.NewSize(480, 260))
	d.Show()
}

func mimeSuffix(mime string) string {
	if mime == "" {
		return ""
	}
	return "  ·  " + mime
}

// askFetch picks the local folder to copy into. The daemon does the range reads
// and the writing; this window only names a destination.
func (b *browser) askFetch(e *openairv1.FileStat) {
	dialog.ShowFolderOpen(func(dir fyne.ListableURI, err error) {
		if err != nil || dir == nil {
			return
		}
		dest := filepath.Join(dir.Path(), path.Base(e.GetPath()))
		go b.fetch(e, dest)
	}, b.win)
}

func (b *browser) fetch(e *openairv1.FileStat, dest string) {
	c := b.u.need()
	if c == nil {
		return
	}
	b.u.logf("fetching %s to %s", e.GetPath(), dest)

	// No timeout: a large file over a slow link is not a stuck call.
	n, err := c.Fetch(context.Background(), b.device.GetDeviceId(), e.GetPath(), dest, 0, 0)
	if err != nil {
		b.u.fail(err)
		return
	}
	b.u.logf("wrote %s to %s", humanBytes(n), dest)
	b.u.info("Saved", fmt.Sprintf("%s (%s) was written to\n%s", path.Base(e.GetPath()), humanBytes(n), dest))
}

// stream publishes the file on a loopback URL. Nothing is downloaded ahead of
// what the player asks for: every Range request becomes a range read on the
// wire (§11.2).
func (b *browser) stream(e *openairv1.FileStat) {
	c := b.u.need()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	url, mime, size, err := c.Stream(ctx, b.device.GetDeviceId(), e.GetPath())
	if err != nil {
		b.u.fail(err)
		return
	}
	b.u.logf("streaming %s (%s, %s) at %s", e.GetPath(), mime, humanBytes(size), url)
	b.u.showURL("Streaming "+path.Base(e.GetPath()), url,
		"Open this in any player that speaks HTTP — `mpv "+url+"` works. "+
			"Only the parts the player asks for are read across the network.")
}
