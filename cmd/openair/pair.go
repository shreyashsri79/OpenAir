package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/daemon"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/pairing"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

type pairOptions struct {
	listen string // non-empty: display an offer and wait for someone to scan it
	offer  string // non-empty: an offer that was scanned or typed here
	addr   string // override the address to dial; otherwise the offer's hint
	keys   string

	socket   string
	noDaemon bool

	onReady func(addr, offer string) // used by tests
}

func runPair(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var o pairOptions
	fs.StringVar(&o.listen, "listen", "", "display an offer and wait on this address (e.g. :9000)")
	fs.StringVar(&o.addr, "addr", "", "address to dial, overriding the offer's own hint")
	fs.StringVar(&o.keys, "keys", "", "directory holding this device's keys")
	fs.StringVar(&o.socket, "socket", "", "daemon IPC socket path")
	fs.BoolVar(&o.noDaemon, "no-daemon", false, "pair from this process instead of asking the daemon")
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		o.offer = rest[0]
	}

	if o.listen != "" && o.offer != "" {
		return fmt.Errorf("give either --listen or an offer to scan, not both")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// With a daemon running, bare `openair pair` is the displaying side: the
	// daemon already has a listener, so there is no address for the user to
	// choose. Direct mode has no listener until --listen says where to bind,
	// which is why the requirement returns below.
	if !o.noDaemon {
		err := pairViaDaemon(ctx, o, stdin, stdout)
		if err == nil {
			return nil
		}
		if !errors.Is(err, daemon.ErrNoDaemon) {
			return err
		}
		fmt.Fprintln(stdout, "no daemon running; pairing from this process")
	}

	if o.listen == "" && o.offer == "" {
		return fmt.Errorf("no daemon is running, so this needs an address to listen on\n" +
			"usage: openair pair --listen ADDR   (on one device)\n" +
			"       openair pair OFFER          (on the other)")
	}

	if o.listen != "" {
		return pairListen(ctx, o, stdin, stdout)
	}
	return pairScan(ctx, o, stdin, stdout)
}

// pairViaDaemon runs §5 in the daemon, which is where it belongs once one is
// running: the daemon owns the identity being pinned and the listener the peer
// dials, and a second process pairing on its behalf would pin the peer into a
// trust store the daemon is holding open.
//
// --listen is ignored in this mode and says so. The daemon already has a
// listener; asking it to bind a second one would advertise an address that
// stops existing when this command exits.
func pairViaDaemon(ctx context.Context, o pairOptions, stdin io.Reader, stdout io.Writer) error {
	var mu sync.Mutex

	// The offer has to reach the user before pairing finishes -- it is what
	// they carry to the other device -- so the daemon sends it as an event
	// rather than in the reply. An event with no DeviceID is that offer.
	onEvent := func(ev *openairv1.DaemonEvent) {
		mu.Lock()
		defer mu.Unlock()
		if ev.GetKind() == openairv1.DaemonEventKind_DAEMON_EVENT_KIND_PAIRED && ev.GetDeviceId() == "" {
			grouped := groupOffer(ev.GetText())
			fmt.Fprintf(stdout, "\nscan or type this on the other device:\n\n  %s\n\n", ev.GetText())
			fmt.Fprintf(stdout, "  (by hand: %s)\n\nwaiting for the other device...\n", grouped)
			return
		}
		if line := formatEvent(ev); line != "" {
			fmt.Fprintln(stdout, line)
		}
	}
	// Prompts, because §5.2's six digits have to reach a human and this
	// terminal is the human. Without them the daemon has nobody to ask and
	// refuses its own pairing.
	onPrompt := func(p *openairv1.DaemonPrompt) bool {
		mu.Lock()
		defer mu.Unlock()
		return askPrompt(stdin, stdout, p)
	}

	c, err := daemon.Connect(ctx, o.socket, onEvent, onPrompt)
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.Subscribe(ctx, true); err != nil {
		return err
	}
	if o.listen != "" {
		fmt.Fprintln(stdout, "the daemon is already listening; --listen is ignored")
	}

	resp, err := c.Pair(ctx, o.offer, 0)
	if err != nil {
		return fmt.Errorf("pairing: %w", err)
	}
	fmt.Fprintf(stdout, "\npaired with %s -- %s\n",
		identity.DeviceID(resp.GetDeviceId()).Fingerprint(), resp.GetDisplayName())
	fmt.Fprintln(stdout, "transfers between these two devices no longer need a fingerprint check.")
	return nil
}

// pairListen is device A in PROTOCOL.md §5.1: it displays the offer and waits.
//
// The pairing window is open only while this command is running, which is the
// whole design -- an unpaired peer can establish a session at all only because
// a user is standing here having asked for it.
func pairListen(ctx context.Context, o pairOptions, stdin io.Reader, stdout io.Writer) error {
	id, store, handler, err := pairingParts(o.keys, stdin, stdout)
	if err != nil {
		return err
	}
	_ = store

	ln, err := conn.Listen(o.listen, id, hostname(), platform(),
		map[byte]session.Handler{0: handler}, conn.ListenOptions{Authorize: handler.Authorize})
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer ln.Close()

	closeWindow := handler.OpenWindow()
	defer closeWindow()

	offer, err := pairing.NewOffer(id, []string{ln.Addr()})
	if err != nil {
		return err
	}
	encoded, err := pairing.EncodeOffer(offer)
	if err != nil {
		return err
	}
	grouped, err := pairing.EncodeOfferGrouped(offer)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "this device: %s\n", fingerprint(id.DeviceID()))
	fmt.Fprintf(stdout, "listening on %s\n\n", ln.Addr())
	fmt.Fprintf(stdout, "scan or type this on the other device:\n\n  %s\n\n", encoded)
	fmt.Fprintf(stdout, "  (by hand: %s)\n\n", grouped)
	fmt.Fprintln(stdout, "waiting for the other device...")
	if o.onReady != nil {
		o.onReady(ln.Addr(), encoded)
	}

	sess, err := ln.Accept(ctx)
	if err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	defer sess.Close(0, "pairing done")

	peer, err := handler.Await(ctx, sess)
	if err != nil {
		return fmt.Errorf("pairing: %w", err)
	}
	reportPaired(stdout, peer)
	return nil
}

// pairScan is device B in §5.1: it consumes the offer, dials, and checks that
// the key the far end presents is the one the offer named before anything else
// happens.
func pairScan(ctx context.Context, o pairOptions, stdin io.Reader, stdout io.Writer) error {
	id, store, handler, err := pairingParts(o.keys, stdin, stdout)
	if err != nil {
		return err
	}
	_ = store

	offer, err := pairing.DecodeOffer(o.offer)
	if err != nil {
		return err
	}

	addr := o.addr
	if addr == "" {
		if len(offer.LanHints) == 0 {
			return fmt.Errorf("the offer carries no address; pass --addr host:port")
		}
		addr = offer.LanHints[0]
	}

	fmt.Fprintf(stdout, "this device: %s\n", fingerprint(id.DeviceID()))
	fmt.Fprintf(stdout, "pairing with %s at %s\n", fingerprint(identity.DeviceID(offer.DeviceId)), addr)

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// No pinned key: during pairing TLS authenticates nothing. VerifyOffer,
	// inside Initiate, is what checks the presented key against the fingerprint
	// that was scanned (§5.1).
	sess, err := conn.NewDialer(id, hostname(), platform(), map[byte]session.Handler{0: handler}).
		DialAddr(dialCtx, addr, identity.Peer{})
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer sess.Close(0, "pairing done")

	peer, err := handler.Initiate(ctx, sess, offer)
	if err != nil {
		return fmt.Errorf("pairing: %w", err)
	}
	reportPaired(stdout, peer)
	return nil
}

func pairingParts(keys string, stdin io.Reader, stdout io.Writer) (
	*identity.FileIdentity, *identity.FileTrustStore, *pairing.Handler, error) {

	id, err := loadIdentity(keys)
	if err != nil {
		return nil, nil, nil, err
	}
	store, err := loadTrustStore(keys)
	if err != nil {
		return nil, nil, nil, err
	}
	handler, err := newPairingHandler(id, store, stdin, stdout)
	if err != nil {
		return nil, nil, nil, err
	}
	return id, store, handler, nil
}

func reportPaired(out io.Writer, peer identity.Peer) {
	name := peer.DisplayName
	if name == "" {
		name = "(unnamed)"
	}
	fmt.Fprintf(out, "\npaired with %s -- %s on %s\n", fingerprint(peer.DeviceID), name, peer.Platform)
	fmt.Fprintln(out, "transfers between these two devices no longer need a fingerprint check.")
}
