package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/clipboard"
	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/daemon"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// runClip is `openair clip push DEVICE [TEXT]`.
//
// Manual push is the guaranteed path on every platform (PRD R18). Automatic
// sync is M13 and opt-in when it arrives; nothing here reads the clipboard
// without being asked to.
func runClip(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "push" {
		return fmt.Errorf("usage: openair clip push DEVICE|ADDR [TEXT]")
	}

	fs := flag.NewFlagSet("clip push", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var o clipOptions
	fs.StringVar(&o.keys, "keys", "", "directory holding this device's keys")
	fs.StringVar(&o.socket, "socket", "", "daemon IPC socket path")
	fs.BoolVar(&o.noDaemon, "no-daemon", false, "dial from this process instead of asking the daemon")
	fs.BoolVar(&o.stdin, "stdin", false, "read the content from standard input")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Second, "how long to spend finding the device and pushing")
	rest, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: openair clip push DEVICE|ADDR [TEXT]")
	}
	o.target = rest[0]

	text, err := clipText(o, rest[1:], stdin)
	if err != nil {
		return err
	}
	if text == "" {
		return fmt.Errorf("nothing to push: the clipboard is empty and no text was given")
	}
	o.text = text

	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	return clipPush(ctx, o, stdin, stdout)
}

type clipOptions struct {
	target   string
	text     string
	keys     string
	socket   string
	noDaemon bool
	stdin    bool
	timeout  time.Duration
}

// clipText decides what to send: an argument, standard input, or -- when
// neither is given -- this machine's own clipboard.
func clipText(o clipOptions, rest []string, stdin io.Reader) (string, error) {
	if o.stdin {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read standard input: %w", err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	if len(rest) > 0 {
		return strings.Join(rest, " "), nil
	}
	text, err := clipboard.ReadOS(context.Background())
	if err != nil {
		return "", fmt.Errorf("%w\ngive the text as an argument, or pipe it with --stdin", err)
	}
	return text, nil
}

func clipPush(ctx context.Context, o clipOptions, stdin io.Reader, stdout io.Writer) error {
	if !o.noDaemon {
		err := clipViaDaemon(ctx, o, stdout)
		if err == nil {
			return nil
		}
		if !errors.Is(err, daemon.ErrNoDaemon) {
			return err
		}
		fmt.Fprintln(stdout, "no daemon running; dialling from this process")
	}
	return clipDirect(ctx, o, stdin, stdout)
}

func clipViaDaemon(ctx context.Context, o clipOptions, stdout io.Writer) error {
	c, err := connectDaemon(ctx, o.socket, nil, io.Discard, false)
	if err != nil {
		return err
	}
	defer c.Close()

	push := &openairv1.ClipboardPush{
		Mime:     clipboard.TextMIME,
		Content:  []byte(o.text),
		OriginTs: time.Now().UnixMilli(),
	}
	if err := c.Clipboard(ctx, o.target, push); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "pushed %d bytes to %s\n", len(o.text), o.target)
	return nil
}

// clipDirect is the no-daemon path: dial the device and push on the session
// this process owns.
func clipDirect(ctx context.Context, o clipOptions, stdin io.Reader, stdout io.Writer) error {
	id, err := loadIdentity(o.keys)
	if err != nil {
		return err
	}
	store, err := loadTrustStore(o.keys)
	if err != nil {
		return err
	}
	pairHandler, err := newPairingHandler(id, store, stdin, stdout)
	if err != nil {
		return err
	}

	cap := clipboard.New(clipboard.Config{Tag: string(id.DeviceID())})

	addrs, err := targetAddrs(ctx, sendOptions{addr: o.target, keys: o.keys}, id, stdout)
	if err != nil {
		return err
	}

	d := conn.NewDialer(id, hostname(), platform(),
		map[byte]session.Handler{clipboard.CapID: cap, 0: pairHandler})
	sess, err := dialFirst(ctx, d, addrs)
	if err != nil {
		return explainRefusal(err)
	}
	defer sess.Close(0, "done")

	// The same rule transfers follow: content goes to devices this one paired
	// with, and to no others (M2).
	if err := requirePaired(pairHandler, sess.Peer()); err != nil {
		return fmt.Errorf("%w; nothing was sent", err)
	}

	if err := cap.PushText(ctx, sess, o.text); err != nil {
		return err
	}

	// The push is one control message with no acknowledgement in §9, so the
	// close behind it would overtake it on the wire (D-46). A short linger is
	// the same remedy internal/pairing uses.
	time.Sleep(250 * time.Millisecond)

	fmt.Fprintf(stdout, "pushed %d bytes to %s\n", len(o.text),
		sess.Peer().DeviceID.Fingerprint())
	return nil
}
