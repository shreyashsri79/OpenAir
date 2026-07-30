package conn

import (
	"context"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

// Dialer establishes sessions with peers. Phase 1 is a plain LAN dial to an
// explicit address; Phase 2 replaces the implementation with candidate racing
// and live migration (PROTOCOL.md §18) without changing this interface --
// which is the point of it existing now.
type Dialer interface {
	// DialAddr connects to an explicit host:port. M1 uses this directly;
	// later milestones use Dial.
	DialAddr(ctx context.Context, addr string, pinned identity.Peer) (session.Session, error)

	// Dial resolves a peer by DeviceID via discovery or rendezvous, races the
	// available paths, and returns once any succeeds. Phase 2.
	Dial(ctx context.Context, id identity.DeviceID) (session.Session, error)
}

// Listener accepts inbound sessions.
type Listener interface {
	Accept(ctx context.Context) (session.Session, error)
	Addr() string
	Close() error
}
