package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/shreyashsri79/openair/internal/daemon"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// `openair input`: M14's surface (§13).
//
// It is scriptable rather than interactive, and that is the honest shape for
// this milestone. Interactive control means capturing this machine's keyboard
// and pointer and forwarding them, which needs a window to capture *in* — and
// the window is M15's screen. What M14 delivers on its own is the ability to
// drive another machine from a script or a shell: type into it, press a key,
// move and click, scroll.
//
// Every form goes through the daemon, because driving another device is
// Owned-level and the key that proves it lives there (D-20).

func runInput(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("input", flag.ContinueOnError)
	fs.SetOutput(stdout)
	socket := fs.String("socket", "", "daemon IPC socket path")
	text := fs.String("text", "", "type this text")
	fromStdin := fs.Bool("stdin", false, "type what arrives on standard input")
	key := fs.String("key", "", "press a named key: enter, escape, tab, f5, up, a")
	mods := fs.String("mods", "", "modifiers held while --key is pressed, comma-separated: ctrl,alt,shift,super")
	move := fs.String("move", "", "move the pointer by X,Y")
	moveTo := fs.String("move-to", "", "move the pointer to X,Y in the target's screen space")
	click := fs.String("click", "", "click a button: left, right, middle, back, forward")
	scroll := fs.String("scroll", "", "scroll by DX,DY in notches")
	stop := fs.Bool("stop", false, "end the control session, which lowers the indicator on that device")
	timeout := fs.Duration("timeout", 60*time.Second, "how long to spend")
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: openair input DEVICE [--text TEXT|--stdin] [--key KEY [--mods ctrl,alt]] " +
			"[--move X,Y|--move-to X,Y] [--click BUTTON] [--scroll DX,DY] [--stop]")
	}
	device := rest[0]

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := daemon.Connect(ctx, *socket, nil, nil)
	if err != nil {
		return fmt.Errorf("%w\ncontrolling a device needs the daemon: it holds the session and the key that proves who you are", err)
	}
	defer c.Close()

	if *stop {
		if err := c.StopInput(ctx, device); err != nil {
			return inputError(device, err)
		}
		fmt.Fprintf(stdout, "stopped controlling %s\n", device)
		return nil
	}

	var actions []*openairv1.InputAction

	// Order matters and it is the order the flags were written in on the
	// command line as far as a person is concerned: move, then click, then
	// type. That is what `--move-to 100,200 --click left` should mean.
	if *move != "" {
		x, y, err := pair(*move)
		if err != nil {
			return fmt.Errorf("--move: %w", err)
		}
		actions = append(actions, &openairv1.InputAction{Move: &openairv1.InputMove{X: x, Y: y}})
	}
	if *moveTo != "" {
		x, y, err := pair(*moveTo)
		if err != nil {
			return fmt.Errorf("--move-to: %w", err)
		}
		actions = append(actions, &openairv1.InputAction{
			Move: &openairv1.InputMove{X: x, Y: y, Absolute: true},
		})
	}
	if *scroll != "" {
		dx, dy, err := pair(*scroll)
		if err != nil {
			return fmt.Errorf("--scroll: %w", err)
		}
		actions = append(actions, &openairv1.InputAction{Scroll: &openairv1.InputScroll{Dx: dx, Dy: dy}})
	}
	if *click != "" {
		actions = append(actions, &openairv1.InputAction{Click: *click})
	}
	if *key != "" {
		actions = append(actions, &openairv1.InputAction{Key: *key, Modifiers: splitCommas(*mods)})
	}
	if *text != "" {
		actions = append(actions, &openairv1.InputAction{Text: *text})
	}
	if *fromStdin {
		// Line by line, so `tail -f log | openair input laptop --stdin` types
		// as the lines arrive rather than at EOF.
		scanner := bufio.NewScanner(stdin)
		for scanner.Scan() {
			line := scanner.Text() + "\n"
			if _, err := c.Input(ctx, device, []*openairv1.InputAction{{Text: line}}); err != nil {
				return inputError(device, err)
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
	}

	if len(actions) == 0 {
		if *fromStdin {
			return nil
		}
		return fmt.Errorf("nothing to send: give --text, --key, --move, --click or --scroll")
	}

	sent, err := c.Input(ctx, device, actions)
	if err != nil {
		return inputError(device, err)
	}
	fmt.Fprintf(stdout, "sent %d event(s) to %s\n", sent, device)
	return nil
}

// inputError turns the refusals a user can act on into advice. Being refused
// here almost always means one of two things, and they have different fixes.
func inputError(device string, err error) error {
	switch {
	case strings.Contains(err.Error(), "unlock"):
		return fmt.Errorf("%w\n\nrun `openair unlock %s` first: controlling a device is Owned-level", err, device)
	case strings.Contains(err.Error(), "capability"), strings.Contains(err.Error(), "unavailable"):
		return fmt.Errorf("%w\n\nthat device is not accepting remote input: start its daemon with "+
			"`openaird --accept-input`, which is off by default", err)
	}
	return err
}

// pair parses "X,Y".
func pair(s string) (int32, int32, error) {
	a, b, ok := strings.Cut(s, ",")
	if !ok {
		return 0, 0, fmt.Errorf("expected X,Y, got %q", s)
	}
	x, err := strconv.Atoi(strings.TrimSpace(a))
	if err != nil {
		return 0, 0, err
	}
	y, err := strconv.Atoi(strings.TrimSpace(b))
	if err != nil {
		return 0, 0, err
	}
	return int32(x), int32(y), nil
}

func splitCommas(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
