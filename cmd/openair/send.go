package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/shreyashsri79/openair/internal/caps/files"
	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

type sendOptions struct {
	addr  string
	paths []string
	keys  string
	yes   bool
}

func runSend(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var o sendOptions
	fs.StringVar(&o.keys, "keys", "", "directory holding this device's keys")
	fs.BoolVar(&o.yes, "yes", false, "accept the peer's fingerprint without asking")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("usage: openair send [flags] FILE... ADDR")
	}
	o.paths, o.addr = rest[:len(rest)-1], rest[len(rest)-1]

	return send(context.Background(), o, stdin, stdout)
}

func send(ctx context.Context, o sendOptions, stdin io.Reader, stdout io.Writer) error {
	id, err := loadIdentity(o.keys)
	if err != nil {
		return err
	}

	items := make([]files.Item, 0, len(o.paths))
	for _, p := range o.paths {
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		if st.IsDir() {
			// Directory trees are a §8.1 offer of many FileMeta; the CLI has no
			// walk yet and silently sending nothing would be worse than saying so.
			return fmt.Errorf("%s is a directory; directory transfer is not implemented yet", p)
		}
		items = append(items, files.Item{LocalPath: p, RelPath: filepath.Base(p)})
	}

	cap := files.New(files.Config{
		OnProgress: func(p files.Progress) {
			fmt.Fprintf(stdout, "\rsending %s / %s", humanBytes(p.BytesReceived), humanBytes(p.TotalBytes))
		},
	})

	fmt.Fprintf(stdout, "this device: %s\n", fingerprint(id.DeviceID()))

	d := conn.NewDialer(id, hostname(), platform(), map[byte]session.Handler{files.CapID: cap})

	// Nothing is pinned: M1 has no trust store to look a peer up in, so the
	// key is learned from the handshake and shown to the user afterwards.
	// identity.Peer's zero value is what session.New treats as "unpinned".
	sess, err := d.DialAddr(ctx, o.addr, identity.Peer{})
	if err != nil {
		return fmt.Errorf("dial %s: %w", o.addr, err)
	}
	defer sess.Close(0, "done")

	peer := sess.Peer()
	fmt.Fprintf(stdout, "connected to %s\n", fingerprint(peer.DeviceID))
	if peer.DisplayName != "" {
		fmt.Fprintf(stdout, "  name: %s  platform: %s\n", peer.DisplayName, peer.Platform)
	}
	if !o.yes && !confirm(stdin, stdout, "does that fingerprint match the receiving device?") {
		return fmt.Errorf("fingerprint not confirmed; nothing was sent")
	}

	transferID, err := cap.Send(ctx, sess, items)
	if err != nil {
		return fmt.Errorf("transfer %s: %w", transferID, err)
	}
	fmt.Fprintf(stdout, "\ntransfer %s complete\n", transferID)
	return nil
}
