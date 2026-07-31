package mobile

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/pairing"
	"github.com/shreyashsri79/openair/internal/session"
)

// SASVerifier shows the six-digit short authentication string and reports
// whether the user confirmed that the other device displays the same digits.
//
// This is the entire security of pairing (PROTOCOL.md §5.2). TLS authenticates
// nothing at this point, so a man in the middle is detected by these two numbers
// differing and by nothing else. An implementation that returns true without a
// human having actually compared them defeats pairing completely, and there is
// deliberately no way to skip the comparison.
//
// It is called from a Go goroutine and must block until the user answers.
type SASVerifier interface {
	// ConfirmSAS receives the digits already grouped for display ("123 456")
	// and the peer as it described itself. Nothing about the peer is
	// authenticated yet.
	ConfirmSAS(sas string, peer *PeerInfo) bool
}

// Pairing runs PROTOCOL.md §5 from either side.
//
// One device shows an offer and waits (ShowOffer then AwaitPeer); the other
// scans or types that offer and dials (PairWithOffer). Both users are then shown
// the same six digits, and the pairing completes only if both confirm.
type Pairing struct {
	id          *Identity
	displayName string

	mu       sync.Mutex
	verifier SASVerifier
	handler  *pairing.Handler
	ln       conn.Listener
	closeWin func()
	offer    string
}

// NewPairing builds the pairing driver. Set an SASVerifier before starting
// anything: without one, every attempt fails rather than silently succeeding.
func NewPairing(id *Identity, displayName string) *Pairing {
	return &Pairing{id: id, displayName: displayName}
}

// SetSASVerifier installs the digit-comparison callback. Required.
func (p *Pairing) SetSASVerifier(v SASVerifier) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.verifier = v
}

// newHandler builds a pairing handler whose Confirm goes to the SASVerifier.
func (p *Pairing) newHandler() (*pairing.Handler, error) {
	p.mu.Lock()
	v := p.verifier
	p.mu.Unlock()
	if v == nil {
		return nil, ErrNoSASVerifier
	}
	return pairing.NewHandler(pairing.Config{
		Local:       p.id.impl,
		Store:       p.id.store,
		DisplayName: p.displayName,
		Platform:    PlatformName,
		Confirm: func(_ context.Context, sas string, peer pairing.PeerInfo) (bool, error) {
			return v.ConfirmSAS(pairing.FormatSAS(sas), &PeerInfo{
				deviceID:    string(peer.DeviceID),
				displayName: peer.DisplayName,
				platform:    peer.Platform,
			}), nil
		},
	})
}

// ShowOffer starts listening and returns the string to render as a QR code or
// display for typing. Pass "" for any free port.
//
// Any free port is the default rather than DefaultListenAddr because a device
// pairing is very often also receiving, and the two would collide on 9000. The
// port does not need to be predictable: it travels inside the offer.
//
// The offer carries this device's DeviceID, a fingerprint of its identity key
// and the address to dial. The scanning device checks the key presented in TLS
// against that fingerprint before anything else happens (§5.1), which is what
// authenticates this device to that one.
//
// Call AwaitPeer next; call Stop when the user cancels.
func (p *Pairing) ShowOffer(listenAddr string) (string, error) {
	if listenAddr == "" {
		listenAddr = ":0"
	}

	p.mu.Lock()
	if p.ln != nil {
		p.mu.Unlock()
		return "", ErrAlreadyRunning
	}
	p.mu.Unlock()

	h, err := p.newHandler()
	if err != nil {
		return "", err
	}

	ln, err := conn.Listen(listenAddr, p.id.impl, p.displayName, PlatformName,
		map[byte]session.Handler{0: h}, h.Authorize)
	if err != nil {
		return "", err
	}

	// The window is what admits an unpaired peer at all, and it is open only
	// while this screen is up: pairing is a deliberate act on both devices.
	closeWin := h.OpenWindow()

	offer, err := pairing.NewOffer(p.id.impl, []string{ln.Addr()})
	if err != nil {
		closeWin()
		ln.Close()
		return "", err
	}
	encoded, err := pairing.EncodeOffer(offer)
	if err != nil {
		closeWin()
		ln.Close()
		return "", err
	}

	p.mu.Lock()
	p.handler, p.ln, p.closeWin, p.offer = h, ln, closeWin, encoded
	p.mu.Unlock()
	return encoded, nil
}

// OfferGrouped returns the current offer hyphenated for manual entry, for the
// case where the other device has no camera. Empty before ShowOffer.
func (p *Pairing) OfferGrouped() string {
	p.mu.Lock()
	encoded := p.offer
	p.mu.Unlock()
	if encoded == "" {
		return ""
	}
	o, err := pairing.DecodeOffer(encoded)
	if err != nil {
		return ""
	}
	grouped, err := pairing.EncodeOfferGrouped(o)
	if err != nil {
		return ""
	}
	return grouped
}

// ListenAddr reports the address ShowOffer bound, or "" if it is not running.
func (p *Pairing) ListenAddr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln == nil {
		return ""
	}
	return p.ln.Addr()
}

// AwaitPeer blocks until a device pairs with this one, or the wait fails.
//
// It must be called off the Android main thread. Stop makes it return.
func (p *Pairing) AwaitPeer() (*PeerInfo, error) {
	p.mu.Lock()
	ln, h := p.ln, p.handler
	p.mu.Unlock()
	if ln == nil {
		return nil, ErrNotRunning
	}

	ctx := context.Background()
	sess, err := ln.Accept(ctx)
	if err != nil {
		return nil, err
	}
	defer sess.Close(0, "pairing done")

	peer, err := h.Await(ctx, sess)
	if err != nil {
		return nil, err
	}
	return newPairedPeerInfo(peer), nil
}

// PairWithOffer is the scanning side: it decodes the offer, dials the address
// in it, and runs the exchange. It blocks and must be called off the main
// thread.
//
// The offer may be either form the other device displayed -- the
// "openair://pair/..." string behind a QR code, or the hyphenated one a user
// retyped. Case, spacing and hyphens are all tolerated.
func (p *Pairing) PairWithOffer(offer string) (*PeerInfo, error) {
	h, err := p.newHandler()
	if err != nil {
		return nil, err
	}

	decoded, err := pairing.DecodeOffer(offer)
	if err != nil {
		return nil, err
	}
	if len(decoded.LanHints) == 0 {
		return nil, fmt.Errorf("mobile: the offer carries no address to dial")
	}

	ctx := context.Background()

	// No pinned key: during pairing TLS authenticates nothing, and the offer's
	// fingerprint is what Initiate checks the presented key against.
	var lastErr error
	for _, addr := range decoded.LanHints {
		sess, err := conn.NewDialer(p.id.impl, p.displayName, PlatformName,
			map[byte]session.Handler{0: h}).
			DialAddr(ctx, addr, identity.Peer{})
		if err != nil {
			lastErr = err
			continue
		}
		peer, err := h.Initiate(ctx, sess, decoded)
		sess.Close(0, "pairing done")
		if err != nil {
			return nil, err
		}
		return newPairedPeerInfo(peer), nil
	}
	return nil, fmt.Errorf("mobile: could not reach the device that showed this code: %w", lastErr)
}

// Stop closes the pairing listener and shuts the pairing window. Safe to call
// when nothing is running.
func (p *Pairing) Stop() error {
	p.mu.Lock()
	ln, closeWin := p.ln, p.closeWin
	p.ln, p.closeWin, p.handler, p.offer = nil, nil, nil, ""
	p.mu.Unlock()

	if closeWin != nil {
		closeWin()
	}
	if ln != nil {
		return ln.Close()
	}
	return nil
}

// IsShowingOffer reports whether this device is currently pairable.
func (p *Pairing) IsShowingOffer() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ln != nil
}

// ErrNoSASVerifier reports a pairing attempt with no digit-comparison callback
// installed. PROTOCOL.md §5.2 forbids a skip-verification path, so a missing
// callback is a programming error rather than an implicit yes.
var ErrNoSASVerifier = errors.New("mobile: SetSASVerifier is required; the digit comparison cannot be skipped")

// newPairedPeerInfo builds a PeerInfo for a peer that has just been pinned.
func newPairedPeerInfo(p identity.Peer) *PeerInfo {
	return &PeerInfo{
		deviceID:    string(p.DeviceID),
		displayName: p.DisplayName,
		platform:    p.Platform,
		trusted:     p.Level > identity.LevelUnpaired,
	}
}
