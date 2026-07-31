package pairing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	v1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// Guard is the mid-session enforcement point PROTOCOL.md §6.1 requires: a
// revoke "takes effect mid-session", which means something a live session
// consults on every privileged operation rather than only at connect time.
//
// It holds two levels, because the wire carries two directions that D-30's
// trust store record conflates into one field:
//
//   - Level is what THIS device grants the peer. It is the persisted
//     Peer.Level, and it is what an inbound operation is checked against.
//   - Granted is what the PEER most recently said it grants us, via Revoke or
//     CapabilityGrant. It is session-scoped and deliberately not persisted:
//     writing it into Peer.Level would overwrite our own decision about them
//     with their decision about us.
//
// §6.3 already establishes that enforcement is local -- "SessionKill is a
// courtesy, not the enforcement" -- so the authoritative check is always the
// accessed device asking its own Guard.
type Guard struct {
	deviceID identity.DeviceID

	mu          sync.RWMutex
	level       identity.TrustLevel
	granted     identity.TrustLevel
	grantedCaps map[byte]struct{}
	reason      string
}

// Level is the trust level this device currently grants the peer.
func (g *Guard) Level() identity.TrustLevel {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.level
}

// Granted is the trust level the peer most recently said it grants this device.
func (g *Guard) Granted() identity.TrustLevel {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.granted
}

// Reason is the human-readable reason attached to the last revoke, if any.
func (g *Guard) Reason() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.reason
}

// Authorize reports whether the peer may perform an operation requiring the
// given level, right now. Capabilities call this before acting, which is what
// makes a revoke land on work already in progress rather than at the next
// connection.
func (g *Guard) Authorize(required identity.TrustLevel) error {
	g.mu.RLock()
	level, reason := g.level, g.reason
	g.mu.RUnlock()
	if level >= required && level > identity.LevelUnpaired {
		return nil
	}
	if reason != "" {
		return fmt.Errorf("%w: %s is at level %d, operation needs %d (%s)",
			ErrRevoked, g.deviceID, level, required, reason)
	}
	return fmt.Errorf("%w: %s is at level %d, operation needs %d",
		ErrRevoked, g.deviceID, level, required)
}

// GrantedCapability reports whether the peer has granted this device persistent
// use of a capability (§6.4). Grants received during the session are additive
// to whatever was recorded at pairing.
func (g *Guard) GrantedCapability(capID byte) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.grantedCaps[capID]
	return ok
}

func (g *Guard) set(level identity.TrustLevel, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.level = level
	g.reason = reason
}

func (g *Guard) setGranted(level identity.TrustLevel, caps []byte, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.granted = level
	if reason != "" {
		g.reason = reason
	}
	for _, c := range caps {
		g.grantedCaps[c] = struct{}{}
	}
}

// GuardFor returns the guard for a session, creating it from the trust store on
// first use. A peer with no stored record starts at LevelUnpaired, so a session
// that skipped Authorize still fails closed.
func (h *Handler) GuardFor(sess session.Session) *Guard {
	h.mu.Lock()
	defer h.mu.Unlock()
	if g, ok := h.guards[sess]; ok {
		return g
	}
	id := sess.Peer().DeviceID
	g := &Guard{deviceID: id, grantedCaps: map[byte]struct{}{}}
	if p, ok := h.cfg.Store.Get(id); ok {
		g.level = p.Level
		for _, c := range p.GrantedCapabilities {
			g.grantedCaps[c] = struct{}{}
		}
	}
	h.guards[sess] = g
	return g
}

// Revoke narrows what a peer may do and tells it so (PROTOCOL.md §6.1).
//
// Order matters and is deliberate: the trust store and the live guard are
// updated before the message goes out, so enforcement does not depend on the
// message arriving. A peer that ignores the notification finds every subsequent
// operation refused anyway.
//
// newLevel == identity.LevelUnpaired is a full unpair: both pinned keys are
// discarded and the session is closed. Demotion to Trusted keeps the pairing,
// which is the distinction D-20 created two keys to make meaningful.
func (h *Handler) Revoke(ctx context.Context, sess session.Session, newLevel identity.TrustLevel, reason string) error {
	id := sess.Peer().DeviceID
	g := h.GuardFor(sess)

	if newLevel == identity.LevelUnpaired {
		if err := h.cfg.Store.Delete(id); err != nil && !errors.Is(err, identity.ErrUnknownPeer) {
			return fmt.Errorf("pairing: unpairing %s: %w", id, err)
		}
	} else if p, ok := h.cfg.Store.Get(id); ok {
		if p.Level > newLevel {
			p.Level = newLevel
			if err := h.cfg.Store.Put(p); err != nil {
				return fmt.Errorf("pairing: demoting %s: %w", id, err)
			}
		}
	}
	g.set(newLevel, reason)

	sendErr := sess.Send(ctx, 0,
		session.MsgType(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_REVOKE),
		&v1.Revoke{NewLevel: session.TrustLevelToWire(newLevel), Reason: reason})

	if newLevel == identity.LevelUnpaired {
		// Closing the moment the Revoke is written loses it: QUIC's
		// CONNECTION_CLOSE overtakes stream data that has not been flushed, and
		// the peer then never learns it was unpaired -- it keeps our key pinned
		// and goes on believing the pairing holds. §6.1 requires the peer to
		// discard both keys on new_level = 0, so the notification has to get out
		// of the door before the door shuts.
		//
		// A short linger is the whole fix: the peer closes the session itself on
		// receipt (onRevoke), so this close is only the fallback for a peer that
		// ignores it. Blocking a deliberate user action for a fraction of a
		// second is not a cost worth engineering around.
		time.Sleep(notifyLinger)
		_ = sess.Close(uint16(session.CodeNotPaired), reason)
		h.Detach(sess)
	}
	if sendErr != nil {
		// The local half already took effect; report the failure so a UI can
		// say the peer was not told, but do not undo the revoke.
		return fmt.Errorf("pairing: revoke applied locally but not delivered to %s: %w", id, sendErr)
	}
	h.log.Info("revoked", "peer", id, "level", newLevel, "reason", reason)
	return nil
}

// onRevoke applies an inbound Revoke (§6.1): "on receipt the peer MUST
// immediately stop honouring operations above new_level and MUST abort
// in-flight ones that exceed it".
//
// The inbound level is what the peer grants us, so it lands in Guard.Granted
// rather than in the persisted record -- with one exception. `new_level = 0`
// additionally requires discarding both pinned keys, and a pairing only one
// side still holds is not a pairing: an unpair is mutual, so it does delete the
// stored record and end the session.
func (h *Handler) onRevoke(sess session.Session, m *v1.Revoke) error {
	id := sess.Peer().DeviceID
	level := session.TrustLevelFromWire(m.NewLevel)
	g := h.GuardFor(sess)
	g.setGranted(level, nil, m.Reason)

	if level == identity.LevelUnpaired {
		if err := h.cfg.Store.Delete(id); err != nil && !errors.Is(err, identity.ErrUnknownPeer) {
			return fmt.Errorf("pairing: discarding pinned keys for %s: %w", id, err)
		}
		g.set(identity.LevelUnpaired, m.Reason)
		h.log.Info("unpaired by peer", "peer", id, "reason", m.Reason)
		_ = sess.Close(uint16(session.CodeNotPaired), "unpaired by peer")
		h.Detach(sess)
		return nil
	}
	h.log.Info("peer lowered our level", "peer", id, "level", level, "reason", m.Reason)
	return nil
}

// onGrant applies an inbound CapabilityGrant (§6.4). Grants take effect
// immediately and are never requested: a peer widening our access is acting on
// its own initiative, so there is nothing to answer.
func (h *Handler) onGrant(sess session.Session, m *v1.CapabilityGrant) error {
	caps := make([]byte, 0, len(m.CapIds))
	for _, id := range m.CapIds {
		c, ok := session.CapIDToWire(id)
		if !ok {
			continue
		}
		caps = append(caps, c)
	}
	// The schema types `level` as the shared TrustLevel enum. §6.4's prose still
	// describes its own scale ("1 = Trusted, 2 = Owned"), which is the exact
	// disagreement §6.1 says TrustLevel was introduced to prevent; the schema is
	// right and the prose is stale. See the report.
	level := session.TrustLevelFromWire(m.Level)
	h.GuardFor(sess).setGranted(level, caps, "")
	h.log.Info("peer granted capabilities", "peer", sess.Peer().DeviceID, "caps", caps, "level", level)
	return nil
}
