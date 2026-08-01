// Command openair-gui is the desktop shell for `openaird`.
//
// It is a second front end onto the daemon control plane, not a second
// implementation of anything: every action here is one of the calls in
// internal/daemon.Client, which is the same path `openair` takes (D-29, D-51).
// Pairing, trust, key handling and the transfer itself stay in the daemon, so a
// GUI that is wrong about the protocol is not possible -- the worst it can be is
// wrong about a label.
//
// The window is also what `openair watch` is for a terminal: it subscribes with
// prompts, so an inbound transfer can be approved by clicking rather than by
// leaving a terminal open. Without something subscribed the daemon refuses
// inbound files, which is the intended default and not a bug to work around.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/shreyashsri79/openair/internal/daemon"
	"github.com/shreyashsri79/openair/internal/identity"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// callTimeout bounds every request that is not a transfer. A transfer gets its
// own, much longer, deadline: Client.Send blocks until the bytes have landed.
const callTimeout = 15 * time.Second

// ui is the whole application state. One instance, owned by the main
// goroutine, mutated from callbacks that arrive on daemon goroutines -- which is
// why every field the widgets read is behind mu, and every widget touch from a
// callback goes through fyne.Do.
type ui struct {
	app    fyne.App
	win    fyne.Window
	socket string

	mu      sync.Mutex
	client  *daemon.Client
	devices []*openairv1.DaemonDevice
	self    *openairv1.DaemonStatusResponse
	lines   []string

	// queued holds the paths waiting to be sent to the selected device. It
	// survives a rebuild of the detail panel, which is why it lives here rather
	// than in the widget that lists it.
	queued []string

	// offerCh receives this device's own pairing offer, which arrives as an
	// event rather than in the reply. Non-nil only while a display-side pairing
	// is in flight.
	offerCh chan string

	// Widgets built once in root() and refreshed thereafter.
	statusBar  *widget.Label
	deviceList *widget.List
	detail     *fyne.Container
	logList    *widget.List
	progress   *widget.ProgressBar
	progLabel  *widget.Label
	queueLabel *widget.Label

	selected int // index into devices, -1 when nothing is selected
}

func main() {
	socket := flag.String("socket", "", "daemon IPC socket path")
	flag.Parse()

	// Declared before the app exists, because Fyne latches it on the first call
	// and warns for the process's lifetime otherwise. Everything here that
	// touches a widget from a daemon goroutine goes through fyne.Do, which is
	// what this claims -- see the note on the ui type.
	app.SetMetadata(fyne.AppMetadata{
		ID:         "com.openair.gui",
		Name:       "OpenAir",
		Version:    "2.0.0",
		Migrations: map[string]bool{"fyneDo": true},
	})

	a := app.NewWithID("com.openair.gui")
	a.Settings().SetTheme(theme.DefaultTheme())
	w := a.NewWindow("OpenAir")

	u := &ui{app: a, win: w, socket: *socket, selected: -1}
	w.SetContent(u.root())
	w.Resize(fyne.NewSize(1060, 700))

	// Dropping files onto the window queues them for the selected device. It is
	// the only way to choose several files at once: Fyne's file dialog opens one
	// at a time.
	w.SetOnDropped(u.onDropped)

	go u.connect()
	w.SetOnClosed(u.close)
	w.ShowAndRun()
}

// root builds the window: a status bar, the device list beside a detail panel,
// and the activity log underneath.
func (u *ui) root() fyne.CanvasObject {
	u.statusBar = widget.NewLabel("connecting to openaird…")
	u.statusBar.Wrapping = fyne.TextWrapWord

	top := container.NewBorder(nil, nil, nil,
		container.NewHBox(
			widget.NewButtonWithIcon("Pair", theme.ContentAddIcon(), u.showPair),
			widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() { go u.refresh() }),
		),
		u.statusBar,
	)

	u.deviceList = widget.NewList(
		func() int {
			u.mu.Lock()
			defer u.mu.Unlock()
			return len(u.devices)
		},
		func() fyne.CanvasObject {
			return container.NewVBox(widget.NewLabel("name"), widget.NewLabel("state"))
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			u.mu.Lock()
			defer u.mu.Unlock()
			if i < 0 || i >= len(u.devices) {
				return
			}
			d := u.devices[i]
			box := o.(*fyne.Container)
			name := box.Objects[0].(*widget.Label)
			sub := box.Objects[1].(*widget.Label)
			name.SetText(displayName(d))
			sub.SetText(stateOf(d) + "  ·  " + identity.DeviceID(d.GetDeviceId()).Fingerprint())
		},
	)
	u.deviceList.OnSelected = func(i widget.ListItemID) {
		u.mu.Lock()
		u.selected = i
		u.mu.Unlock()
		u.showDetail()
	}

	u.detail = container.NewVBox(widget.NewLabel("Select a device."))

	u.progress = widget.NewProgressBar()
	u.progress.Hide()
	u.progLabel = widget.NewLabel("")

	u.logList = widget.NewList(
		func() int {
			u.mu.Lock()
			defer u.mu.Unlock()
			return len(u.lines)
		},
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			u.mu.Lock()
			defer u.mu.Unlock()
			if i >= 0 && i < len(u.lines) {
				o.(*widget.Label).SetText(u.lines[i])
			}
		},
	)

	devices := container.NewBorder(widget.NewLabelWithStyle("Devices", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		nil, nil, nil, u.deviceList)

	main := container.NewHSplit(devices, container.NewVScroll(u.detail))
	main.SetOffset(0.32)

	activity := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Activity", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			u.progLabel, u.progress,
		), nil, nil, nil, u.logList)

	split := container.NewVSplit(main, activity)
	split.SetOffset(0.66)

	return container.NewBorder(top, nil, nil, nil, split)
}

// connect opens the IPC link and subscribes for events and prompts. It runs off
// the UI goroutine and retries until the window is closed, so starting the GUI
// before the daemon is not an error the user has to notice.
func (u *ui) connect() {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		c, err := daemon.Connect(ctx, u.socket, u.onEvent, u.onPrompt)
		cancel()
		if err != nil {
			u.setStatus("no daemon: " + err.Error() + " — start `openaird`, then Refresh")
			time.Sleep(3 * time.Second)
			continue
		}

		// prompts: true is the point of this window. A shell that subscribes
		// without prompts leaves the daemon with nobody to ask, and inbound
		// transfers are refused rather than queued.
		ctx, cancel = context.WithTimeout(context.Background(), callTimeout)
		err = c.Subscribe(ctx, true)
		cancel()
		if err != nil {
			c.Close()
			u.setStatus("subscribe failed: " + err.Error())
			time.Sleep(3 * time.Second)
			continue
		}

		u.mu.Lock()
		u.client = c
		u.mu.Unlock()
		u.logf("connected to openaird")
		u.refresh()

		// Hold here until the daemon goes away, then reconnect. A daemon
		// restart should cost a reconnection, not a relaunch.
		<-c.Done()
		u.mu.Lock()
		u.client = nil
		u.devices = nil
		u.selected = -1
		u.mu.Unlock()
		u.logf("the daemon went away; reconnecting")
		fyne.Do(func() {
			u.deviceList.Refresh()
			u.detail.Objects = []fyne.CanvasObject{widget.NewLabel("Select a device.")}
			u.detail.Refresh()
		})
	}
}

func (u *ui) close() {
	if c := u.conn(); c != nil {
		c.Close()
	}
}

// conn returns the live client, or nil when the daemon is not connected.
func (u *ui) conn() *daemon.Client {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.client
}

// need returns the client, reporting to the user when there is none. Callers
// that get nil must not proceed.
func (u *ui) need() *daemon.Client {
	c := u.conn()
	if c == nil {
		u.fail(fmt.Errorf("not connected to openaird"))
	}
	return c
}

// refresh reloads status and the device list. Safe to call from any goroutine.
func (u *ui) refresh() {
	c := u.conn()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	st, err := c.Status(ctx)
	if err != nil {
		u.setStatus("status failed: " + err.Error())
		return
	}
	devices, err := c.Devices(ctx, false)
	if err != nil {
		u.setStatus("devices failed: " + err.Error())
		return
	}

	u.mu.Lock()
	u.self = st
	u.devices = devices
	if u.selected >= len(devices) {
		u.selected = -1
	}
	u.mu.Unlock()

	u.setStatus(summarise(st))
	fyne.Do(func() { u.deviceList.Refresh() })
	u.showDetail()
}

// summarise is the one-line answer to "what is this machine doing".
func summarise(st *openairv1.DaemonStatusResponse) string {
	name := st.GetDisplayName()
	if name == "" {
		name = "(unnamed)"
	}
	line := fmt.Sprintf("%s · %s · listening %s · saving to %s",
		name, identity.DeviceID(st.GetDeviceId()).Fingerprint(), st.GetListenAddr(), st.GetDestDir())
	switch st.GetProtectionTier() {
	case openairv1.ProtectionTier_PROTECTION_TIER_KEYSTORE:
		line += " · privilege: keystore"
	case openairv1.ProtectionTier_PROTECTION_TIER_PASSPHRASE:
		line += " · privilege: passphrase"
	default:
		line += " · privilege: none (run `openair protect` for owned access)"
	}
	return line
}

func (u *ui) setStatus(text string) {
	fyne.Do(func() { u.statusBar.SetText(text) })
}

// logf appends one line to the activity list and scrolls to it.
func (u *ui) logf(format string, args ...any) {
	line := time.Now().Format("15:04:05") + "  " + fmt.Sprintf(format, args...)
	u.mu.Lock()
	u.lines = append(u.lines, line)
	// The log is a running commentary, not a record; an unbounded one is a leak
	// in a window that stays open for days.
	if len(u.lines) > 500 {
		u.lines = u.lines[len(u.lines)-500:]
	}
	n := len(u.lines)
	u.mu.Unlock()

	fyne.Do(func() {
		u.logList.Refresh()
		u.logList.ScrollTo(n - 1)
	})
}

// fail reports an error in the log and in a dialog. Errors from the daemon are
// the user's business: "nothing happened" is the failure mode to avoid.
func (u *ui) fail(err error) {
	u.logf("error: %v", err)
	fyne.Do(func() { dialog.ShowError(err, u.win) })
}

func (u *ui) info(title, msg string) {
	fyne.Do(func() { dialog.ShowInformation(title, msg, u.win) })
}

// humanBytes renders a byte count the way the CLI does.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// deviceFingerprint is the short form of a device ID, the same one the CLI
// prints. It is what a user compares against the other machine's screen.
func deviceFingerprint(id string) string {
	if id == "" {
		return "(unknown device)"
	}
	return identity.DeviceID(id).Fingerprint()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
