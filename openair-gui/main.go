package main

import (
	"fmt"
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

	"github.com/shreyashsri79/openair-gui/internal/receiver"
	"github.com/shreyashsri79/openair-gui/internal/sender"
)

func main() {
	a := app.New()
	w := a.NewWindow("OpenAir")
	w.Resize(fyne.NewSize(400, 700))

	// Setup logging view
	logData := binding.NewString()
	logArea := widget.NewMultiLineEntry()
	logArea.Bind(logData)
	logArea.Disable() // Read-only

	appendLog := func(msg string) {
		currentText, _ := logData.Get()
		lines := strings.Split(currentText, "\n")
		if len(lines) > 50 {
			lines = lines[len(lines)-50:]
		}
		newText := strings.Join(lines, "\n")
		if newText != "" {
			newText += "\n"
		}
		newText += fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg)
		logData.Set(newText)
		
		// Note: logArea.CursorRow isn't thread-safe here either, so we'll omit auto-scrolling
		// for now to ensure stability. Data binding will handle the text update safely.
	}

	sender.OnLog = appendLog
	receiver.OnLog = appendLog

	// Header
	logo := canvas.NewImageFromFile("../logo.png")
	logo.FillMode = canvas.ImageFillContain
	logo.SetMinSize(fyne.NewSize(40, 40))

	title := widget.NewLabelWithStyle("OpenAir", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	deviceBadge := widget.NewLabel("💻 My PC")

	header := container.NewBorder(nil, nil, logo, deviceBadge, title)

	// Receiver Toggle
	receiverRunning := false
	receiverState := binding.NewBool()
	receiverToggle := widget.NewCheckWithData("Receiver", receiverState)
	
	receiverState.AddListener(binding.NewDataListener(func() {
		checked, _ := receiverState.Get()
		if checked {
			if !receiverRunning {
				receiverRunning = true
				appendLog("Starting Receiver...")
				go func() {
					err := receiver.RunReceiver()
					if err != nil {
						appendLog("Receiver error: " + err.Error())
					}
					receiverRunning = false
					receiverState.Set(false)
				}()
			}
		} else {
			appendLog("Receiver toggle off (Note: requires restart to fully unbind port)")
		}
	}))

	receiverCard := container.NewVBox(
		widget.NewLabelWithStyle("Receiver", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		receiverToggle,
	)

	// Attach & Send
	var selectedFile string
	fileLabel := widget.NewLabel("No file selected")

	attachBtn := widget.NewButtonWithIcon("Attach File", theme.FolderIcon(), func() {
		dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, w)
				return
			}
			if reader == nil {
				return
			}
			selectedFile = reader.URI().Path()
			fileLabel.SetText(fmt.Sprintf("Attached: %s", filepath.Base(selectedFile)))
		}, w)
	})

	sendBtn := widget.NewButtonWithIcon("Send", theme.MailSendIcon(), func() {
		if selectedFile == "" {
			dialog.ShowInformation("Error", "Please attach a file first.", w)
			return
		}
		appendLog(fmt.Sprintf("Preparing to send %s...", filepath.Base(selectedFile)))
		go func(path string) {
			err := sender.RunSender(path, appendLog)
			if err != nil {
				appendLog("Send failed: " + err.Error())
			}
		}(selectedFile)
	})
	sendBtn.Importance = widget.HighImportance // Make it prominent

	attachSection := container.NewVBox(
		widget.NewLabelWithStyle("Attach & Send", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		fileLabel,
		attachBtn,
	)

	// Logs Section
	logsCard := container.NewVBox(
		widget.NewLabelWithStyle("Logs", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
	)

	// Combine into main layout
	content := container.NewBorder(
		container.NewVBox(
			header,
			widget.NewSeparator(),
			receiverCard,
			widget.NewSeparator(),
			attachSection,
			widget.NewSeparator(),
			logsCard,
		),
		sendBtn, // Bottom sticky button
		nil, nil,
		logArea, // Center fills remaining space
	)

	paddedContent := container.NewPadded(content)
	w.SetContent(paddedContent)
	w.ShowAndRun()
}
