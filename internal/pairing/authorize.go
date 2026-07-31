package pairing

import (
	"crypto/subtle"
	"fmt"
	"sync"

	"github.com/shreyashsri79/openair/internal/identity"
)

// Authorize is the trust-store gate that replaces M1's nil
// session.Config.Authorize. Register it as session.Config.Authorize on both the
// listening and the dialling path:
//
//	cfg.Authorize = handler.Authorize
//
// It is called once Hello has completed and the peer record carries a DeviceID
// and identity key derived from the TLS certificate, but before any capability
// message is dispatched. Returning an error closes the session.
//
// Three outcomes, in the order they are decided:
//
//  1. The peer is in the trust store above LevelUnpaired and the key it
//     presented is the pinned one -- admitted.
//  2. The peer is in the store but presented a different key -- refused with
//     identity.KeyMismatchError, which reports Retryable false. PROTOCOL.md §2
//     requires this to surface as a re-pair prompt and never as a retryable
//     failure, so nothing above may dial again on it.
//  3. The peer is unknown, or stored at LevelUnpaired -- refused, unless a
//     pairing window is open (§5.2 needs an unauthenticated session to run the
//     exchange over). The two refusals are distinct errors so a UI can say
//     "press pair on this device" rather than "unknown device".
func (h *Handler) Authorize(peer identity.Peer) error {
	stored, ok := h.cfg.Store.Get(peer.DeviceID)
	if ok && stored.Level > identity.LevelUnpaired {
		if err := checkPinned(stored, peer); err != nil {
			h.log.Warn("refusing peer: pinned key changed", "peer", peer.DeviceID)
			return err
		}
		h.touch(stored)
		return nil
	}

	if h.windowOpen() {
		h.log.Info("admitting unpaired peer for pairing", "peer", peer.DeviceID)
		return nil
	}
	if ok {
		return fmt.Errorf("%w: %s is stored at level unpaired", ErrUnpaired, peer.DeviceID)
	}
	return fmt.Errorf("%w: %s is not in the trust store", ErrPairingClosed, peer.DeviceID)
}

// checkPinned compares the key the peer actually presented against the pinned
// one in constant time.
//
// The comparison is redundant with the TLS pinning in identity.TLSConfig on the
// dialling path, which knows who it is calling and pins before the handshake. It
// is not redundant on the listening path: a listener cannot pin in advance
// because it does not know who is connecting until Hello arrives, so this is the
// only place the stored key and the presented key are ever compared there.
func checkPinned(stored, presented identity.Peer) error {
	if len(presented.IdentityPublicKey) == 0 {
		return identity.ErrNoPeerCertificate
	}
	if subtle.ConstantTimeCompare(stored.IdentityPublicKey, presented.IdentityPublicKey) != 1 {
		return &identity.KeyMismatchError{
			Pinned: stored.IdentityPublicKey,
			Got:    presented.IdentityPublicKey,
		}
	}
	return nil
}

// touch records that the peer connected. Best effort: a trust store that cannot
// write its last-seen timestamp is a disk problem worth logging, not a reason to
// refuse a peer whose key checked out.
func (h *Handler) touch(stored identity.Peer) {
	stored.LastSeen = h.now().UnixMilli()
	if err := h.cfg.Store.Put(stored); err != nil {
		h.log.Warn("could not record last-seen", "peer", stored.DeviceID, "error", err)
	}
}

// OpenWindow opens a pairing window and returns the func that closes it.
//
// A window is what admits an unpaired peer at all. It exists because §5.2 has to
// run over a session that is not yet authenticated by anything, and the safe
// default is that such a session cannot be established: pairing is a deliberate
// act on both devices (PRD R2), so the local user pressing "pair" is what opens
// the door, and it shuts again the moment they stop waiting.
//
// Windows nest. Two UI surfaces may each be waiting to pair, and the door stays
// open until both have given up. The returned func is idempotent, so a deferred
// close and an explicit one do not double-count.
func (h *Handler) OpenWindow() (close func()) {
	h.mu.Lock()
	h.window++
	h.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			if h.window > 0 {
				h.window--
			}
			h.mu.Unlock()
		})
	}
}

// WindowOpen reports whether any pairing window is currently open. Exported for
// a UI that wants to show it; the gate itself reads the same state under lock.
func (h *Handler) WindowOpen() bool { return h.windowOpen() }

func (h *Handler) windowOpen() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.window > 0
}
