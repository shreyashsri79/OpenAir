package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shreyashsri79/openair/internal/daemon"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// `openair notify` and `openair dismiss`: M12's surface (§12).
//
// The interesting one is `notify` with no device, which posts to every machine
// currently connected — that is PRD R23's case, a long build finishing on the
// home machine and the person seeing it wherever they are sitting. `watch`
// prints them at the other end.

func runNotify(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	fs.SetOutput(stdout)
	socket := fs.String("socket", "", "daemon IPC socket path")
	title := fs.String("title", "", "notification title")
	body := fs.String("body", "", "notification body (use --stdin to read it from a pipe)")
	app := fs.String("app", "openair", "app id this notification is attributed to, which the filter matches on")
	appName := fs.String("app-name", "", "human-readable app name (default: the app id)")
	category := fs.String("category", "", `"msg", "call", "alarm", "progress", or empty`)
	key := fs.String("key", "", "stable key for this notification, so it can be dismissed later (default: random)")
	fromStdin := fs.Bool("stdin", false, "read the body from standard input")
	timeout := fs.Duration("timeout", 30*time.Second, "how long to spend")
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}

	device := ""
	if len(rest) > 0 {
		device = rest[0]
	}
	text := *body
	if *fromStdin {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		text = strings.TrimRight(string(b), "\n")
	}
	if *title == "" && text == "" {
		return fmt.Errorf("usage: openair notify [DEVICE] --title TEXT [--body TEXT|--stdin]")
	}
	if *title == "" {
		*title = text
		text = ""
	}
	name := *appName
	if name == "" {
		name = *app
	}
	id := *key
	if id == "" {
		id, err = randomKey()
		if err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := daemon.Connect(ctx, *socket, nil, nil)
	if err != nil {
		return err
	}
	defer c.Close()

	delivered, filtered, err := c.Notify(ctx, device, &openairv1.Posted{
		Key:      id,
		AppId:    *app,
		AppName:  name,
		Title:    *title,
		Body:     text,
		Category: *category,
		PostedAt: time.Now().UnixMilli(),
	})
	if filtered {
		// Not an error: the filter is doing its job, and a user who set one
		// should be told which app was withheld rather than left guessing.
		fmt.Fprintf(stdout, "withheld: this device's notification filter excludes %q\n", *app)
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "notified %d device(s), key %s\n", delivered, id)
	return nil
}

func runDismiss(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("dismiss", flag.ContinueOnError)
	fs.SetOutput(stdout)
	socket := fs.String("socket", "", "daemon IPC socket path")
	device := fs.String("device", "", "the device to tell; default is every device that has it")
	action := fs.String("action", "", "press this action instead of dismissing")
	reply := fs.String("reply", "", "inline reply text, with --action")
	timeout := fs.Duration("timeout", 30*time.Second, "how long to spend")
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: openair dismiss KEY [--device DEVICE] [--action ID [--reply TEXT]]")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := daemon.Connect(ctx, *socket, nil, nil)
	if err != nil {
		return err
	}
	defer c.Close()

	if *action != "" {
		if *device == "" {
			return fmt.Errorf("--action needs --device: an action is pressed on the device that posted it")
		}
		if err := c.InvokeAction(ctx, *device, rest[0], *action, *reply); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "pressed %s on %s\n", *action, rest[0])
		return nil
	}
	if err := c.Dismiss(ctx, *device, rest[0]); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "dismissed %s\n", rest[0])
	return nil
}

// randomKey makes a stable-enough key for a notification posted from the CLI.
// §12 wants it opaque and source-assigned; nothing else reads it.
func randomKey() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}
