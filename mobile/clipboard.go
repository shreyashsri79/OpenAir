package mobile

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/clipboard"
	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/pairing"
	"github.com/shreyashsri79/openair/internal/session"
)

// ClipboardCallback receives clipboard content pushed by a paired device
// (PROTOCOL.md §9).
//
// It is called on a Go goroutine, like every other callback here, so a shell
// that touches UI state from it has to post to its own thread first. Applying
// the content is the shell's job: Android's ClipboardManager is not reachable
// from Go, and on API 29+ writing the clipboard is only permitted from the
// foreground, which is a policy this layer cannot see.
type ClipboardCallback interface {
	OnClipboard(peer *PeerInfo, text string)
}

// clipboardTimeout bounds one push, including the dial. A phone that has walked
// out of range must fail rather than hang a button press.
const clipboardTimeout = 20 * time.Second

// Clipboard pushes text to a paired device and is the receiving end's sink.
//
// It runs on the identity key, not the privilege key (D-20), so it keeps
// working while an unlock session is expired. That is the whole reason the
// clipboard is not gated: a phone left idle for six hours that silently stopped
// sharing its clipboard would have the auth policy blamed on the feature.
type Clipboard struct {
	id          *Identity
	displayName string

	mu     sync.Mutex
	onRecv ClipboardCallback
}

// NewClipboard returns a Clipboard bound to this device's identity.
func NewClipboard(id *Identity, displayName string) *Clipboard {
	return &Clipboard{id: id, displayName: displayName}
}

// SetClipboardCallback installs the sink for inbound content. Install it before
// starting a Receiver, since a paired peer can push the moment one is
// listening.
func (c *Clipboard) SetClipboardCallback(cb ClipboardCallback) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onRecv = cb
}

func (c *Clipboard) callback() ClipboardCallback {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onRecv
}

// MaxBytes is the largest content this build will send or accept, so a shell
// can refuse locally rather than after a round trip.
func (c *Clipboard) MaxBytes() int { return clipboard.DefaultMaxBytes }

// Push sends text to the device at addr ("host:port").
//
// It dials, checks the trust store against the key the peer actually presented,
// pushes, and closes. A device that was never paired is refused at both ends.
func (c *Clipboard) Push(addr string, text string) error {
	if text == "" {
		return fmt.Errorf("mobile: nothing to push")
	}

	ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
	defer cancel()

	handler, err := c.pairingHandler()
	if err != nil {
		return err
	}
	cap := clipboard.New(clipboard.Config{Tag: string(c.id.impl.DeviceID())})

	dialer := conn.NewDialer(c.id.impl, c.displayName, PlatformName,
		map[byte]session.Handler{clipboard.CapID: cap, 0: handler})

	// No pinned key before the handshake: an address is not an identity. The
	// key is learned from the connection and checked immediately afterwards.
	sess, err := dialer.DialAddr(ctx, addr, identity.Peer{})
	if err != nil {
		return err
	}
	defer sess.Close(0, "clipboard push done")

	if err := handler.Authorize(sess.Peer()); err != nil {
		return fmt.Errorf("mobile: %s is not paired with this device: %w",
			sess.Peer().DeviceID.Fingerprint(), err)
	}

	if err := cap.PushText(ctx, sess, text); err != nil {
		return err
	}

	// §9 defines no acknowledgement, so the close would overtake the message on
	// the wire without this (D-46).
	time.Sleep(250 * time.Millisecond)
	return nil
}

// capability builds the receiving-side handler, for Receiver to register.
func (c *Clipboard) capability() *clipboard.Capability {
	return clipboard.New(clipboard.Config{
		Tag: string(c.id.impl.DeviceID()),
		OnReceive: func(_ context.Context, peer identity.Peer, content clipboard.Content) error {
			if cb := c.callback(); cb != nil {
				cb.OnClipboard(newPeerInfo(peer), content.Text())
			}
			return nil
		},
	})
}

// pairingHandler builds the capID 0 handler that gates peers by the trust
// store. Pushing never pairs, so its Confirm refuses: pairing has its own
// screen and its own listener.
func (c *Clipboard) pairingHandler() (*pairing.Handler, error) {
	return pairing.NewHandler(pairing.Config{
		Local:       c.id.impl,
		Store:       c.id.store,
		DisplayName: c.displayName,
		Platform:    PlatformName,
		Confirm: func(context.Context, string, pairing.PeerInfo) (bool, error) {
			return false, nil
		},
	})
}
