package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// pairTimeout is how long a displayed offer stays valid. It matches the CLI's
// default: long enough to walk to the other device, short enough that an offer
// left on a screen does not stay live all afternoon.
const pairTimeout = 3 * time.Minute

// showPair asks which half of §5 this device is doing. Both halves exist
// because pairing is symmetric only in outcome: one device displays an offer and
// the other consumes it, and a user has to be told which one they are.
func (u *ui) showPair() {
	show := widget.NewButtonWithIcon("Show my code", theme.VisibilityIcon(), func() {
		go u.pairDisplay()
	})
	enter := widget.NewButtonWithIcon("Enter their code", theme.DocumentCreateIcon(), func() {
		u.askOffer()
	})
	note := widget.NewLabel("Do this once per pair of devices, with both of them in front of you. " +
		"One shows a code, the other types it, and then both compare six digits.")
	note.Wrapping = fyne.TextWrapWord

	d := dialog.NewCustom("Pair a device", "Cancel",
		container.NewVBox(note, show, enter), u.win)
	d.Resize(fyne.NewSize(460, 260))
	d.Show()
}

// pairDisplay puts this device's offer on screen and waits for the peer.
//
// The offer arrives as an event before the reply does -- the daemon cannot
// return it in the reply, because the reply does not come until pairing has
// finished or timed out -- so the event handler hands it here through offerCh.
func (u *ui) pairDisplay() {
	c := u.need()
	if c == nil {
		return
	}

	ch := make(chan string, 1)
	u.mu.Lock()
	u.offerCh = ch
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.offerCh = nil
		u.mu.Unlock()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case offer := <-ch:
			u.showURL("Your pairing code", offer,
				"Type or scan this on the other device within "+pairTimeout.String()+
					". Both devices will then show six digits — they must match.")
		case <-time.After(pairTimeout + 10*time.Second):
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), pairTimeout+30*time.Second)
	defer cancel()

	resp, err := c.Pair(ctx, "", pairTimeout)
	<-done
	if err != nil {
		u.fail(err)
		return
	}
	u.paired(resp)
}

// askOffer takes an offer that was shown on the other device. Both the
// `openair://pair/…` form and the hyphenated one are accepted, because a code
// that was scanned and a code that was typed are not written the same way.
func (u *ui) askOffer() {
	entry := widget.NewMultiLineEntry()
	entry.SetPlaceHolder("openair://pair/… or the hyphenated code")
	entry.Wrapping = fyne.TextWrapWord

	d := dialog.NewCustomConfirm("Enter their code", "Pair", "Cancel", entry, func(ok bool) {
		if !ok || strings.TrimSpace(entry.Text) == "" {
			return
		}
		go u.pairWith(strings.TrimSpace(entry.Text))
	}, u.win)
	d.Resize(fyne.NewSize(520, 260))
	d.Show()
}

func (u *ui) pairWith(offer string) {
	c := u.need()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pairTimeout+30*time.Second)
	defer cancel()

	resp, err := c.Pair(ctx, offer, pairTimeout)
	if err != nil {
		u.fail(err)
		return
	}
	u.paired(resp)
}

func (u *ui) paired(resp *openairv1.DaemonPairResponse) {
	name := resp.GetDisplayName()
	if name == "" {
		name = "(unnamed)"
	}
	u.logf("paired with %s %s", deviceFingerprint(resp.GetDeviceId()), name)
	u.info("Paired", fmt.Sprintf("%s (%s) is now paired with this device.",
		name, deviceFingerprint(resp.GetDeviceId())))
	u.refresh()
}

// ── events and prompts ───────────────────────────────────────────────────────

// onEvent is called on a daemon goroutine for everything this connection is
// subscribed to. It never touches a widget directly; logf, setStatus and the
// progress helpers all cross onto the UI goroutine themselves.
func (u *ui) onEvent(ev *openairv1.DaemonEvent) {
	switch ev.GetKind() {
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_PAIRED:
		// A PAIRED event with no device is the offer text for a pairing this
		// window asked to display; whoever asked for it shows it.
		if ev.GetDeviceId() == "" {
			u.mu.Lock()
			ch := u.offerCh
			u.mu.Unlock()
			if ch != nil {
				select {
				case ch <- ev.GetText():
				default:
				}
				return
			}
		}
		u.logf("paired with %s %s", deviceFingerprint(ev.GetDeviceId()), ev.GetText())
		go u.refresh()
		return

	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_DEVICE_FOUND,
		openairv1.DaemonEventKind_DAEMON_EVENT_KIND_DEVICE_LOST,
		openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_OPEN,
		openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_CLOSED:
		go u.refresh()

	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_CLIPBOARD:
		// Putting it on this machine's clipboard is the whole feature; logging
		// it and making the user copy it again is not.
		text := ev.GetText()
		fyne.Do(func() { u.app.Clipboard().SetContent(text) })

	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_TRANSFER_STARTED:
		u.setProgress(fmt.Sprintf("transfer %s from %s, %s",
			ev.GetTransferId(), deviceFingerprint(ev.GetDeviceId()), humanBytes(ev.GetBytesTotal())), 0)

	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_TRANSFER_PROGRESS:
		frac := 0.0
		if total := ev.GetBytesTotal(); total > 0 {
			frac = float64(ev.GetBytesDone()) / float64(total)
		}
		u.setProgress(fmt.Sprintf("%s / %s", humanBytes(ev.GetBytesDone()), humanBytes(ev.GetBytesTotal())), frac)
		return // too noisy for the log

	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_TRANSFER_DONE:
		u.clearProgress()
	}

	if line := formatEvent(ev); line != "" {
		u.logf("%s", line)
	}
}

// onPrompt is the daemon asking a question. It is synchronous: the transfer or
// the pairing is held open until this returns, so it blocks on the dialog's
// answer rather than defaulting to one.
//
// The default is refusal, and it must stay refusal. A prompt this build does not
// recognise cannot be shown to a user, and approving an unread question is the
// one outcome that must never happen.
func (u *ui) onPrompt(p *openairv1.DaemonPrompt) bool {
	answer := make(chan bool, 1)

	switch p.GetKind() {
	case openairv1.DaemonPromptKind_DAEMON_PROMPT_KIND_PAIR_SAS:
		who := deviceFingerprint(p.GetDeviceId())
		if p.GetDisplayName() != "" {
			who = fmt.Sprintf("%s (%s on %s)", who, p.GetDisplayName(), p.GetPlatform())
		}
		digits := formatSAS(p.GetSas())
		fyne.Do(func() {
			big := widget.NewLabelWithStyle(digits, fyne.TextAlignCenter, fyne.TextStyle{Bold: true, Monospace: true})
			body := widget.NewLabel("Pairing with " + who + ".\n\n" +
				"The other device is showing six digits too. Approve only if they are the same — " +
				"this comparison is the only thing that detects someone in the middle.")
			body.Wrapping = fyne.TextWrapWord
			d := dialog.NewCustomConfirm("Do these digits match?", "They match", "Refuse",
				container.NewVBox(big, body), func(ok bool) { answer <- ok }, u.win)
			d.Resize(fyne.NewSize(480, 300))
			d.Show()
		})

	case openairv1.DaemonPromptKind_DAEMON_PROMPT_KIND_ACCEPT_TRANSFER:
		who := deviceFingerprint(p.GetDeviceId())
		if p.GetDisplayName() != "" {
			who = fmt.Sprintf("%s (%s)", p.GetDisplayName(), who)
		}
		files := strings.Join(p.GetFiles(), "\n")
		fyne.Do(func() {
			list := widget.NewLabel(files)
			list.Wrapping = fyne.TextWrapWord
			head := widget.NewLabel(fmt.Sprintf("%s offers %d file(s), %s:",
				who, len(p.GetFiles()), humanBytes(p.GetTotalBytes())))
			head.Wrapping = fyne.TextWrapWord
			d := dialog.NewCustomConfirm("Accept these files?", "Accept", "Decline",
				container.NewVBox(head, container.NewVScroll(list)), func(ok bool) { answer <- ok }, u.win)
			d.Resize(fyne.NewSize(520, 340))
			d.Show()
		})

	default:
		u.logf("refusing a prompt this version does not understand (kind %d)", p.GetKind())
		return false
	}

	return <-answer
}

// formatSAS groups the six digits the way both devices must show them.
func formatSAS(sas string) string {
	if len(sas) != 6 {
		return sas
	}
	return sas[0:3] + " " + sas[3:6]
}

// formatEvent renders one event for the activity log, or returns "" for the
// ones worth staying quiet about.
func formatEvent(ev *openairv1.DaemonEvent) string {
	id := deviceFingerprint(ev.GetDeviceId())
	switch ev.GetKind() {
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_DEVICE_FOUND:
		return fmt.Sprintf("found %s %s", id, ev.GetText())
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_DEVICE_LOST:
		return fmt.Sprintf("lost %s", id)
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_OPEN:
		return fmt.Sprintf("session with %s %s", id, ev.GetText())
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_CLOSED:
		return fmt.Sprintf("session with %s closed", id)
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_TRANSFER_STARTED:
		return fmt.Sprintf("transfer %s from %s, %s", ev.GetTransferId(), id, humanBytes(ev.GetBytesTotal()))
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_TRANSFER_DONE:
		if ev.GetOk() {
			return fmt.Sprintf("transfer %s complete", ev.GetTransferId())
		}
		return fmt.Sprintf("transfer %s FAILED", ev.GetTransferId())
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_REFUSED:
		return "refused: " + ev.GetText()
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_CLIPBOARD:
		return fmt.Sprintf("clipboard from %s is now on this machine's clipboard", id)
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_ANNOUNCED:
		// §6.3's visible indicator. Deliberately loud: on this machine,
		// something else is about to move the pointer or type.
		return "** " + ev.GetText()
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_ENDED:
		return fmt.Sprintf("session with %s %s", id, ev.GetText())
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION:
		n := ev.GetNotification()
		if n == nil {
			return fmt.Sprintf("notification from %s: %s", id, ev.GetText())
		}
		line := fmt.Sprintf("[%s] %s", n.GetAppName(), n.GetTitle())
		if body := n.GetBody(); body != "" {
			line += " — " + body
		}
		return fmt.Sprintf("%s from %s  (%s)", line, id, n.GetKey())
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION_REMOVED:
		return "notification cleared: " + ev.GetText()
	default:
		return ""
	}
}

// ── the progress row ─────────────────────────────────────────────────────────

func (u *ui) setProgress(text string, frac float64) {
	fyne.Do(func() {
		u.progLabel.SetText(text)
		u.progress.SetValue(frac)
		u.progress.Show()
	})
}

func (u *ui) clearProgress() {
	fyne.Do(func() {
		u.progLabel.SetText("")
		u.progress.Hide()
	})
}
