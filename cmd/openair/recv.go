package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/shreyashsri79/openair/internal/caps/files"
	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

type recvOptions struct {
	listen  string
	dir     string
	keys    string
	yes     bool
	once    bool              // stop after one completed transfer; used by tests
	onReady func(addr string) // called with the bound address; used by tests
	onDone  func(id string, ok bool)
}

func runRecv(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("recv", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var o recvOptions
	fs.StringVar(&o.listen, "listen", ":9000", "address to listen on")
	fs.StringVar(&o.dir, "dir", ".", "directory to write received files into")
	fs.StringVar(&o.keys, "keys", "", "directory holding this device's keys")
	fs.BoolVar(&o.yes, "yes", false, "accept any peer and any offer without asking")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return receive(ctx, o, stdin, stdout)
}

func receive(ctx context.Context, o recvOptions, stdin io.Reader, stdout io.Writer) error {
	id, err := loadIdentity(o.keys)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(o.dir, 0o700); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	done := make(chan bool, 1)
	cap := files.New(files.Config{
		DestRoot: o.dir,
		Accept: func(_ context.Context, peer identity.Peer, offer files.Offer) (bool, error) {
			fmt.Fprintf(stdout, "\n%s offers %d file(s), %s\n",
				fingerprint(peer.DeviceID), len(offer.Files), humanBytes(offer.TotalBytes))
			for _, f := range offer.Files {
				fmt.Fprintf(stdout, "  %s  %s\n", f.GetPath(), humanBytes(f.GetSize()))
			}
			if o.yes {
				return true, nil
			}
			return confirm(stdin, stdout, "accept this transfer?"), nil
		},
		OnProgress: func(p files.Progress) {
			fmt.Fprintf(stdout, "\rreceiving %s / %s", humanBytes(p.BytesReceived), humanBytes(p.TotalBytes))
		},
		OnComplete: func(transferID string, ok bool) {
			if ok {
				fmt.Fprintf(stdout, "\ntransfer %s complete\n", transferID)
			} else {
				fmt.Fprintf(stdout, "\ntransfer %s failed\n", transferID)
			}
			if o.onDone != nil {
				o.onDone(transferID, ok)
			}
			select {
			case done <- ok:
			default:
			}
		},
	})

	// The peer gate. M1 pins nothing in advance, so this is where an inbound
	// device is either recognised by its fingerprint or turned away
	// (session.Config.Authorize).
	authorize := func(peer identity.Peer) error {
		fmt.Fprintf(stdout, "\ninbound connection from %s\n", fingerprint(peer.DeviceID))
		if peer.DisplayName != "" {
			fmt.Fprintf(stdout, "  name: %s  platform: %s\n", peer.DisplayName, peer.Platform)
		}
		if o.yes || confirm(stdin, stdout, "does that fingerprint match the sending device?") {
			return nil
		}
		return fmt.Errorf("fingerprint not confirmed by the user")
	}

	ln, err := conn.Listen(o.listen, id, hostname(), platform(),
		map[byte]session.Handler{files.CapID: cap}, authorize)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer ln.Close()

	fmt.Fprintf(stdout, "this device: %s\n", fingerprint(id.DeviceID()))
	fmt.Fprintf(stdout, "listening on %s, writing to %s\n", ln.Addr(), o.dir)
	if o.onReady != nil {
		o.onReady(ln.Addr())
	}

	// Accept in the background: it blocks through the peer's Hello exchange, so
	// running it inline would let one silent peer hold up the whole loop.
	accepted := make(chan error, 1)
	go func() {
		for {
			sess, err := ln.Accept(ctx)
			if err != nil {
				accepted <- err
				return
			}
			fmt.Fprintf(stdout, "session established with %s\n", fingerprint(sess.Peer().DeviceID))
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ok := <-done:
			if o.once {
				if !ok {
					return fmt.Errorf("transfer failed")
				}
				return nil
			}
		case err := <-accepted:
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "openair"
	}
	return h
}
