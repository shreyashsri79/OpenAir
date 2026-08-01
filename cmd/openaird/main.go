// Command openaird is the OpenAir daemon: always-on, one per device.
//
// It owns this device's identity, listens for peers on QUIC, advertises itself
// on the LAN, and serves the local shells that drive it -- the CLI today, a
// tray UI later. Running it is what makes receiving a file independent of any
// terminal being open (BUILD-PLAN.md M4).
//
// Desktop shells talk to it over local IPC using the same envelope and messages
// as the network protocol, not gRPC (D-29). Android hosts the same core
// in-process via gomobile instead (D-31), so it has no IPC at all.
//
//	openaird                              # listen on :9000
//	openaird --listen :9100 --dir ./inbox
//	openaird --accept-all                 # headless: no prompt, accept everything
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/shreyashsri79/openair/internal/caps/remotefs"
	"github.com/shreyashsri79/openair/internal/daemon"
	"github.com/shreyashsri79/openair/internal/identity"
)

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("openaird: ")

	var cfg daemon.Config
	var quiet bool
	flag.StringVar(&cfg.Listen, "listen", fmt.Sprintf(":%d", daemon.DefaultPort), "address to accept peers on")
	flag.StringVar(&cfg.DestDir, "dir", defaultDestDir(), "directory to write received files into")
	flag.StringVar(&cfg.KeyDir, "keys", "", "directory holding this device's keys")
	flag.StringVar(&cfg.SocketPath, "socket", "", "local IPC socket path (default: per-user runtime directory)")
	flag.StringVar(&cfg.DisplayName, "name", "", "name other devices see (default: hostname)")
	flag.BoolVar(&cfg.AutoAccept, "accept-all", false,
		"accept every inbound transfer from a paired device without asking")
	flag.BoolVar(&cfg.NoAnnounce, "no-announce", false, "do not advertise this device on the local network")
	flag.BoolVar(&quiet, "quiet", false, "log nothing but errors")
	rendezvousAddr := flag.String("rendezvous", "",
		"rendezvous server as host:port@deviceid, so paired devices can find this one across networks")
	relayAddr := flag.String("relay", "",
		"relay server as host:port@deviceid, to stay reachable where no direct path works")
	flag.BoolVar(&cfg.AcceptInput, "accept-input", false,
		"let paired owned devices drive this machine's keyboard and pointer (M14, §13; off by default)")
	shares := flag.String("share", "",
		"directories paired Owned devices may browse and read, comma-separated (M10, §11)")
	stunServers := flag.String("stun", "",
		"STUN servers for hole punching, comma-separated (default: the rendezvous server)")
	flag.BoolVar(&cfg.AutoClipboard, "auto-clipboard", false,
		"push this machine's clipboard to connected devices as it changes, and apply theirs (M13, opt-in)")
	notifyAllow := flag.String("notify-allow", "",
		"only mirror notifications from these app ids, comma-separated (PRD R22)")
	notifyBlock := flag.String("notify-block", "",
		"mirror every notification except these app ids, comma-separated")
	flag.Parse()

	rv, err := daemon.ParseRendezvous(*rendezvousAddr)
	if err != nil {
		log.Fatal(err)
	}
	cfg.Rendezvous = rv

	relayCfg, err := daemon.ParseRelay(*relayAddr)
	if err != nil {
		log.Fatal(err)
	}
	cfg.Relay = relayCfg
	cfg.Shares = parseShares(*shares)
	cfg.NotifyAllow = splitList(*notifyAllow)
	cfg.NotifyBlock = splitList(*notifyBlock)
	cfg.STUN = splitList(*stunServers)

	if !quiet {
		cfg.Logf = func(format string, args ...any) { log.Printf(format, args...) }
	}

	// Before anything can unseal a privilege key: D-19 requires that the
	// decrypted key never reaches a core file, and on Linux this also stops a
	// same-uid debugger attaching to a process that will hold one for six
	// hours. A failure is worth saying out loud and is not fatal -- the daemon
	// still serves, and the tighter guarantee simply is not available.
	if err := identity.DisableCoreDumps(); err != nil && !quiet {
		log.Printf("could not disable core dumps: %v", err)
	}

	d, err := daemon.New(cfg)
	if err != nil {
		log.Fatal(err)
	}

	// SIGTERM as well as interrupt: a service manager stops this with the
	// former, and a daemon that ignores it is killed mid-transfer rather than
	// closing its sessions.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, sigterm())
	defer stop()

	if err := d.Run(ctx); err != nil {
		log.Fatal(err)
	}
}

// defaultDestDir is where inbound files land when --dir is not given.
func defaultDestDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	if dl := filepath.Join(home, "Downloads"); dirExists(dl) {
		return filepath.Join(dl, "OpenAir")
	}
	return filepath.Join(home, "OpenAir")
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// parseShares reads the --share list. A share is named by the base name of its
// directory, which is what a peer sees as the first path component (§11.1,
// D-72), or by "name=/path" when two shares would otherwise collide.
func parseShares(list string) []remotefs.Root {
	var out []remotefs.Root
	for _, item := range splitList(list) {
		name, dir, hasName := strings.Cut(item, "=")
		if !hasName {
			name, dir = "", item
		}
		out = append(out, remotefs.Root{Name: name, Path: dir})
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
