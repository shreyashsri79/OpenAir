package caps

import (
	"context"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

// Capability is one feature riding a session. Registration is by wire capID
// (PROTOCOL.md Appendix B).
//
// Serve is called for each inbound message already routed to this capability
// and already authorised: the session layer checks trust level and verifies any
// AuthProof before a message reaches here (PROTOCOL.md §6), so implementations
// must not repeat that check and must not skip it either.
type Capability interface {
	CapID() byte

	// RequiredLevel is the minimum trust level for this capability's
	// operations. Capabilities needing per-message granularity check inside
	// Serve instead.
	RequiredLevel() identity.TrustLevel

	Serve(ctx context.Context, sess session.Session, msgType uint16, payload []byte) error

	// ServeStream handles a capability stream after its opening envelope.
	// Bulk capabilities read raw frames from here (PROTOCOL.md §8.3).
	ServeStream(ctx context.Context, sess session.Session, st session.Stream, msgType uint16, payload []byte) error
}
