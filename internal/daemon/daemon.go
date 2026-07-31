// Package daemon is `openaird`: the always-on process that owns this device's
// identity, listens for peers, and serves the local shells that drive it.
//
// It exists so that receiving a file does not require a terminal to be open
// (BUILD-PLAN.md M4). Everything the CLI did inline in M1--M3 -- listen, pair,
// discover, transfer -- happens here instead, once per device rather than once
// per command, and the CLI becomes a client of it over local IPC (D-29).
//
// Two boundaries meet in this package and they are not the same boundary. The
// QUIC listener faces the network, where a peer is trusted only as far as its
// pinned key (M2). The IPC socket faces the local machine, where anything that
// can open it drives this daemon with the identity key behind it -- see
// internal/ipc for what enforces that.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/clipboard"
	"github.com/shreyashsri79/openair/internal/caps/files"
	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/discovery"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/ipc"
	"github.com/shreyashsri79/openair/internal/pairing"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// DefaultPort is the QUIC port a daemon listens on when nothing says otherwise.
const DefaultPort = 9000

// defaultPromptTimeout bounds how long the daemon waits for a human to answer.
//
// Unanswered means refused, never accepted: the daemon must not become a device
// that accepts files because nobody was watching. Pairing gets its own, longer
// budget, because §5 already allows two minutes for two people to compare six
// digits.
const (
	defaultPromptTimeout = 60 * time.Second
	pairPromptTimeout    = 2 * time.Minute
)

// Config is what New needs. Every field has a working default except the ones
// that would otherwise write files somewhere surprising.
type Config struct {
	// KeyDir holds the identity keys and the trust store. Empty means the
	// per-user config directory.
	KeyDir string

	// Listen is the QUIC address for inbound sessions, default ":9000".
	Listen string

	// DestDir is where inbound files are written.
	DestDir string

	// SocketPath is the local IPC endpoint. Empty means the platform default
	// (ipc.DefaultSocketPath).
	SocketPath string

	// DisplayName is what peers and the LAN see. Empty means the hostname.
	DisplayName string

	// AutoAccept takes the human out of the inbound path entirely. It is for
	// headless installs; the default is to ask a subscribed client and refuse
	// if none answers.
	AutoAccept bool

	// NoAnnounce keeps the daemon off the LAN announcements while still
	// browsing, for a device that should be reachable only by an address
	// someone already knows.
	NoAnnounce bool

	// PromptTimeout overrides how long a transfer prompt waits for an answer.
	PromptTimeout time.Duration

	Logf func(format string, args ...any)

	// Discovery carries the test-only knobs that keep two daemons inside one
	// host from spraying the maintainer's LAN.
	Discovery DiscoveryOptions
}

// DiscoveryOptions mirrors discovery.Config's transport switches. Nothing sets
// these from a flag; the defaults are what a user gets.
type DiscoveryOptions struct {
	DisableMDNS      bool
	DisableBroadcast bool
	UnicastPort      int
	UnicastPeers     []string
}

// Daemon is one running instance.
type Daemon struct {
	cfg     Config
	keyDir  string
	id      *identity.FileIdentity
	store   *identity.FileTrustStore
	pairs   *pairing.Handler
	files   *files.Capability
	clip    *clipboard.Capability
	ln      conn.Listener
	ipcLn   net.Listener
	disco   *discovery.Discovery
	started time.Time

	mu       sync.Mutex
	sessions map[identity.DeviceID]session.Session
	clients  map[*client]struct{}

	closeOnce sync.Once
}

// New builds a daemon and binds both of its listeners, so that Addr and
// SocketPath are answerable before Run is called. Nothing is served until Run.
func New(cfg Config) (*Daemon, error) {
	if cfg.Listen == "" {
		cfg.Listen = fmt.Sprintf(":%d", DefaultPort)
	}
	if cfg.DestDir == "" {
		cfg.DestDir = "."
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = ipc.DefaultSocketPath()
	}
	if cfg.DisplayName == "" {
		cfg.DisplayName = hostname()
	}
	if cfg.PromptTimeout <= 0 {
		cfg.PromptTimeout = defaultPromptTimeout
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}

	keyDir := cfg.KeyDir
	if keyDir == "" {
		keyDir = defaultKeyDir()
	}
	// The tier is read off the disk rather than configured (D-21, D-57). A flag
	// would let a typo take a device out of Owned silently; the sealed key file
	// already says which tier it belongs to, and `openair protect` is what
	// creates one.
	tier, err := identity.DetectTier(keyDir)
	if err != nil {
		return nil, fmt.Errorf("read privilege key: %w", err)
	}
	d := &Daemon{}
	id, err := identity.LoadOrCreate(identity.Options{
		Dir:             keyDir,
		Tier:            tier,
		OnExpiryWarning: func(t identity.DeviceID, at time.Time) { d.onExpiryWarning(t, at) },
		Logf:            func(format string, args ...any) { cfg.Logf(format, args...) },
	})
	if err != nil {
		return nil, fmt.Errorf("open identity: %w", err)
	}
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}
	store, err := identity.OpenTrustStore(filepath.Join(keyDir, "trust.json"))
	if err != nil {
		return nil, fmt.Errorf("open trust store: %w", err)
	}
	if err := os.MkdirAll(cfg.DestDir, 0o700); err != nil {
		return nil, fmt.Errorf("create destination directory: %w", err)
	}

	*d = Daemon{
		cfg:      cfg,
		keyDir:   keyDir,
		id:       id,
		store:    store,
		started:  time.Now(),
		sessions: map[identity.DeviceID]session.Session{},
		clients:  map[*client]struct{}{},
	}

	d.pairs, err = pairing.NewHandler(pairing.Config{
		Local:       id,
		Store:       store,
		DisplayName: cfg.DisplayName,
		Platform:    platform(),
		Confirm:     d.confirmPairing,
	})
	if err != nil {
		return nil, err
	}

	d.files = files.New(files.Config{
		DestRoot:   cfg.DestDir,
		Accept:     d.acceptTransfer,
		OnProgress: d.onTransferProgress,
		OnComplete: d.onTransferComplete,
	})

	// The clipboard runs on the identity key and is registered unconditionally
	// (D-20): a device with no system clipboard still accepts pushes and
	// reports them, because a headless machine having nowhere to paste is not a
	// reason for the peer's push to fail.
	d.clip = clipboard.New(clipboard.Config{
		Tag:       string(id.DeviceID()),
		OnReceive: d.onClipboardReceived,
	})

	// capID 0 is registered so a peer that revokes this device mid-session is
	// honoured while a transfer is still running (§6.1).
	handlers := map[byte]session.Handler{
		0:               d.pairs,
		files.CapID:     d.files,
		clipboard.CapID: d.clip,
	}
	d.ln, err = conn.Listen(cfg.Listen, id, cfg.DisplayName, platform(), handlers, conn.ListenOptions{
		Authorize:   d.authorize,
		PeerLookup:  d.store.Get,
		OnAuthEvent: d.onAuthEvent,
	})
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}

	d.ipcLn, err = ipc.Listen(cfg.SocketPath)
	if err != nil {
		d.ln.Close()
		return nil, err
	}
	return d, nil
}

// Addr is the QUIC address actually bound, including the port chosen when
// Listen asked for :0.
func (d *Daemon) Addr() string { return d.ln.Addr() }

// SocketPath is the local IPC endpoint clients connect to.
func (d *Daemon) SocketPath() string { return d.cfg.SocketPath }

// DeviceID is this device's identity (PROTOCOL.md §2).
func (d *Daemon) DeviceID() identity.DeviceID { return d.id.DeviceID() }

// Run serves until ctx is cancelled. It is the whole daemon: peers on QUIC,
// shells on the socket, and discovery between them.
func (d *Daemon) Run(ctx context.Context) error {
	defer d.Close()

	port := 0
	if !d.cfg.NoAnnounce {
		p, err := portOf(d.ln.Addr())
		if err != nil {
			return err
		}
		port = p
	}
	disco, err := discovery.New(discovery.Config{
		DeviceID:         d.id.DeviceID(),
		Port:             port,
		BrowseOnly:       port == 0,
		DisplayName:      d.cfg.DisplayName,
		DisableMDNS:      d.cfg.Discovery.DisableMDNS,
		DisableBroadcast: d.cfg.Discovery.DisableBroadcast,
		UnicastPort:      d.cfg.Discovery.UnicastPort,
		UnicastPeers:     d.cfg.Discovery.UnicastPeers,
	})
	if err != nil {
		// A network with no multicast and no broadcast is not a reason to
		// refuse to run: an explicit address still works, and so does every
		// device already paired and dialling in.
		d.cfg.Logf("not advertising on the local network: %v", err)
	} else {
		d.disco = disco
		defer disco.Close()
	}

	d.cfg.Logf("device %s listening on %s, writing to %s",
		d.id.DeviceID().Fingerprint(), d.ln.Addr(), d.cfg.DestDir)
	d.cfg.Logf("ipc on %s", d.cfg.SocketPath)
	switch d.id.ProtectionTier() {
	case identity.TierNone:
		// D-21 tier 3, said plainly: a user who believes they have unattended
		// access and does not is worse off than one who was told.
		d.cfg.Logf("no privilege key here: this device can pair and transfer, but cannot unlock Owned access (run `openair protect`)")
	case identity.TierPassphrase:
		d.cfg.Logf("privilege key sealed with a passphrase (tier 2)")
	case identity.TierKeystore:
		d.cfg.Logf("privilege key sealed by the platform keystore (tier 1)")
	}
	if !clipboard.HaveOS() {
		d.cfg.Logf("no system clipboard here; inbound pushes will be reported, not pasted")
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); d.acceptSessions(ctx) }()
	go func() { defer wg.Done(); d.acceptClients(ctx) }()
	go func() { defer wg.Done(); d.watchDiscovery(ctx) }()

	<-ctx.Done()
	d.Close()
	wg.Wait()
	return nil
}

// Close releases both listeners and every live session. It is safe to call
// more than once, which Run relies on.
func (d *Daemon) Close() error {
	d.closeOnce.Do(func() {
		if d.ln != nil {
			d.ln.Close()
		}
		if d.ipcLn != nil {
			d.ipcLn.Close()
		}
		// A unix socket outlives its process unless removed. Leaving it behind
		// makes the next start look like an already-running daemon.
		if runtime.GOOS != "windows" {
			_ = os.Remove(d.cfg.SocketPath)
		}
		d.mu.Lock()
		sessions := make([]session.Session, 0, len(d.sessions))
		for _, s := range d.sessions {
			sessions = append(sessions, s)
		}
		d.sessions = map[identity.DeviceID]session.Session{}
		d.mu.Unlock()
		for _, s := range sessions {
			_ = s.Close(uint16(session.CodeNoError), "daemon shutting down")
		}
	})
	return nil
}

// acceptSessions is the inbound half. A refused peer is one event, not the end
// of the loop: a daemon that stops listening because a stranger knocked has
// been denied service by anyone who can reach its port.
func (d *Daemon) acceptSessions(ctx context.Context) {
	for {
		sess, err := d.ln.Accept(ctx)
		if err != nil {
			var he *conn.HandshakeError
			if errors.As(err, &he) {
				d.cfg.Logf("refused inbound connection: %v", he)
				d.broadcast(&openairv1.DaemonEvent{
					Kind: openairv1.DaemonEventKind_DAEMON_EVENT_KIND_REFUSED,
					Text: he.Error(),
				})
				continue
			}
			if ctx.Err() == nil && !errors.Is(err, conn.ErrListenerClosed) {
				d.cfg.Logf("accept: %v", err)
			}
			return
		}
		d.register(sess)
	}
}

// register tracks a live session and drops it when it ends. Session.Done is
// what makes that possible without polling.
func (d *Daemon) register(sess session.Session) {
	peer := sess.Peer()
	d.mu.Lock()
	prev, existed := d.sessions[peer.DeviceID]
	d.sessions[peer.DeviceID] = sess
	d.mu.Unlock()

	if existed && prev != sess {
		// One session per peer (HLD §3.3). A second one means the peer
		// reconnected without the first being noticed as dead.
		_ = prev.Close(uint16(session.CodeNoError), "superseded by a new session")
	}

	d.cfg.Logf("session with %s (%s)", peer.DeviceID.Fingerprint(), peer.DisplayName)
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_OPEN,
		DeviceId: string(peer.DeviceID),
		Text:     peer.DisplayName,
	})

	go func() {
		<-sess.Done()
		d.mu.Lock()
		if d.sessions[peer.DeviceID] == sess {
			delete(d.sessions, peer.DeviceID)
		}
		d.mu.Unlock()
		d.pairs.Detach(sess)
		d.broadcast(&openairv1.DaemonEvent{
			Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_CLOSED,
			DeviceId: string(peer.DeviceID),
		})
	}()
}

// sessionFor returns a live session with a peer, if one is already up.
func (d *Daemon) sessionFor(id identity.DeviceID) (session.Session, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.sessions[id]
	return s, ok
}

// authorize is the trust-store gate on inbound peers (M2). It is
// pairing.Handler.Authorize plus the logging and event that make a refusal
// visible to whoever is watching.
func (d *Daemon) authorize(peer identity.Peer) error {
	if err := d.pairs.Authorize(peer); err != nil {
		d.cfg.Logf("refused %s: %v", peer.DeviceID.Fingerprint(), err)
		d.broadcast(&openairv1.DaemonEvent{
			Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_REFUSED,
			DeviceId: string(peer.DeviceID),
			Text:     err.Error(),
		})
		return err
	}
	return nil
}

// watchDiscovery turns discovery events into daemon events, so a subscribed UI
// sees devices appear without polling.
func (d *Daemon) watchDiscovery(ctx context.Context) {
	if d.disco == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-d.disco.Events():
			if !ok {
				return
			}
			kind := openairv1.DaemonEventKind_DAEMON_EVENT_KIND_DEVICE_FOUND
			if ev.Kind == discovery.PeerLost {
				kind = openairv1.DaemonEventKind_DAEMON_EVENT_KIND_DEVICE_LOST
			}
			d.broadcast(&openairv1.DaemonEvent{
				Kind:     kind,
				DeviceId: string(ev.Peer.DeviceID),
				Text:     ev.Peer.DisplayName,
			})
		}
	}
}

func defaultKeyDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "openair")
	}
	return ".openair"
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "openair"
	}
	return h
}

// platform reports this device's platform string for Hello (PROTOCOL.md §4,
// which enumerates exactly these four values).
func platform() string {
	switch runtime.GOOS {
	case "linux", "windows", "android", "darwin":
		return runtime.GOOS
	default:
		return "linux"
	}
}

func portOf(addr string) (int, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("listener address %q: %w", addr, err)
	}
	var n int
	if _, err := fmt.Sscanf(port, "%d", &n); err != nil {
		return 0, fmt.Errorf("listener port %q: %w", port, err)
	}
	return n, nil
}
