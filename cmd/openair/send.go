package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/files"
	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/daemon"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

// sendOptions has no "accept the fingerprint" flag any more: M2 answers that
// question from the trust store, and a device that is not paired is refused
// rather than prompted about.
type sendOptions struct {
	// addr is what the user typed: either an explicit host:port, or a device
	// name or fingerprint prefix to resolve on the local network (M3).
	addr    string
	paths   []string
	keys    string
	timeout time.Duration // how long to look for a named device; zero means 5s

	// socket is the daemon to drive; empty means the platform default.
	// noDaemon forces this process to dial for itself even when one is running.
	socket   string
	noDaemon bool

	disco discoveryOptions // test-only; see discoveryOptions
}

func runSend(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var o sendOptions
	fs.StringVar(&o.keys, "keys", "", "directory holding this device's keys")
	fs.DurationVar(&o.timeout, "timeout", 5*time.Second, "how long to look for a named device on the network")
	fs.StringVar(&o.socket, "socket", "", "daemon IPC socket path")
	fs.BoolVar(&o.noDaemon, "no-daemon", false, "dial from this process instead of asking the daemon")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("usage: openair send [flags] FILE... DEVICE|ADDR")
	}
	o.paths, o.addr = rest[:len(rest)-1], rest[len(rest)-1]

	return send(context.Background(), o, stdin, stdout)
}

// send offers files, through the daemon when one is running.
//
// The fallback is not a convenience: a daemon holds this device's identity and
// its open sessions, so sending around it would dial a second connection from a
// second copy of the same identity. Direct mode exists for the machine that has
// no daemon, and saying which mode is in use matters because the two write
// their received files to different places.
func send(ctx context.Context, o sendOptions, stdin io.Reader, stdout io.Writer) error {
	if !o.noDaemon {
		err := sendViaDaemon(ctx, o, stdout)
		if err == nil {
			return nil
		}
		if !errors.Is(err, daemon.ErrNoDaemon) {
			return err
		}
		fmt.Fprintln(stdout, "no daemon running; dialling from this process")
	}
	return sendDirect(ctx, o, stdin, stdout)
}

// sendViaDaemon hands the whole transfer to the daemon and prints what it
// reports back.
func sendViaDaemon(ctx context.Context, o sendOptions, stdout io.Writer) error {
	c, err := connectDaemon(ctx, o.socket, nil, stdout, false)
	if err != nil {
		return err
	}
	defer c.Close()

	// Events, but no prompts: sending asks this side no questions, and
	// subscribing for prompts would make the daemon route an inbound transfer's
	// approval to a command that is about to exit.
	if err := c.Subscribe(ctx, false); err != nil {
		return err
	}

	resp, err := c.Send(ctx, o.addr, o.paths)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "transfer %s to %s complete\n",
		resp.GetTransferId(), identity.DeviceID(resp.GetDeviceId()).Fingerprint())
	return nil
}

func sendDirect(ctx context.Context, o sendOptions, stdin io.Reader, stdout io.Writer) error {
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

	addrs, err := targetAddrs(ctx, o, id, stdout)
	if err != nil {
		return err
	}

	d := conn.NewDialer(id, hostname(), platform(),
		map[byte]session.Handler{files.CapID: cap, 0: pairHandler})

	// An address is not an identity, so there is nothing to pin before the
	// handshake: the key is learned from it and checked against the trust store
	// immediately afterwards. identity.Peer's zero value is what session.New
	// treats as "unpinned".
	//
	// A discovered device may advertise several addresses -- wired and
	// wireless, IPv4 and IPv6 -- and only trying them in turn tells us which
	// one is actually routable from here.
	sess, err := dialFirst(ctx, d, addrs)
	if err != nil {
		return err
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

// targetAddrs turns the target the user gave into addresses to try, starting
// discovery only when it is actually needed.
func targetAddrs(ctx context.Context, o sendOptions, id *identity.FileIdentity, stdout io.Writer) ([]string, error) {
	if isHostPort(o.addr) {
		return []string{o.addr}, nil
	}

	// Browse only: this process is not accepting sessions, so it has no port
	// worth announcing.
	d, err := startDiscovery(id.DeviceID(), 0, o.disco, nil)
	if err != nil {
		return nil, fmt.Errorf("%q is not a host:port and discovery is unavailable: %w", o.addr, err)
	}
	defer d.Close()

	timeout := o.timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return resolveTarget(lookupCtx, d, o.addr, stdout)
}

// dialFirst tries each address in turn and returns the first session that comes
// up.
//
// A key mismatch stops the loop immediately rather than moving to the next
// address: it means the device answering is not the one we pinned, and trying
// its other advertised addresses would just ask the same impostor twice
// (PROTOCOL.md §2).
func dialFirst(ctx context.Context, d conn.Dialer, addrs []string) (session.Session, error) {
	var lastErr error
	for _, addr := range addrs {
		sess, err := d.DialAddr(ctx, addr, identity.Peer{})
		if err == nil {
			return sess, nil
		}
		if errors.Is(err, identity.ErrKeyMismatch) {
			return nil, fmt.Errorf("dial %s: %w", addr, err)
		}
		lastErr = fmt.Errorf("dial %s: %w", addr, err)
	}
	return nil, lastErr
}
