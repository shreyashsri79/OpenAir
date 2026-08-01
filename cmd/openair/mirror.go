package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shreyashsri79/openair/internal/daemon"
)

// `openair mirror`: M15's surface (§14).
//
// It prints a URL, for the same reason `openair stream` does: every media
// player already decodes H.264 from one. The alternative is building a video
// window, which would be this project's second-worst renderer and would arrive
// years after mpv's. `--with mpv` is a viewer; `--with 'mpv --profile=low-latency
// --untimed'` is a good one.
//
// Watching a screen is Owned-level and needs the far end to have started its
// daemon with --share-screen. Both refusals say so.

func runMirror(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("mirror", flag.ContinueOnError)
	fs.SetOutput(stdout)
	socket := fs.String("socket", "", "daemon IPC socket path")
	width := fs.Int("width", 0, "scale the source to this width (0 keeps its own)")
	height := fs.Int("height", 0, "scale the source to this height")
	fps := fs.Int("fps", 30, "frames per second to ask for")
	bitrate := fs.String("bitrate", "8Mb", "bitrate to ask for, in bits per second")
	with := fs.String("with", "", "run this player with the URL (for example: mpv)")
	open := fs.Bool("open", false, "hand the URL to this desktop's default player")
	stop := fs.Bool("stop", false, "stop watching, which lowers the indicator on that device")
	timeout := fs.Duration("timeout", 60*time.Second, "how long to spend setting it up")
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: openair mirror DEVICE [--with PLAYER|--open] [--fps N] [--bitrate 8Mb] [--stop]")
	}
	device := rest[0]

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := daemon.Connect(ctx, *socket, nil, nil)
	if err != nil {
		return fmt.Errorf("%w\nwatching a screen needs the daemon: it holds the session and the key that proves who you are", err)
	}
	defer c.Close()

	if *stop {
		if err := c.StopMirror(ctx, device); err != nil {
			return mirrorAdvice(device, err)
		}
		fmt.Fprintf(stdout, "stopped watching %s\n", device)
		return nil
	}

	bits, err := parseBits(*bitrate)
	if err != nil {
		return fmt.Errorf("--bitrate: %w", err)
	}

	url, err := c.Mirror(ctx, device, *width, *height, *fps, bits/8)
	if err != nil {
		return mirrorAdvice(device, err)
	}

	fmt.Fprintf(stdout, "%s\n", url)
	fmt.Fprintf(stdout, "H.264, %d fps — the URL is live while this daemon runs, and only from this machine\n", *fps)
	fmt.Fprintf(stdout, "stop with: openair mirror %s --stop\n", device)

	switch {
	case *with != "":
		fields := strings.Fields(*with)
		return play(fields[0], fields[1:], url)
	case *open:
		program, args := defaultOpener()
		return play(program, args, url)
	}
	return nil
}

// mirrorAdvice turns the two refusals a person can act on into instructions.
func mirrorAdvice(device string, err error) error {
	switch {
	case strings.Contains(err.Error(), "not sharing its screen"),
		strings.Contains(err.Error(), "share-screen"):
		return fmt.Errorf("%w\n\nstart that device's daemon with `openaird --share-screen`; it is off by default", err)
	case strings.Contains(err.Error(), "unlock"):
		return fmt.Errorf("%w\n\nrun `openair unlock %s` first: watching a screen is Owned-level", err, device)
	case strings.Contains(err.Error(), "no encoder"), strings.Contains(err.Error(), "ffmpeg"):
		return fmt.Errorf("%w\n\nthat device has no encoder: install ffmpeg there, or start its daemon with "+
			"`--mirror-command` naming one that writes Annex-B H.264 to stdout", err)
	}
	return err
}

// parseBits reads "8Mb", "500Kb" or a plain bit count.
func parseBits(s string) (int, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "ps")
	mult := 1
	switch {
	case strings.HasSuffix(s, "Mb"), strings.HasSuffix(s, "mb"):
		mult, s = 1_000_000, s[:len(s)-2]
	case strings.HasSuffix(s, "Kb"), strings.HasSuffix(s, "kb"):
		mult, s = 1_000, s[:len(s)-2]
	case strings.HasSuffix(s, "b"):
		s = s[:len(s)-1]
	}
	var n float64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%g", &n); err != nil {
		return 0, fmt.Errorf("expected something like 8Mb, got %q", s)
	}
	return int(n * float64(mult)), nil
}
