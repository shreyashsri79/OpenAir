// Command openair-rendezvous is a rendezvous server (PROTOCOL.md §16).
//
// It maps a DeviceID to the endpoints that device last published, so two paired
// devices on different networks can find each other. It is one process, one
// port and one map: there is no database, because every entry expires within
// ten minutes and a device that matters is heartbeating.
//
// Run it, then give the DeviceID it prints to the devices that will use it —
// clients pin the server by that ID exactly as they pin each other (§2), so the
// address alone is not enough to impersonate it.
//
//	openair-rendezvous --listen :9443 --keys /var/lib/openair-rendezvous
//
// What the operator learns is written in §16 and is not nothing: which
// DeviceIDs exist, their IP endpoints, when they are online, and who looks up
// whom. It is never session content, because none passes through here. Anyone
// who considers the metadata significant should run their own, which is the
// reason this binary is small enough to.
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
	"github.com/shreyashsri79/openair/internal/path"
	"github.com/shreyashsri79/openair/internal/rendezvous"
)

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("openair-rendezvous: ")

	listen := flag.String("listen", ":9443", "address to serve on")
	keyDir := flag.String("keys", "", "directory holding this server's key (default: ./openair-rendezvous-keys)")
	quiet := flag.Bool("quiet", false, "log nothing but errors")
	flag.Parse()

	dir := *keyDir
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			log.Fatalf("working directory: %v", err)
		}
		dir = filepath.Join(cwd, "openair-rendezvous-keys")
	}

	// The server holds an identity key like any other device and no privilege
	// key at all: it initiates nothing, so there is nothing for one to gate.
	id, err := identity.LoadOrCreate(identity.Options{Dir: dir, Tier: identity.TierNone})
	if err != nil {
		log.Fatalf("open key directory %s: %v", dir, err)
	}

	logf := func(format string, args ...any) { log.Printf(format, args...) }
	if *quiet {
		logf = func(string, ...any) {}
	}

	srv, err := rendezvous.NewServer(rendezvous.Config{Local: id, Logf: logf})
	if err != nil {
		log.Fatalf("build server: %v", err)
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen on %s: %v", *listen, err)
	}

	// The same port, over UDP, answers STUN Binding requests, so a device that
	// has a rendezvous server has a reflexive address without having to trust
	// anybody else's STUN server (M9, D-68). It is a plain RFC 8489 responder;
	// failing to bind it costs punching behind NAT and nothing else, so it is
	// a warning rather than a fatal error.
	stunConn, stunErr := net.ListenPacket("udp", *listen)

	fmt.Printf("rendezvous server on %s\n", ln.Addr())
	fmt.Printf("device id  %s\n", id.DeviceID())
	fmt.Printf("fingerprint %s\n", id.DeviceID().Fingerprint())
	fmt.Printf("\nPoint devices at it with:\n  openaird --rendezvous %s@%s\n", *listen, id.DeviceID())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if stunErr != nil {
		log.Printf("not answering STUN on %s: %v (devices behind NAT will stay on the relay)", *listen, stunErr)
	} else {
		defer stunConn.Close()
		fmt.Printf("answering STUN on %s/udp\n", stunConn.LocalAddr())
		go func() {
			if err := path.ServeSTUN(ctx, stunConn); err != nil {
				log.Printf("stun: %v", err)
			}
		}()
	}

	if err := srv.Serve(ctx, ln); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
