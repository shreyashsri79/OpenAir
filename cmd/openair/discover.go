package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/shreyashsri79/openair/internal/discovery"
	"github.com/shreyashsri79/openair/internal/identity"
)

type discoverOptions struct {
	keys   string
	window time.Duration
	watch  bool

	disco discoveryOptions // test-only; see discoveryOptions
}

func runDiscover(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var o discoverOptions
	fs.StringVar(&o.keys, "keys", "", "directory holding this device's keys")
	fs.DurationVar(&o.window, "for", 3*time.Second, "how long to listen before printing")
	fs.BoolVar(&o.watch, "watch", false, "keep printing changes until interrupted")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return discover(ctx, o, stdout)
}

// discover lists the devices on this network. It announces nothing: a process
// that is not accepting sessions has no port to advertise.
func discover(ctx context.Context, o discoverOptions, stdout io.Writer) error {
	id, err := loadIdentity(o.keys)
	if err != nil {
		return err
	}
	store, err := loadTrustStore(o.keys)
	if err != nil {
		return err
	}

	d, err := startDiscovery(id.DeviceID(), 0, o.disco, nil)
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	defer d.Close()

	fmt.Fprintf(stdout, "this device: %s\n", fingerprint(id.DeviceID()))

	if o.watch {
		fmt.Fprintln(stdout, "watching for devices; interrupt to stop")
		for {
			select {
			case <-ctx.Done():
				return nil
			case ev, ok := <-d.Events():
				if !ok {
					return nil
				}
				fmt.Fprintf(stdout, "%-7s %s\n", ev.Kind, describePeer(store, ev.Peer))
			}
		}
	}

	window := o.window
	if window <= 0 {
		window = 3 * time.Second
	}
	select {
	case <-ctx.Done():
	case <-time.After(window):
	}

	peers := d.Peers()
	if len(peers) == 0 {
		fmt.Fprintf(stdout, "\nno devices found in %s.\n", window)
		fmt.Fprintln(stdout, "the other device needs `openair recv` running, on the same network.")
		return nil
	}

	fmt.Fprintf(stdout, "\n%d device(s):\n\n", len(peers))
	for _, p := range peers {
		fmt.Fprintf(stdout, "  %s\n", describePeer(store, p))
	}
	fmt.Fprintln(stdout, "\nsend to one with: openair send FILE <name or fingerprint>")
	return nil
}

// describePeer renders one candidate, saying whether it is paired.
//
// That last part is the only judgement this command makes, and it comes from
// the local trust store rather than from anything the peer said: a candidate is
// an unauthenticated hint, so "paired" here means "we hold a key for this
// DeviceID", not "this device proved anything".
func describePeer(store identity.TrustStore, p discovery.PeerCandidate) string {
	name := p.DisplayName
	if name == "" {
		name = "(unnamed)"
	}
	status := "not paired"
	if peer, ok := store.Get(p.DeviceID); ok && peer.Level > identity.LevelUnpaired {
		status = "paired"
	}
	addr := "(no address)"
	if len(p.Addrs) > 0 {
		addr = p.Addrs[0]
	}
	return fmt.Sprintf("%-20s %s  %-21s %-10s via %s", name, fingerprint(p.DeviceID), addr, status, p.Via)
}
