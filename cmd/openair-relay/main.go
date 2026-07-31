// Command openair-relay is a relay server (PROTOCOL.md §17).
//
// It forwards ciphertext between paired devices whose networks will not let
// them reach each other directly. Every payload it moves is a complete QUIC
// packet, so it cannot read what it carries and could not participate in a
// session if it tried: end-to-end encryption is identical whether a path is
// relayed or direct (PRD R27).
//
// Run it, then give the DeviceID it prints to the devices that will use it —
// clients pin the relay by that ID exactly as they pin each other (§2).
//
//	openair-relay --listen :9444 --keys /var/lib/openair-relay
//
// What the operator learns, stated plainly for the threat model: which
// DeviceIDs talk to each other, when, and how much. Not content. Same
// self-hosting argument as the rendezvous server, and the same reason this
// binary is small enough to run yourself.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/relay"
)

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("openair-relay: ")

	listen := flag.String("listen", ":9444", "address to serve on")
	keyDir := flag.String("keys", "", "directory holding this relay's key (default: ./openair-relay-keys)")
	quiet := flag.Bool("quiet", false, "log nothing but errors")
	flag.Parse()

	dir := *keyDir
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			log.Fatalf("working directory: %v", err)
		}
		dir = filepath.Join(cwd, "openair-relay-keys")
	}

	// The relay holds an identity key and no privilege key: it initiates
	// nothing, so there is nothing for one to gate. It holds no key belonging
	// to any client at all, which is the property the whole design rests on.
	id, err := identity.LoadOrCreate(identity.Options{Dir: dir, Tier: identity.TierNone})
	if err != nil {
		log.Fatalf("open key directory %s: %v", dir, err)
	}

	logf := func(format string, args ...any) { log.Printf(format, args...) }
	if *quiet {
		logf = func(string, ...any) {}
	}

	srv, err := relay.NewServer(relay.Config{Local: id, Logf: logf})
	if err != nil {
		log.Fatalf("build relay: %v", err)
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen on %s: %v", *listen, err)
	}

	fmt.Printf("relay on %s\n", ln.Addr())
	fmt.Printf("device id  %s\n", id.DeviceID())
	fmt.Printf("fingerprint %s\n", id.DeviceID().Fingerprint())
	fmt.Printf("\nPoint devices at it with:\n  openaird --relay %s@%s\n", *listen, id.DeviceID())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Serve(ctx, ln); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
