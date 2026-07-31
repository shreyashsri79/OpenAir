package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ErrNoClipboard means this machine offers no way to reach the system
// clipboard: a headless server, a Linux session with none of the usual helpers
// installed, or a build for a platform with no support here.
//
// It is a normal condition rather than a failure. A daemon on a headless box
// still receives pushes and still reports them; it simply has nowhere to put
// them, and saying so is better than pretending the paste worked.
var ErrNoClipboard = errors.New("clipboard: no system clipboard available")

// osTimeout bounds a helper process. A clipboard tool that hangs -- waiting on
// a display that is not there, or on a compositor that is busy -- must not hang
// the daemon behind it.
const osTimeout = 5 * time.Second

// helper is one external clipboard tool and how to drive it.
type helper struct {
	name  string
	read  []string
	write []string
}

// helpers returns the tools worth trying on this platform, best first.
//
// External processes rather than a native binding, deliberately. A cgo X11 or
// Wayland binding would pull a display dependency into a daemon that mostly
// runs without one, and would have to be conditionally compiled per platform;
// exec'ing the tool the user's desktop already ships keeps this package
// buildable everywhere, which is what the Windows cross-build gate needs.
func helpers() []helper {
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd":
		return []helper{
			// Wayland first: on a Wayland session xclip talks to XWayland and
			// reaches a clipboard that half the applications cannot see.
			{name: "wl-copy", read: []string{"wl-paste", "--no-newline"}, write: []string{"wl-copy"}},
			{name: "xclip", read: []string{"xclip", "-selection", "clipboard", "-o"},
				write: []string{"xclip", "-selection", "clipboard", "-i"}},
			{name: "xsel", read: []string{"xsel", "--clipboard", "--output"},
				write: []string{"xsel", "--clipboard", "--input"}},
		}
	case "darwin":
		return []helper{{name: "pbcopy", read: []string{"pbpaste"}, write: []string{"pbcopy"}}}
	case "windows":
		return []helper{{
			name:  "powershell",
			read:  []string{"powershell", "-NoProfile", "-Command", "Get-Clipboard -Raw"},
			write: []string{"powershell", "-NoProfile", "-Command", "$input | Set-Clipboard"},
		}}
	default:
		return nil
	}
}

// pick returns the first helper whose binary exists.
func pick(needWrite bool) (helper, []string, error) {
	for _, h := range helpers() {
		argv := h.read
		if needWrite {
			argv = h.write
		}
		if len(argv) == 0 {
			continue
		}
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue
		}
		return h, argv, nil
	}
	return helper{}, nil, fmt.Errorf("%w on %s; install wl-clipboard, xclip or xsel, or pass the text as an argument",
		ErrNoClipboard, runtime.GOOS)
}

// ReadOS reads the system clipboard as text.
func ReadOS(ctx context.Context) (string, error) {
	_, argv, err := pick(false)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, osTimeout)
	defer cancel()

	var out, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("read clipboard with %s: %w: %s", argv[0], err, strings.TrimSpace(stderr.String()))
	}
	// Get-Clipboard appends a newline the user did not copy.
	return strings.TrimRight(out.String(), "\r\n"), nil
}

// WriteOS puts text on the system clipboard.
//
// The stderr handling here is not incidental. `wl-copy` forks a child that
// holds the selection until something replaces it, and that child inherits
// whatever stderr it was given. Hand it an os/exec pipe -- which is what
// assigning a bytes.Buffer does -- and Run blocks until the pipe reaches EOF,
// which is to say until the selection is replaced, which is to say until the
// user copies something else. The daemon's receive path hung on exactly this.
// A real file is not a pipe, so Run returns when the parent exits, and the
// diagnostics survive.
func WriteOS(ctx context.Context, text string) error {
	_, argv, err := pick(true)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, osTimeout)
	defer cancel()

	errFile, err := os.CreateTemp("", "openair-clip-*.err")
	if err != nil {
		return fmt.Errorf("write clipboard: %w", err)
	}
	defer func() {
		errFile.Close()
		os.Remove(errFile.Name())
	}()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stderr = errFile
	// A belt for the same hazard: if a helper still holds something open past
	// the deadline, do not wait on it indefinitely after the kill.
	cmd.WaitDelay = time.Second

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write clipboard with %s: %w: %s", argv[0], err, readErrFile(errFile))
	}
	return nil
}

func readErrFile(f *os.File) string {
	if _, err := f.Seek(0, 0); err != nil {
		return ""
	}
	var b bytes.Buffer
	if _, err := b.ReadFrom(f); err != nil {
		return ""
	}
	return strings.TrimSpace(b.String())
}

// HaveOS reports whether a system clipboard is reachable at all, so a caller
// can say so once at start-up rather than discovering it on the first push.
func HaveOS() bool {
	if _, _, err := pick(true); err != nil {
		return false
	}
	// A Linux helper binary with no display to talk to would fail at the point
	// of use. Checking here keeps the daemon's start-up message honest.
	if runtime.GOOS == "linux" &&
		os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") == "" {
		return false
	}
	return true
}
