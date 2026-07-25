package main

import (
	"context"
	_ "embed"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/ncruces/zenity"

	"github.com/shreyashsri79/openair-gui/internal/receiver"
	"github.com/shreyashsri79/openair-gui/internal/sender"
)

//go:embed logo.png
var logoBytes []byte

type attachment struct {
	path  string
	isDir bool
}

// card wraps content in a rounded, outlined rectangle like the mock's panels.
func card(bg color.Color, content fyne.CanvasObject) fyne.CanvasObject {
	r := canvas.NewRectangle(bg)
	r.CornerRadius = 12
	r.StrokeColor = color.NRGBA{R: 0x18, G: 0x16, B: 0x10, A: 0xC0}
	r.StrokeWidth = 1.5
	return container.NewStack(r, container.NewPadded(container.NewPadded(content)))
}

func sectionLabel(text string) fyne.CanvasObject {
	l := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	return l
}

func main() {
	a := app.New()
	a.Settings().SetTheme(openairTheme{})

	logoRes := fyne.NewStaticResource("logo.png", logoBytes)
	a.SetIcon(logoRes)

	w := a.NewWindow("OpenAir")
	w.SetIcon(logoRes)
	w.Resize(fyne.NewSize(420, 760))

	// ── Logging ───────────────────────────────────────────────────────────────
	logData := binding.NewString()
	logArea := widget.NewMultiLineEntry()
	logArea.Bind(logData)
	logArea.Disable()
	logArea.Wrapping = fyne.TextWrapWord

	statusData := binding.NewString()
	statusData.Set("Ready.")
	statusLabel := widget.NewLabelWithData(statusData)
	statusLabel.Wrapping = fyne.TextWrapWord

	appendLog := func(msg string) {
		statusData.Set(msg)
		current, _ := logData.Get()
		lines := strings.Split(current, "\n")
		if len(lines) > 100 {
			lines = lines[len(lines)-100:]
		}
		text := strings.Join(lines, "\n")
		if text != "" {
			text += "\n"
		}
		text += fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg)
		logData.Set(text)
	}

	sender.OnLog = appendLog
	receiver.OnLog = appendLog

	// ── Header ────────────────────────────────────────────────────────────────
	logo := canvas.NewImageFromResource(logoRes)
	logo.FillMode = canvas.ImageFillContain
	logo.SetMinSize(fyne.NewSize(34, 34))

	title := canvas.NewText("OpenAir", colInk)
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.TextSize = 22

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "My PC"
	}
	badge := card(colBeige, container.NewHBox(
		widget.NewIcon(theme.ComputerIcon()),
		widget.NewLabelWithStyle(hostname, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
	))

	header := container.NewBorder(nil, nil,
		container.NewHBox(logo, container.NewCenter(title)),
		container.NewCenter(badge),
	)

	// ── Receiver card (navy) ──────────────────────────────────────────────────
	recvTitle := canvas.NewText("Receiver", color.White)
	recvTitle.TextStyle = fyne.TextStyle{Bold: true}
	recvTitle.TextSize = 17

	recvStatus := canvas.NewText("Off", color.NRGBA{R: 0xDD, G: 0xE6, B: 0xEC, A: 0xFF})
	recvStatus.TextSize = 12

	setRecvStatus := func(s string) {
		fyne.Do(func() {
			recvStatus.Text = s
			recvStatus.Refresh()
		})
	}

	var recvCheck *widget.Check
	recvCheck = widget.NewCheck("", func(on bool) {
		if on {
			go func() {
				setRecvStatus(fmt.Sprintf("Ready — listening on :%d", receiver.PORT))
				err := receiver.RunReceiver()
				if err != nil {
					appendLog("Receiver error: " + err.Error())
				}
				setRecvStatus("Off")
				fyne.Do(func() { recvCheck.SetChecked(false) })
			}()
		} else {
			receiver.Stop()
			setRecvStatus("Off")
		}
	})

	receiverCard := card(colNavy, container.NewBorder(nil, nil, nil,
		container.NewCenter(recvCheck),
		container.NewVBox(recvTitle, recvStatus),
	))

	// ── Available devices ─────────────────────────────────────────────────────
	selected := map[string]bool{} // device key → chosen as send target
	knownDevs := []sender.Device{}
	devicesBox := container.NewVBox()

	refreshDevices := func(devs []sender.Device) {
		knownDevs = devs
		devicesBox.RemoveAll()

		if len(devs) == 0 {
			empty := widget.NewLabel("No devices found yet…")
			empty.Importance = widget.LowImportance
			devicesBox.Add(empty)
		}

		live := map[string]bool{}
		for _, d := range devs {
			live[d.Key()] = true
			dev := d

			name := widget.NewLabelWithStyle(dev.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			addr := widget.NewLabel(fmt.Sprintf("%s:%d", dev.Host, dev.Port))
			addr.Importance = widget.LowImportance

			var btn *widget.Button
			btn = widget.NewButton("Connect", func() {
				selected[dev.Key()] = !selected[dev.Key()]
				if selected[dev.Key()] {
					btn.SetText("Selected")
					btn.Importance = widget.HighImportance
				} else {
					btn.SetText("Connect")
					btn.Importance = widget.MediumImportance
				}
				btn.Refresh()
			})
			if selected[dev.Key()] {
				btn.SetText("Selected")
				btn.Importance = widget.HighImportance
			}

			row := container.NewBorder(nil, nil,
				widget.NewIcon(theme.ComputerIcon()),
				container.NewCenter(btn),
				container.NewVBox(name, addr),
			)
			devicesBox.Add(card(colPaper, row))
		}
		// Drop selections for devices that disappeared.
		for k := range selected {
			if !live[k] {
				delete(selected, k)
			}
		}
		devicesBox.Refresh()
	}
	refreshDevices(nil)

	scanText := canvas.NewText("Scanning…", color.White)
	scanText.TextStyle = fyne.TextStyle{Bold: true}
	scanText.TextSize = 14
	scanCard := card(colNavy, container.NewCenter(scanText))

	discoverCtx, cancelDiscover := context.WithCancel(context.Background())
	go sender.DiscoverLoop(discoverCtx, 4*time.Second, 2*time.Second, func(devs []sender.Device) {
		fyne.Do(func() { refreshDevices(devs) })
	})
	w.SetOnClosed(func() {
		cancelDiscover()
		receiver.Stop()
	})

	// ── Attach & send ─────────────────────────────────────────────────────────
	var attachments []attachment
	attachedBox := container.NewVBox()

	var refreshAttached func()
	refreshAttached = func() {
		attachedBox.RemoveAll()
		for i, at := range attachments {
			idx := i
			kind := "file"
			if at.isDir {
				kind = "folder → zip"
			}
			lbl := widget.NewLabel(fmt.Sprintf("%s (%s)", filepath.Base(at.path), kind))
			lbl.Truncation = fyne.TextTruncateEllipsis
			rm := widget.NewButtonWithIcon("", theme.CancelIcon(), func() {
				attachments = append(attachments[:idx], attachments[idx+1:]...)
				refreshAttached()
			})
			attachedBox.Add(container.NewBorder(nil, nil, nil, rm, lbl))
		}
		attachedBox.Refresh()
	}

	addPaths := func(paths []string, isDir bool) {
		fyne.Do(func() {
			for _, p := range paths {
				if p == "" {
					continue
				}
				attachments = append(attachments, attachment{path: p, isDir: isDir})
			}
			refreshAttached()
		})
	}

	attachFilesBtn := widget.NewButtonWithIcon("+ Attach Files", theme.DocumentIcon(), func() {
		go func() {
			paths, err := zenity.SelectFileMultiple(zenity.Title("Select files to send"))
			if err == nil {
				addPaths(paths, false)
				return
			}
			if err == zenity.ErrCanceled {
				return
			}
			// Native picker unavailable — fall back to Fyne's dialog (single file).
			fyne.Do(func() {
				dialog.ShowFileOpen(func(rc fyne.URIReadCloser, err error) {
					if err != nil || rc == nil {
						return
					}
					rc.Close()
					addPaths([]string{rc.URI().Path()}, false)
				}, w)
			})
		}()
	})

	attachFolderBtn := widget.NewButtonWithIcon("+ Attach Folder", theme.FolderIcon(), func() {
		go func() {
			dir, err := zenity.SelectFile(zenity.Directory(), zenity.Title("Select folder to send"))
			if err == nil {
				addPaths([]string{dir}, true)
				return
			}
			if err == zenity.ErrCanceled {
				return
			}
			fyne.Do(func() {
				dialog.ShowFolderOpen(func(lu fyne.ListableURI, err error) {
					if err != nil || lu == nil {
						return
					}
					addPaths([]string{lu.Path()}, true)
				}, w)
			})
		}()
	})

	textEntry := widget.NewMultiLineEntry()
	textEntry.SetPlaceHolder("Paste text to send…")
	textEntry.Wrapping = fyne.TextWrapWord
	textEntry.SetMinRowsVisible(4)

	attachCard := card(colPaper, container.NewVBox(
		container.NewGridWithColumns(2, attachFilesBtn, attachFolderBtn),
		attachedBox,
		textEntry,
	))

	// ── Send ──────────────────────────────────────────────────────────────────
	sending := false
	var sendBtn *widget.Button
	sendBtn = widget.NewButtonWithIcon("Send", theme.MailSendIcon(), func() {
		if sending {
			return
		}
		targets := []sender.Device{}
		for _, d := range knownDevs {
			if selected[d.Key()] {
				targets = append(targets, d)
			}
		}
		if len(targets) == 0 {
			dialog.ShowInformation("No device selected",
				"Tap Connect on a device in the list first.", w)
			return
		}

		items := append([]attachment{}, attachments...)
		text := strings.TrimSpace(textEntry.Text)
		if len(items) == 0 && text == "" {
			dialog.ShowInformation("Nothing to send",
				"Attach files/folders or paste some text first.", w)
			return
		}

		sending = true
		sendBtn.Disable()

		go func() {
			defer func() {
				sending = false
				fyne.Do(func() { sendBtn.Enable() })
			}()

			// Resolve attachments: zip folders, dump text to a temp file.
			paths := []string{}
			for _, it := range items {
				if it.isDir {
					appendLog("Zipping folder: " + filepath.Base(it.path))
					zp, err := zipDir(it.path)
					if err != nil {
						appendLog("Zip failed: " + err.Error())
						continue
					}
					paths = append(paths, zp)
				} else {
					paths = append(paths, it.path)
				}
			}
			if text != "" {
				tp, err := writeTextTemp(text)
				if err != nil {
					appendLog("Text save failed: " + err.Error())
				} else {
					paths = append(paths, tp)
				}
			}

			for _, dev := range targets {
				for _, p := range paths {
					appendLog(fmt.Sprintf("Sending %s → %s", filepath.Base(p), dev.Name))
					if err := sender.RunSenderTo(dev.Host, dev.Port, p, appendLog); err != nil {
						appendLog(fmt.Sprintf("Send failed (%s): %v", dev.Name, err))
					} else {
						appendLog(fmt.Sprintf("Sent %s to %s", filepath.Base(p), dev.Name))
					}
				}
			}
			appendLog("All transfers finished.")
		}()
	})
	sendBtn.Importance = widget.HighImportance

	tall := canvas.NewRectangle(color.Transparent)
	tall.SetMinSize(fyne.NewSize(0, 46))
	sendArea := container.NewPadded(container.NewStack(tall, sendBtn))

	// ── Layout ────────────────────────────────────────────────────────────────
	logScroll := container.NewVScroll(logArea)
	logScroll.SetMinSize(fyne.NewSize(0, 110))

	page := container.NewVBox(
		header,
		receiverCard,
		sectionLabel("Available Devices"),
		devicesBox,
		scanCard,
		sectionLabel("Attach & Send"),
		attachCard,
		statusLabel,
		widget.NewAccordion(widget.NewAccordionItem("Activity log", logScroll)),
	)

	content := container.NewBorder(nil, sendArea, nil, nil,
		container.NewVScroll(container.NewPadded(page)),
	)

	w.SetContent(content)
	w.ShowAndRun()
}
