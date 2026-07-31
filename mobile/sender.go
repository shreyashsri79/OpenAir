package mobile

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"sync"

	"github.com/shreyashsri79/openair/internal/caps/files"
	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

// ErrPeerRefused reports that PeerVerifier said no, or that no verifier was
// set. Nothing was sent in either case.
var ErrPeerRefused = errors.New("mobile: the peer's fingerprint was not confirmed; nothing was sent")

// ErrNoFiles reports a send with an empty FileList.
var ErrNoFiles = errors.New("mobile: no files to send")

// ErrNotPaired reports a transfer to a device this one has never paired with.
// M2 makes pairing the precondition for any transfer, in both directions, and
// there is no callback that can override it.
var ErrNotPaired = errors.New("mobile: not paired with this device; pair with it first")

// ErrBusy reports a second SendFiles while one is already running. One
// transfer at a time is M1's scope, and silently queueing would make the
// progress callback ambiguous about which transfer it describes.
var ErrBusy = errors.New("mobile: a transfer is already in progress")

// Sender sends files to an explicit address.
//
// One Sender can be reused for many transfers, but only one at a time. Set the
// callbacks before calling SendFiles; they may be changed between transfers.
type Sender struct {
	id          *Identity
	displayName string

	mu       sync.Mutex
	progress ProgressCallback
	verifier PeerVerifier
	cancel   context.CancelFunc
	busy     bool
}

// NewSender returns a Sender using this device's identity. displayName is what
// the peer sees beside the fingerprint; it is not authenticated, so it is a
// convenience, not a credential.
func NewSender(id *Identity, displayName string) *Sender {
	return &Sender{id: id, displayName: displayName}
}

// SetProgressCallback installs the progress sink. Pass nil to remove it.
func (s *Sender) SetProgressCallback(cb ProgressCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress = cb
}

// SetPeerVerifier installs an optional confirmation shown before anything
// leaves the device.
//
// It is no longer the security boundary -- the trust store is (M2) -- so a
// shell that sets nothing sends to its paired devices without asking. Set one
// when the product wants a per-transfer prompt anyway.
func (s *Sender) SetPeerVerifier(v PeerVerifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verifier = v
}

// SendFiles dials addr ("host:port"), checks the peer is paired, and
// transfers every file in list. It blocks until the transfer finishes and
// returns the transfer id.
//
// The id is returned even on failure whenever one was allocated, so a caller
// can correlate a failure with the progress it already displayed.
func (s *Sender) SendFiles(addr string, list *FileList) (string, error) {
	items := list.snapshot()
	if len(items) == 0 {
		return "", ErrNoFiles
	}
	total := list.TotalBytes()

	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return "", ErrBusy
	}
	progress, verifier := s.progress, s.verifier
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.busy = cancel, true
	s.mu.Unlock()

	defer func() {
		cancel()
		s.mu.Lock()
		s.cancel, s.busy = nil, false
		s.mu.Unlock()
	}()

	cap := files.New(files.Config{
		OnProgress: func(p files.Progress) {
			if progress == nil {
				return
			}
			// The core derives the sender-side total from the registered plan
			// and reports 0 when a progress message arrives before or after
			// that plan exists. We already know the real total from the list,
			// so report it rather than handing the UI a zero to divide by.
			t := int64(p.TotalBytes)
			if t == 0 {
				t = total
			}
			progress.OnProgress(p.TransferID, int64(p.BytesReceived), t)
		},
	})

	d := conn.NewDialer(s.id.impl, s.displayName, PlatformName,
		map[byte]session.Handler{files.CapID: cap})

	// An address is not an identity, so nothing can be pinned before the
	// handshake: the key is learned from it and checked against the trust store
	// immediately afterwards. identity.Peer's zero value is what the session
	// layer treats as unpinned.
	sess, err := d.DialAddr(ctx, addr, identity.Peer{})
	if err != nil {
		return "", err
	}
	defer sess.Close(0, "done")

	peer := sess.Peer()

	// M2's rule, on the sending side: files go to devices this one paired with,
	// and to no others. The receiving end enforces the same thing for itself.
	stored, ok := s.id.store.Get(peer.DeviceID)
	if !ok || stored.Level == identity.LevelUnpaired {
		return "", fmt.Errorf("%w: %s", ErrNotPaired, FormatFingerprint(string(peer.DeviceID)))
	}
	if subtle.ConstantTimeCompare(stored.IdentityPublicKey, peer.IdentityPublicKey) != 1 {
		return "", &identity.KeyMismatchError{
			Pinned: stored.IdentityPublicKey,
			Got:    peer.IdentityPublicKey,
		}
	}

	// An optional last look before anything leaves the device. Unlike M1's
	// version this is not the security boundary -- the pairing above is -- so a
	// shell that does not set one sends to its paired devices without asking.
	if verifier != nil && !verifier.VerifyPeer(newPeerInfo(peer)) {
		return "", ErrPeerRefused
	}

	return cap.Send(ctx, sess, items)
}

// Cancel aborts the transfer in progress, if any. SendFiles then returns an
// error. Calling it when nothing is running does nothing.
func (s *Sender) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// IsSending reports whether a transfer is in progress.
func (s *Sender) IsSending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}
