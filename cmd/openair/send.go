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

// sendOptions has no "accept the fingerprint" flag any more: M2 answers that
// question from the trust store, and a device that is not paired is refused
// rather than prompted about.
type sendOptions struct {
	addr  string
	paths []string
	keys  string
}

func runSend(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var o sendOptions
	fs.StringVar(&o.keys, "keys", "", "directory holding this device's keys")
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

	store, err := loadTrustStore(o.keys)
	if err != nil {
		return err
	}
	pairHandler, err := newPairingHandler(id, store, stdin, stdout)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "this device: %s\n", fingerprint(id.DeviceID()))

	d := conn.NewDialer(id, hostname(), platform(),
		map[byte]session.Handler{files.CapID: cap, 0: pairHandler})

	// An address is not an identity, so there is nothing to pin before the
	// handshake: the key is learned from it and checked against the trust store
	// immediately afterwards. identity.Peer's zero value is what session.New
	// treats as "unpinned".
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

	// M2's rule, on the sending side: files go to devices this one paired with,
	// and to no others. The receiving end enforces the same thing independently.
	if err := requirePaired(pairHandler, peer); err != nil {
		return fmt.Errorf("%w; nothing was sent", err)
	}

	transferID, err := cap.Send(ctx, sess, items)
	if err != nil {
		return fmt.Errorf("transfer %s: %w", transferID, err)
	}
	fmt.Fprintf(stdout, "\ntransfer %s complete\n", transferID)
	return nil
}
