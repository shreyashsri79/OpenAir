package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"time"

	"github.com/shreyashsri79/openair/internal/daemon"
)

// `openair stream`: M11's surface (§11.2, §11.4).
//
// It prints a URL. That is the whole design — every media player already opens
// URLs and already issues Range requests, and a Range request is §11.2's range
// read with a different header on it. So there is no plugin, nothing is
// mounted, and a 40 GB film starts playing at the second someone drags the
// scrubber to.
//
// The URL is loopback-only and carries an unguessable token, and it stops
// working when the daemon does. Printing it is deliberate: the shell that asked
// is the only thing that gets it.

func runStream(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("stream", flag.ContinueOnError)
	fs.SetOutput(stdout)
	socket := fs.String("socket", "", "daemon IPC socket path")
	open := fs.Bool("open", false, "hand the URL to this desktop's default player")
	with := fs.String("with", "", "run this program with the URL as its argument (for example: mpv)")
	stop := fs.Bool("stop", false, "stop serving a URL published earlier")
	timeout := fs.Duration("timeout", 60*time.Second, "how long to spend setting it up")
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: openair stream DEVICE PATH [--open|--with PLAYER] [--stop]")
	}
	device, remote := rest[0], rest[1]

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := daemon.Connect(ctx, *socket, nil, nil)
	if err != nil {
		return fmt.Errorf("%w\nstreaming needs the daemon: it holds the session and the key that proves who you are", err)
	}
	defer c.Close()

	if *stop {
		if err := c.StopStream(ctx, device, remote); err != nil {
			return browseError(device, err)
		}
		fmt.Fprintf(stdout, "stopped streaming %s\n", remote)
		return nil
	}

	url, mime, size, err := c.Stream(ctx, device, remote)
	if err != nil {
		return browseError(device, err)
	}

	fmt.Fprintf(stdout, "%s\n", url)
	fmt.Fprintf(stdout, "%s, %s — the URL works while this daemon runs, and only from this machine\n",
		humanBytes(size), orElse(mime, "unknown type"))

	switch {
	case *with != "":
		return play(*with, nil, url)
	case *open:
		program, args := defaultOpener()
		return play(program, args, url)
	}
	return nil
}

// play starts a player and does not wait for it: the point is to get back to
// the shell while the film runs.
func play(program string, args []string, url string) error {
	if program == "" {
		return fmt.Errorf("no way to open a URL on this platform; pass --with PLAYER")
	}
	cmd := exec.Command(program, append(append([]string{}, args...), url)...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", program, err)
	}
	go cmd.Wait() // reap it rather than leaving a zombie behind
	return nil
}

// defaultOpener is the platform's "open this URL with whatever handles it".
func defaultOpener() (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		return "xdg-open", nil
	}
}

func orElse(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
