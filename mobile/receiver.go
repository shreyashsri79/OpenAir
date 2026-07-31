package mobile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/shreyashsri79/openair/internal/caps/clipboard"
	"github.com/shreyashsri79/openair/internal/caps/files"
	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/pairing"
	"github.com/shreyashsri79/openair/internal/session"
)

// DefaultListenAddr is the address Receiver.Start binds when given "".
// Port 9000 matches the CLI's default so a desktop `openair send` needs no
// flag to reach a phone.
const DefaultListenAddr = ":9000"

// ErrNotRunning reports an operation on a stopped Receiver.
var ErrNotRunning = errors.New("mobile: receiver is not running")

// ErrAlreadyRunning reports Start on a Receiver that is already listening.
var ErrAlreadyRunning = errors.New("mobile: receiver is already running")

// Receiver listens for inbound transfers and writes them into a directory.
//
// Start returns as soon as the socket is bound; the accept loop runs on a Go
// goroutine and reaches the shell only through the callbacks. Install the
// callbacks before calling Start — a peer can connect on the next line.
type Receiver struct {
	id          *Identity
	displayName string
	destRoot    string

	mu         sync.Mutex
	progress   ProgressCallback
	peerVerify PeerVerifier
	offerVerif OfferVerifier
	onDone     TransferCallback
	onErr      ErrorCallback
	onSession  SessionCallback

	clip *Clipboard

	ln     conn.Listener
	cancel context.CancelFunc
	addr   string
}

// NewReceiver returns a Receiver that writes inbound files under destRoot.
//
// destRoot must be a directory this process can write; on Android that is
// app-private storage (Context.filesDir, or getExternalFilesDir for something
// the user can find with a file manager). It is created if absent. Every
// offered path is resolved under it and anything escaping it is refused by the
// core, so destRoot is a real boundary, not a hint.
func NewReceiver(id *Identity, displayName string, destRoot string) *Receiver {
	return &Receiver{id: id, displayName: displayName, destRoot: destRoot}
}

// SetProgressCallback installs the progress sink.
func (r *Receiver) SetProgressCallback(cb ProgressCallback) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = cb
}

// SetPeerVerifier installs an optional extra prompt for inbound peers.
//
// The trust store already refused everything unpaired before this runs (M2), so
// with none set a paired device is admitted unattended — which is the point of
// having paired. Set one when the product wants to ask anyway.
func (r *Receiver) SetPeerVerifier(v PeerVerifier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.peerVerify = v
}

// SetOfferVerifier installs the per-transfer accept prompt. With none set,
// every offer is refused.
func (r *Receiver) SetOfferVerifier(v OfferVerifier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.offerVerif = v
}

// SetTransferCallback installs the completion sink.
func (r *Receiver) SetTransferCallback(cb TransferCallback) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onDone = cb
}

// SetErrorCallback installs the sink for failures in the accept loop.
func (r *Receiver) SetErrorCallback(cb ErrorCallback) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onErr = cb
}

// SetClipboard registers a Clipboard on this receiver, so a paired device can
// push content to it (§9, M5).
//
// It is separate from NewReceiver because the clipboard is optional and
// independent: a shell that only receives files registers nothing, and a shell
// that wants both shares one Clipboard between pushing and receiving. Call it
// before Start -- registration happens when the listener is built, and a peer
// can push on the next line.
func (r *Receiver) SetClipboard(c *Clipboard) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clip = c
}

func (r *Receiver) clipboard() *Clipboard {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clip
}

// SetSessionCallback installs the sink for session establishment.
func (r *Receiver) SetSessionCallback(cb SessionCallback) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onSession = cb
}

// Start binds listenAddr and begins accepting. Pass "" for DefaultListenAddr,
// or ":0" to let the OS choose a port, which Addr then reports.
func (r *Receiver) Start(listenAddr string) error {
	if listenAddr == "" {
		listenAddr = DefaultListenAddr
	}

	r.mu.Lock()
	if r.ln != nil {
		r.mu.Unlock()
		return ErrAlreadyRunning
	}
	r.mu.Unlock()

	if err := os.MkdirAll(r.destRoot, 0o700); err != nil {
		return fmt.Errorf("mobile: create destination directory: %w", err)
	}

	cap := files.New(files.Config{
		DestRoot: r.destRoot,
		Accept: func(ctx context.Context, peer identity.Peer, offer files.Offer) (bool, error) {
			// Unattended acceptance, M6's point: an Owned peer that proved
			// possession of its privilege key for this offer does not need the
			// phone unlocked and a human looking at it (§6, PRD R3). The proof
			// was verified by the session layer before this call.
			if peer.Level == identity.LevelOwned && session.OwnedFromContext(ctx) {
				return true, nil
			}
			v := r.offerVerifier()
			if v == nil {
				return false, nil
			}
			return v.VerifyOffer(newPeerInfo(peer), newOffer(offer)), nil
		},
		OnProgress: func(p files.Progress) {
			if cb := r.progressCallback(); cb != nil {
				t := int64(p.TotalBytes)
				if t == 0 {
					t = -1
				}
				cb.OnProgress(p.TransferID, int64(p.BytesReceived), t)
			}
		},
		OnComplete: func(transferID string, ok bool) {
			if cb := r.transferCallback(); cb != nil {
				cb.OnComplete(transferID, ok)
			}
		},
	})

	// The peer gate. It runs inside session establishment, once Hello has
	// revealed who is calling and before any capability message is dispatched
	// (session.Config.Authorize).
	//
	// M2 made this the trust store's decision: a device that was never paired
	// is turned away, and no UI callback can talk past it. The PeerVerifier is
	// now an optional second opinion on top -- a phone that wants to ask "let
	// desktop-home in?" every time can, and one that does not sets nothing and
	// admits its paired devices unattended. A nil verifier no longer means
	// refuse-everyone, because the pairing is what makes unattended receiving
	// safe in the first place.
	pairHandler, err := pairing.NewHandler(pairing.Config{
		Local:       r.id.impl,
		Store:       r.id.store,
		DisplayName: r.displayName,
		Platform:    PlatformName,
		// Receiving never pairs: this handler is here for the trust-store gate
		// and for §6.1 revocation arriving mid-transfer. Pairing has its own
		// screen and its own listener (see Pairing).
		Confirm: func(context.Context, string, pairing.PeerInfo) (bool, error) {
			return false, nil
		},
	})
	if err != nil {
		return err
	}

	authorize := func(peer identity.Peer) error {
		if err := pairHandler.Authorize(peer); err != nil {
			return err
		}
		if v := r.peerVerifier(); v != nil && !v.VerifyPeer(newPeerInfo(peer)) {
			return ErrPeerRefused
		}
		return nil
	}

	// capID 0 is registered so a peer that revokes this device mid-transfer is
	// honoured while the transfer is still running (§6.1).
	handlers := map[byte]session.Handler{files.CapID: cap, 0: pairHandler}
	if c := r.clipboard(); c != nil {
		handlers[clipboard.CapID] = c.capability()
	}

	ln, err := conn.Listen(listenAddr, r.id.impl, r.displayName, PlatformName, handlers,
		conn.ListenOptions{Authorize: authorize, PeerLookup: r.id.store.Get})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())

	r.mu.Lock()
	r.ln, r.cancel, r.addr = ln, cancel, ln.Addr()
	r.mu.Unlock()

	go r.acceptLoop(ctx, ln)
	return nil
}

// acceptLoop admits sessions until the receiver is stopped.
//
// Accept blocks through the peer's Hello exchange and through the verifier
// prompt, so it runs here rather than inline: one peer that connects and then
// says nothing would otherwise hold up every other peer.
func (r *Receiver) acceptLoop(ctx context.Context, ln conn.Listener) {
	for {
		sess, err := ln.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // Stop was called; not a failure.
			}
			if cb := r.errorCallback(); cb != nil {
				cb.OnError("accept", err.Error())
			}
			return
		}
		if cb := r.sessionCallback(); cb != nil {
			cb.OnPeerConnected(newPeerInfo(sess.Peer()))
		}
	}
}

// Addr reports the bound address, including the concrete port when Start was
// given ":0". Empty when the receiver is not running.
func (r *Receiver) Addr() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.addr
}

// Port reports just the bound port, or -1 when not running. It exists because
// the shell shows a port next to an IP the platform supplies, and splitting
// host:port in Kotlin to get there is needless.
func (r *Receiver) Port() int {
	r.mu.Lock()
	addr := r.addr
	r.mu.Unlock()
	if addr == "" {
		return -1
	}
	return portOf(addr)
}

// IsRunning reports whether the receiver is listening.
func (r *Receiver) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ln != nil
}

// Stop closes the listener and unblocks the accept loop. Stopping a stopped
// receiver returns ErrNotRunning. Transfers already in flight are torn down
// with their connections.
func (r *Receiver) Stop() error {
	r.mu.Lock()
	ln, cancel := r.ln, r.cancel
	r.ln, r.cancel, r.addr = nil, nil, ""
	r.mu.Unlock()

	if ln == nil {
		return ErrNotRunning
	}
	cancel()
	return ln.Close()
}

func (r *Receiver) progressCallback() ProgressCallback {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.progress
}

func (r *Receiver) peerVerifier() PeerVerifier {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peerVerify
}

func (r *Receiver) offerVerifier() OfferVerifier {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.offerVerif
}

func (r *Receiver) transferCallback() TransferCallback {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.onDone
}

func (r *Receiver) errorCallback() ErrorCallback {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.onErr
}

func (r *Receiver) sessionCallback() SessionCallback {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.onSession
}

// portOf extracts the port from a host:port address. It does not use
// net.SplitHostPort's error, because the address came from a bound listener and
// a parse failure here is a bug, not a condition the shell can act on.
func portOf(addr string) int {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			var p int
			for _, c := range addr[i+1:] {
				if c < '0' || c > '9' {
					return -1
				}
				p = p*10 + int(c-'0')
			}
			return p
		}
	}
	return -1
}
