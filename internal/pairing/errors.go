package pairing

import (
	"errors"
	"fmt"

	"github.com/shreyashsri79/openair/internal/identity"
)

var (
	// ErrUnpaired reports an inbound peer that is not in the trust store, or is
	// in it at level unpaired. PROTOCOL.md §6 / PRD R2: only devices you paired
	// with may proceed.
	ErrUnpaired = errors.New("pairing: peer is not paired")

	// ErrPairingClosed reports an unpaired peer arriving while no pairing
	// window is open. Distinct from ErrUnpaired so a UI can say "press pair on
	// this device" rather than "unknown device".
	ErrPairingClosed = errors.New("pairing: no pairing window is open")

	// ErrDeclined reports that the local user did not confirm the short
	// authentication string.
	ErrDeclined = errors.New("pairing: declined on this device")

	// ErrPeerDeclined reports that the remote user did not confirm.
	ErrPeerDeclined = errors.New("pairing: declined on the peer device")

	// ErrNotPairing reports a pairing message arriving on a session with no
	// exchange in progress.
	ErrNotPairing = errors.New("pairing: no pairing exchange in progress on this session")

	// ErrNoConfirm reports a Config with no Confirm callback. PROTOCOL.md §5.2:
	// implementations MUST NOT offer a "skip verification" path, so there is no
	// default and a nil callback is a configuration error rather than an
	// implicit yes.
	ErrNoConfirm = errors.New("pairing: Config.Confirm is required; the SAS comparison cannot be skipped")

	// ErrRevoked reports an operation attempted above the level the peer
	// currently holds, after a revoke took effect mid-session (§6.1).
	ErrRevoked = errors.New("pairing: peer trust level no longer permits this operation")

	// ErrKeyBinding reports that a pairing message carried an identity key that
	// is not the one the peer used to terminate TLS. It wraps
	// identity.ErrKeyMismatch: pinning the claimed key instead of the presented
	// one would pin an attacker's chosen key.
	ErrKeyBinding = fmt.Errorf("%w: pairing message claims a different identity key than the TLS handshake presented",
		identity.ErrKeyMismatch)
)

// OfferMismatchError reports that the device answering is not the device whose
// offer was scanned (PROTOCOL.md §5.1).
//
// It wraps identity.ErrKeyMismatch and reports Retryable false for the same
// reason KeyMismatchError does: the answer will not change on a second dial,
// and retrying hides the prompt the user actually needs to see.
type OfferMismatchError struct {
	Offered   identity.DeviceID
	Presented identity.DeviceID
}

func (e *OfferMismatchError) Error() string {
	return fmt.Sprintf("pairing: offer is for device %s but %s answered, re-pair required",
		e.Offered, e.Presented)
}

func (e *OfferMismatchError) Unwrap() error { return identity.ErrKeyMismatch }

// Retryable reports false, always. See identity.KeyMismatchError.Retryable.
func (e *OfferMismatchError) Retryable() bool { return false }

// Retryable reports whether err is worth dialling again for.
//
// It answers false for every key mismatch in the stack -- a pinned key that
// changed, an offer answered by the wrong device, a pairing message claiming a
// key other than the one TLS presented. PROTOCOL.md §2 requires these to
// surface as a re-pair prompt and never as a retryable error, and generic retry
// logic needs one place to ask.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, identity.ErrKeyMismatch) {
		return false
	}
	var r interface{ Retryable() bool }
	if errors.As(err, &r) {
		return r.Retryable()
	}
	// Declining is a decision, not a fault; retrying it would re-prompt a user
	// who already said no.
	if errors.Is(err, ErrDeclined) || errors.Is(err, ErrPeerDeclined) {
		return false
	}
	return true
}
