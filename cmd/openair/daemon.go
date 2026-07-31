package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/daemon"
	"github.com/shreyashsri79/openair/internal/identity"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// connectDaemon opens the IPC link and wires the terminal to it: events are
// printed, prompts are asked.
//
// prompts is not a preference. A command that can be asked to compare six
// digits must pass true, and one that cannot answer must pass false rather than
// leave the daemon waiting out a timeout for an answer that was never coming.
func connectDaemon(ctx context.Context, socket string, in io.Reader, out io.Writer, prompts bool) (*daemon.Client, error) {
	var mu sync.Mutex // the prompt and the event stream share one terminal

	onEvent := func(ev *openairv1.DaemonEvent) {
		mu.Lock()
		defer mu.Unlock()
		if line := formatEvent(ev); line != "" {
			fmt.Fprintln(out, line)
		}
	}

	var onPrompt daemon.PromptFunc
	if prompts {
		onPrompt = func(p *openairv1.DaemonPrompt) bool {
			mu.Lock()
			defer mu.Unlock()
			return askPrompt(in, out, p)
		}
	}
	return daemon.Connect(ctx, socket, onEvent, onPrompt)
}

// askPrompt puts one daemon question to the user.
func askPrompt(in io.Reader, out io.Writer, p *openairv1.DaemonPrompt) bool {
	who := identity.DeviceID(p.GetDeviceId()).Fingerprint()
	if p.GetDisplayName() != "" {
		who = fmt.Sprintf("%s (%s on %s)", who, p.GetDisplayName(), p.GetPlatform())
	}

	switch p.GetKind() {
	case openairv1.DaemonPromptKind_DAEMON_PROMPT_KIND_PAIR_SAS:
		fmt.Fprintf(out, "\npairing with %s\n\n  %s\n\n", who, formatSAS(p.GetSas()))
		return confirm(in, out, p.GetText())
	case openairv1.DaemonPromptKind_DAEMON_PROMPT_KIND_ACCEPT_TRANSFER:
		fmt.Fprintf(out, "\n%s offers:\n", who)
		for _, f := range p.GetFiles() {
			fmt.Fprintf(out, "  %s\n", f)
		}
		return confirm(in, out, p.GetText())
	default:
		// §3.1 in spirit: a prompt kind this build does not know cannot be
		// shown to a user, and answering yes to an unread question is the one
		// thing that must never happen.
		fmt.Fprintf(out, "\nrefusing a prompt this version does not understand (kind %d)\n", p.GetKind())
		return false
	}
}

// formatEvent renders one event, or returns "" for the ones worth staying
// quiet about.
func formatEvent(ev *openairv1.DaemonEvent) string {
	id := ""
	if ev.GetDeviceId() != "" {
		id = identity.DeviceID(ev.GetDeviceId()).Fingerprint()
	}
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
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_TRANSFER_PROGRESS:
		return fmt.Sprintf("transfer %s: %s / %s", ev.GetTransferId(),
			humanBytes(ev.GetBytesDone()), humanBytes(ev.GetBytesTotal()))
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_TRANSFER_DONE:
		if ev.GetOk() {
			return fmt.Sprintf("transfer %s complete", ev.GetTransferId())
		}
		return fmt.Sprintf("transfer %s FAILED", ev.GetTransferId())
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_PAIRED:
		if ev.GetDeviceId() == "" {
			return "" // the offer text; whoever asked for it prints it
		}
		return fmt.Sprintf("paired with %s %s", id, ev.GetText())
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_REFUSED:
		return fmt.Sprintf("refused: %s", ev.GetText())
	case openairv1.DaemonEventKind_DAEMON_EVENT_KIND_CLIPBOARD:
		return fmt.Sprintf("clipboard from %s: %s", id, ev.GetText())
	default:
		return ""
	}
}

// formatSAS groups the six digits the way both devices must show them.
func formatSAS(sas string) string {
	if len(sas) != 6 {
		return sas
	}
	return sas[0:3] + " " + sas[3:6]
}

// runStatus reports what the daemon is doing. It is the one command with no
// direct-mode fallback: with no daemon there is no status.
func runStatus(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stdout)
	socket := fs.String("socket", "", "daemon IPC socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := connectDaemon(ctx, *socket, nil, io.Discard, false)
	if err != nil {
		return err
	}
	defer c.Close()

	st, err := c.Status(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "device      %s  %s on %s\n",
		identity.DeviceID(st.GetDeviceId()).Fingerprint(), st.GetDisplayName(), st.GetPlatform())
	fmt.Fprintf(stdout, "listening   %s\n", st.GetListenAddr())
	fmt.Fprintf(stdout, "saving to   %s\n", st.GetDestDir())
	fmt.Fprintf(stdout, "uptime      %s\n", time.Since(time.Unix(st.GetStartedUnix(), 0)).Truncate(time.Second))
	fmt.Fprintf(stdout, "paired      %d device(s)\n", st.GetPairedDevices())
	fmt.Fprintf(stdout, "sessions    %d open\n", st.GetActiveSessions())
	fmt.Fprintf(stdout, "announcing  %v\n", st.GetAnnouncing())
	switch {
	case st.GetAutoAccept():
		fmt.Fprintf(stdout, "inbound     accepted automatically (--accept-all)\n")
	case st.GetSubscribers() > 0:
		fmt.Fprintf(stdout, "inbound     %d client(s) can approve transfers\n", st.GetSubscribers())
	default:
		fmt.Fprintf(stdout, "inbound     refused -- nothing is watching; run `openair watch`\n")
	}
	return nil
}

// runDevices lists what the daemon knows: paired devices, and whatever
// discovery can currently see.
func runDevices(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("devices", flag.ContinueOnError)
	fs.SetOutput(stdout)
	socket := fs.String("socket", "", "daemon IPC socket path")
	pairedOnly := fs.Bool("paired", false, "list only paired devices")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := connectDaemon(ctx, *socket, nil, io.Discard, false)
	if err != nil {
		return err
	}
	defer c.Close()

	devices, err := c.Devices(ctx, *pairedOnly)
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		fmt.Fprintln(stdout, "no devices")
		return nil
	}
	for _, d := range devices {
		state := "seen"
		switch {
		case d.GetSessionOpen():
			state = "connected"
		case d.GetPaired():
			state = "paired"
		}
		name := d.GetDisplayName()
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(stdout, "%-20s %-10s %-24s %s\n",
			identity.DeviceID(d.GetDeviceId()).Fingerprint(), state, name, strings.Join(d.GetAddrs(), " "))
	}
	return nil
}

// runWatch is how a person supervises a headless daemon: it prints events and
// answers the questions the daemon cannot answer alone.
//
// Without something like this running, and without --accept-all, an inbound
// transfer is refused. That is the intended failure: a daemon that accepted
// files because nobody was watching would be a worse default than one that
// refuses them.
func runWatch(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stdout)
	socket := fs.String("socket", "", "daemon IPC socket path")
	yes := fs.Bool("yes", false, "approve every inbound transfer without asking")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var mu sync.Mutex
	onEvent := func(ev *openairv1.DaemonEvent) {
		mu.Lock()
		defer mu.Unlock()
		if line := formatEvent(ev); line != "" {
			fmt.Fprintln(stdout, line)
		}
	}
	// --yes answers "is this the device I meant" for transfers only. It must
	// never answer a SAS prompt: §5.2 forbids a skip-verification path, and the
	// digits are the only thing that detects a man in the middle.
	onPrompt := func(p *openairv1.DaemonPrompt) bool {
		mu.Lock()
		defer mu.Unlock()
		if *yes && p.GetKind() == openairv1.DaemonPromptKind_DAEMON_PROMPT_KIND_ACCEPT_TRANSFER {
			fmt.Fprintf(stdout, "accepting %d file(s) from %s\n",
				len(p.GetFiles()), identity.DeviceID(p.GetDeviceId()).Fingerprint())
			return true
		}
		return askPrompt(stdin, stdout, p)
	}

	c, err := daemon.Connect(ctx, *socket, onEvent, onPrompt)
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.Subscribe(ctx, true); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "watching the daemon; transfers will be offered here. ctrl-c to stop.")

	select {
	case <-ctx.Done():
		return nil
	case <-c.Done():
		return fmt.Errorf("the daemon went away")
	}
}

// groupOffer hyphenates an offer for manual entry, matching what
// pairing.EncodeOfferGrouped produces. The daemon sends the scheme form,
// because that is what a QR code carries; a person typing wants the other one,
// and DecodeOffer accepts both.
func groupOffer(offer string) string {
	const group = 8
	body := strings.TrimPrefix(offer, "openair://pair/")
	var b strings.Builder
	for i := 0; i < len(body); i += group {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(body[i:min(i+group, len(body))])
	}
	return b.String()
}
