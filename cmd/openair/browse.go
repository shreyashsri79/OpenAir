package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/shreyashsri79/openair/internal/daemon"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// `openair ls` and `openair get`: M10's surface (§11).
//
// Both go through the daemon and only through it. Browsing needs Owned, which
// needs the privilege key to sign a proof, and that key exists in the daemon
// and nowhere else (D-20) -- so unlike send there is no --no-daemon path here,
// and pretending otherwise would produce a command that always failed.

// runLs lists what a device shares, or a directory inside a share.
func runLs(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	fs.SetOutput(stdout)
	socket := fs.String("socket", "", "daemon IPC socket path")
	long := fs.Bool("l", false, "show size, modification time and type")
	all := fs.Bool("all", false, "page through the whole directory rather than the first screenful")
	timeout := fs.Duration("timeout", 60*time.Second, "how long to spend")
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: openair ls DEVICE [PATH] [-l] [--all]")
	}
	device := rest[0]
	remote := ""
	if len(rest) > 1 {
		remote = rest[1]
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := daemon.Connect(ctx, *socket, nil, nil)
	if err != nil {
		return fmt.Errorf("%w\nbrowsing needs the daemon: the key that proves who you are lives there", err)
	}
	defer c.Close()

	limit := 256
	offset := 0
	for {
		entries, truncated, err := c.Browse(ctx, device, remote, offset, limit)
		if err != nil {
			return browseError(device, err)
		}
		if offset == 0 && len(entries) == 0 {
			if remote == "" {
				fmt.Fprintf(stdout, "%s shares nothing\n", device)
			} else {
				fmt.Fprintf(stdout, "%s is empty\n", remote)
			}
			return nil
		}
		for _, e := range entries {
			printEntry(stdout, e, *long)
		}
		offset += len(entries)
		if !truncated {
			return nil
		}
		if !*all {
			fmt.Fprintf(stdout, "\n... and more; use --all to page through the rest (%d shown)\n", offset)
			return nil
		}
		if len(entries) == 0 {
			return nil
		}
	}
}

func printEntry(stdout io.Writer, e *openairv1.FileStat, long bool) {
	name := path.Base(e.GetPath())
	if e.GetIsDir() {
		name += "/"
	}
	if !long {
		fmt.Fprintln(stdout, name)
		return
	}
	kind := e.GetMime()
	if e.GetIsDir() {
		kind = "directory"
	}
	modified := ""
	if ms := e.GetModifiedAt(); ms > 0 {
		modified = time.UnixMilli(ms).Format("2006-01-02 15:04")
	}
	fmt.Fprintf(stdout, "%10s  %-16s  %-24s  %s\n", humanBytes(e.GetSize()), modified, truncate(kind, 24), name)
}

// runGet copies a remote file here, whole or in part.
func runGet(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.SetOutput(stdout)
	socket := fs.String("socket", "", "daemon IPC socket path")
	out := fs.String("out", "", "local path to write (default: the file's own name, here)")
	offset := fs.Uint64("offset", 0, "byte offset to start at")
	length := fs.Uint64("length", 0, "bytes to fetch; 0 means to the end of the file")
	timeout := fs.Duration("timeout", 30*time.Minute, "how long to spend")
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: openair get DEVICE PATH [--out FILE] [--offset N] [--length N]")
	}
	device, remote := rest[0], rest[1]

	dest := *out
	if dest == "" {
		dest = path.Base(remote)
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := daemon.Connect(ctx, *socket, nil, nil)
	if err != nil {
		return fmt.Errorf("%w\nfetching needs the daemon: the key that proves who you are lives there", err)
	}
	defer c.Close()

	written, err := c.Fetch(ctx, device, remote, abs, *offset, *length)
	if err != nil {
		return browseError(device, err)
	}
	fmt.Fprintf(stdout, "%s -> %s (%s)\n", remote, abs, humanBytes(written))
	return nil
}

// browseError turns the two refusals a user can act on into advice.
func browseError(device string, err error) error {
	switch {
	case strings.Contains(err.Error(), "the path may not be shared"):
		return fmt.Errorf("%w\n\ncheck the path with `openair ls %s`, and that the other device still lists this one as owned", err, device)
	case strings.Contains(err.Error(), "unlock for that device first"):
		return fmt.Errorf("%w\n\nrun `openair unlock %s` first: browsing is Owned-level, "+
			"so the other device needs proof that someone authenticated here", err, device)
	case strings.Contains(err.Error(), "not sharing"), strings.Contains(err.Error(), "not shared"):
		return fmt.Errorf("%w\n\nthe other device shares nothing: start its daemon with "+
			"`openaird --share /path/to/dir`", err)
	}
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
