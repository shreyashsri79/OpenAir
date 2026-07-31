package session

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	v1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// Owned-level authorisation, PROTOCOL.md §6.
//
// The wire arrangement is fixed by the schema rather than chosen here: AuthProof
// is a standalone control message with its own msgType, and it carries no capID
// or msgType field of its own. It therefore cannot be verified when it arrives,
// because the signed input includes the operation it authorises. So a proof is
// held, and the next message that verifies against it consumes it. That is the
// only reading of §6 the schema permits, and it keeps the binding cryptographic:
// a proof for a file read simply does not verify as a screen mirror (D-57).
//
// Everything here runs on the control loop, synchronously, before a message is
// queued for its capability. Authorisation that happened after queueing would be
// authorisation in a race with the thing it authorises.

// MsgAuthProof is the control msgType carrying an AuthProof (§6).
const MsgAuthProof = uint16(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_AUTH_PROOF)

// proofWindow is §6's ±60 seconds on issued_at. It is also how long a nonce is
// remembered: outside the window a replay is refused by the clock check, so the
// cache only has to cover the window itself.
const proofWindow = 60 * time.Second

// maxPendingProofs bounds proofs received but not yet matched to a request.
//
// §6 pairs one proof with one operation, so a peer holding several open at once
// is either pipelining hard or probing. The bound is what stops the latter from
// growing a map on this side; the oldest is dropped, which costs a legitimate
// pipeliner one refused operation and costs an attacker the attack.
const maxPendingProofs = 8

// AuthEvent is one authorisation decision on an inbound message.
//
// PRD R4 requires the accessed device to keep a local session log, and §6.3 adds
// that both ends must log authentication events because neither log is
// sufficient alone. This is the accessed device's half; the initiator logs its
// own unlock and expiry, which happen in identity and never reach the wire.
type AuthEvent struct {
	Peer    identity.DeviceID
	CapID   byte
	MsgType uint16

	// Owned reports that the message carried a verified privilege-key proof.
	Owned bool

	// Allowed is the decision. When false, Code is the §10 code that describes
	// it -- UNAUTHORISED or AUTH_EXPIRED -- and Reason is for the log.
	Allowed bool
	Code    ErrorCode
	Reason  string
}

// ownedCtxKey marks a context whose message arrived with a verified proof.
type ownedCtxKey struct{}

// OwnedFromContext reports whether the message being served carried a valid
// Owned-level AuthProof (§6).
//
// It is how a capability distinguishes "a Trusted peer is asking" from "an
// unlocked, Owned peer is asking" for the same message -- an inbound file offer
// that needs a human, versus one that may be accepted unattended (PRD R3). A
// capability whose every operation is Owned should declare RequiredLevel
// instead; this is for the per-message case.
func OwnedFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(ownedCtxKey{}).(bool)
	return v
}

func withOwned(ctx context.Context, owned bool) context.Context {
	if !owned {
		return ctx
	}
	return context.WithValue(ctx, ownedCtxKey{}, true)
}

// authVerifier holds one session's inbound proofs and the nonces it has seen.
type authVerifier struct {
	// local is this device's own DeviceID, which every proof must name as its
	// target. It is what stops a proof captured by one peer being replayed
	// against another (§6, rule 2).
	local identity.DeviceID
	now   func() time.Time

	mu      sync.Mutex
	pending []*v1.AuthProof
	seen    map[string]time.Time
}

func newAuthVerifier(local identity.DeviceID, now func() time.Time) *authVerifier {
	if now == nil {
		now = time.Now
	}
	return &authVerifier{local: local, now: now, seen: map[string]time.Time{}}
}

// receive takes an AuthProof off the control stream. It cannot be verified yet:
// the signed input names the operation, and the operation has not arrived.
func (a *authVerifier) receive(payload []byte) error {
	var p v1.AuthProof
	if err := proto.Unmarshal(payload, &p); err != nil {
		return protoErr(CodeProtocolViolation, err, "malformed AuthProof")
	}
	if len(p.Nonce) != identity.OwnedNonceLen || len(p.Signature) != ed25519.SignatureSize {
		return protoErr(CodeUnauthorised, nil,
			"AuthProof has a %d-byte nonce and a %d-byte signature", len(p.Nonce), len(p.Signature))
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.pending) >= maxPendingProofs {
		a.pending = a.pending[1:]
	}
	a.pending = append(a.pending, &p)
	return nil
}

// consume looks for a pending proof that authorises this exact operation and,
// finding one, spends it. A proof is good for one operation: §6 makes it fresh
// per request, and leaving it usable twice would make the nonce pointless.
//
// It returns owned=false with a nil code when no proof was offered at all --
// the common case, since most messages are not Owned-level.
func (a *authVerifier) consume(peerPrivilegeKey ed25519.PublicKey, capID byte, msgType uint16) (owned bool, code ErrorCode, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.now()
	a.pruneLocked(now)
	if len(a.pending) == 0 {
		return false, 0, ""
	}
	if len(peerPrivilegeKey) != ed25519.PublicKeySize {
		// D-20: the privilege key is pinned at pairing alongside the identity
		// key. No pinned key means either a tier 3 peer (D-21), which holds no
		// privilege key at all, or a record written before it had one.
		return false, CodeUnauthorised, "peer has no pinned privilege key"
	}

	for idx, p := range a.pending {
		input, err := identity.OwnedSigningInput(a.local, capID, msgType, p.Nonce, p.IssuedAt)
		if err != nil {
			continue
		}
		if !ed25519.Verify(peerPrivilegeKey, input, p.Signature) {
			// Not this operation's proof -- or not a valid one at all. The two
			// are indistinguishable here, deliberately: a peer learns nothing
			// from us about which of its guesses was closer.
			continue
		}

		a.pending = append(a.pending[:idx], a.pending[idx+1:]...)

		issued := time.UnixMilli(p.IssuedAt)
		if d := now.Sub(issued); d > proofWindow || d < -proofWindow {
			// §6 rule 3. AUTH_EXPIRED rather than UNAUTHORISED, because the
			// initiator's correct response is to re-authenticate rather than to
			// conclude it is not allowed (§10).
			return false, CodeAuthExpired, "AuthProof issued_at is outside the 60-second window"
		}
		key := base64.StdEncoding.EncodeToString(p.Nonce)
		if _, replayed := a.seen[key]; replayed {
			// §6 rule 4.
			return false, CodeUnauthorised, "AuthProof nonce was already used"
		}
		a.seen[key] = issued
		return true, 0, ""
	}
	return false, 0, ""
}

// pruneLocked forgets nonces that have aged out of §6's window. A nonce older
// than the window cannot be replayed successfully anyway -- the clock check
// refuses it first -- so the cache is bounded by the window rather than by the
// length of the session.
//
// Pending proofs are deliberately *not* pruned by age. Dropping a stale one
// here would leave the operation it accompanies looking simply unauthorised,
// and the peer would be told UNAUTHORISED when the truth is AUTH_EXPIRED: one
// says "you may not do this", the other says "authenticate again", and they
// send a user to different places (§10). Staleness is therefore diagnosed at
// match time, and the count bound is what keeps the slice small.
func (a *authVerifier) pruneLocked(now time.Time) {
	for nonce, issued := range a.seen {
		if now.Sub(issued) > proofWindow {
			delete(a.seen, nonce)
		}
	}
}

// Leveled is the optional interface a Handler implements to declare the trust
// level its operations need (§6). caps.Capability already declares
// RequiredLevel, so every real capability satisfies it structurally; a handler
// that does not is treated as Trusted, which is the level a device has the
// moment it is paired.
type Leveled interface {
	RequiredLevel() identity.TrustLevel
}

func requiredLevel(h Handler) identity.TrustLevel {
	if l, ok := h.(Leveled); ok {
		return l.RequiredLevel()
	}
	return identity.LevelTrusted
}

// authorizeInbound is §6's gate, applied to one message before it reaches its
// capability. It returns whether the message may be served, and whether it
// arrived with a verified Owned proof.
//
// Two independent checks, and it is worth being precise about which is which:
//
//   - The trust ladder. A capability declaring Owned is unreachable by a peer
//     the trust store does not record as Owned, proof or no proof. This is only
//     enforced when we actually hold a record for the peer: a caller that
//     supplies no PeerLookup has already decided who may connect, in Authorize,
//     and the session must not overrule that decision with a level it never
//     learned.
//   - The proof. A capability declaring Owned additionally requires a valid
//     AuthProof on every message, because the trust store says what a peer is
//     allowed to do and the proof says the human was present for it (D-18).
func (s *sess) authorizeInbound(h Handler, capID byte, msgType uint16) (owned bool, allowed bool) {
	s.mu.RLock()
	peer := s.peer
	haveRecord := s.haveRecord
	s.mu.RUnlock()

	// The trust level is re-read per message rather than taken from the record
	// Hello populated. A level is state on *this* device, and the local user
	// changes it while sessions are open: §6.4's grant, and §6.1's revocation,
	// which is explicitly required to land on a session already running. A
	// cached level makes a promotion need a reconnect to take effect and -- the
	// half that matters -- leaves a demoted peer at its old level until one
	// happens (D-74).
	if s.cfg.PeerLookup != nil && peer.DeviceID != "" {
		if current, ok := s.cfg.PeerLookup(peer.DeviceID); ok {
			peer.Level = current.Level
			peer.PrivilegePublicKey = current.PrivilegePublicKey
			peer.GrantedCapabilities = current.GrantedCapabilities
			haveRecord = true
		}
	}

	owned, code, reason := s.auth.consume(peer.PrivilegePublicKey, capID, msgType)
	if code != 0 {
		s.authEvent(AuthEvent{
			Peer: peer.DeviceID, CapID: capID, MsgType: msgType,
			Allowed: false, Code: code, Reason: reason,
		})
		return false, false
	}

	required := requiredLevel(h)
	if required <= identity.LevelTrusted {
		if owned {
			s.authEvent(AuthEvent{
				Peer: peer.DeviceID, CapID: capID, MsgType: msgType,
				Owned: true, Allowed: true,
			})
		}
		return owned, true
	}

	if haveRecord && peer.Level < required {
		s.authEvent(AuthEvent{
			Peer: peer.DeviceID, CapID: capID, MsgType: msgType,
			Allowed: false, Code: CodeUnauthorised,
			Reason: "peer is not recorded as Owned",
		})
		return false, false
	}
	if !owned {
		s.authEvent(AuthEvent{
			Peer: peer.DeviceID, CapID: capID, MsgType: msgType,
			Allowed: false, Code: CodeUnauthorised,
			Reason: "Owned-level message carried no AuthProof",
		})
		return false, false
	}
	s.authEvent(AuthEvent{
		Peer: peer.DeviceID, CapID: capID, MsgType: msgType,
		Owned: true, Allowed: true,
	})
	return true, true
}

func (s *sess) authEvent(e AuthEvent) {
	if !e.Allowed {
		s.log.Warn("refusing message",
			"peer", e.Peer, "capID", e.CapID, "msgType", e.MsgType,
			"code", e.Code.String(), "reason", e.Reason)
	}
	if s.cfg.OnAuthEvent != nil {
		s.cfg.OnAuthEvent(e)
	}
}

// SendOwned signs an AuthProof for this exact operation and sends it
// immediately ahead of the message it authorises (§6).
//
// The two frames go out under one hold of the write lock. Interleaving another
// sender's proof between them would hand this message someone else's
// authorisation -- which would not verify, so the failure would be a confusing
// refusal rather than a breach, but it would still be a bug that only appears
// under concurrency.
func (s *sess) SendOwned(ctx context.Context, capID byte, msgType uint16, msg proto.Message) error {
	peer := s.Peer()
	nonce, issuedAt, sig, err := s.cfg.Local.SignOwned(peer.DeviceID, capID, msgType)
	if err != nil {
		return err
	}
	proofPayload, err := proto.Marshal(&v1.AuthProof{Nonce: nonce, IssuedAt: issuedAt, Signature: sig})
	if err != nil {
		return err
	}
	payload, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	if len(payload) > MaxMessageSize {
		return protoErr(CodeMessageTooLarge, nil,
			"capID %d msgType %d marshals to %d bytes", capID, msgType, len(payload))
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := EncodeEnvelope(s.ctrl, Envelope{
		Version: EnvelopeVersion,
		CapID:   0,
		MsgType: MsgAuthProof,
		Payload: proofPayload,
	}); err != nil {
		return err
	}
	return EncodeEnvelope(s.ctrl, Envelope{
		Version: EnvelopeVersion,
		CapID:   capID,
		MsgType: msgType,
		Payload: payload,
	})
}

// OwnedSender is the initiating half of §6, exposed as an optional interface
// for the same reason Negotiated is: Session is a seam this package does not
// widen on its own. Callers type-assert.
type OwnedSender interface {
	SendOwned(ctx context.Context, capID byte, msgType uint16, msg proto.Message) error
}

var _ OwnedSender = (*sess)(nil)

// SendOwnedIfUnlocked sends msg with an AuthProof when this device holds a live
// unlock session for the peer, and plainly when it does not.
//
// It exists so an ordinary operation can be *upgraded* by the fact that someone
// authenticated recently, rather than forked into two code paths. A file offer
// is the case that matters: the same offer needs a human on the far end when it
// arrives unproven, and needs nobody when it arrives with a valid proof from an
// Owned device (PRD R3, R11). Reporting which happened lets the caller say so.
//
// A locked key is not an error here. ErrLocked means "nobody has authenticated
// on this machine for six hours", which is the normal state and no reason to
// refuse to send a file.
func SendOwnedIfUnlocked(ctx context.Context, sess Session, capID byte, msgType uint16, msg proto.Message) (owned bool, err error) {
	sender, ok := sess.(OwnedSender)
	if !ok {
		return false, sess.Send(ctx, capID, msgType, msg)
	}
	switch err := sender.SendOwned(ctx, capID, msgType, msg); {
	case err == nil:
		return true, nil
	case errors.Is(err, identity.ErrLocked), errors.Is(err, identity.ErrNoPrivilegeKey):
		return false, sess.Send(ctx, capID, msgType, msg)
	default:
		return false, err
	}
}

// OpenOwnedStream opens a capability stream whose first frame is an AuthProof
// for the operation about to be written on it (§6).
//
// D-57 named the hazard this closes. A proof sent on the control stream and a
// capability stream opened straight after are not ordered against each other by
// QUIC, so an Owned-level stream opener races its own authorisation: sometimes
// the proof arrives first and the operation is allowed, sometimes it does not
// and the same operation is refused. No Phase 1 capability declared Owned, so
// nothing exercised it; remotefs (§11, capID 3) does, and this is the answer --
// the proof travels on the stream it authorises, where QUIC's own ordering
// guarantees it arrives first.
//
// The caller writes the operation's own envelope next, with the same capID and
// msgType the proof was signed over. Anything else fails to verify.
func (s *sess) OpenOwnedStream(ctx context.Context, capID byte, msgType uint16) (Stream, error) {
	peer := s.Peer()
	nonce, issuedAt, sig, err := s.cfg.Local.SignOwned(peer.DeviceID, capID, msgType)
	if err != nil {
		return nil, err
	}
	proofPayload, err := proto.Marshal(&v1.AuthProof{Nonce: nonce, IssuedAt: issuedAt, Signature: sig})
	if err != nil {
		return nil, err
	}

	st, err := s.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	// No write lock: this stream belongs to this caller and nothing else writes
	// to it, which is the whole reason the race D-57 describes does not exist
	// here.
	if err := EncodeEnvelope(st, Envelope{
		Version: EnvelopeVersion,
		CapID:   0,
		MsgType: MsgAuthProof,
		Payload: proofPayload,
	}); err != nil {
		st.Reset(uint32(CodeProtocolViolation))
		return nil, err
	}
	return st, nil
}

// OwnedStreamOpener is OpenOwnedStream as an optional interface, for the same
// reason OwnedSender is one: Session is a seam this package does not widen
// unilaterally.
type OwnedStreamOpener interface {
	OpenOwnedStream(ctx context.Context, capID byte, msgType uint16) (Stream, error)
}

var _ OwnedStreamOpener = (*sess)(nil)

// OpenOwnedStreamIfUnlocked opens a proven stream when this device holds a live
// unlock for the peer, and a plain one when it does not.
//
// The plain stream is not a fallback that works: a capability requiring Owned
// will have it refused at the far end. It is the right shape anyway, because
// the refusal belongs to the peer that holds the policy -- and the caller is
// told which kind it got, so a UI can say "unlock first" instead of reporting
// a bare UNAUTHORISED.
func OpenOwnedStreamIfUnlocked(ctx context.Context, sess Session, capID byte, msgType uint16) (st Stream, owned bool, err error) {
	opener, ok := sess.(OwnedStreamOpener)
	if !ok {
		st, err = sess.OpenStream(ctx)
		return st, false, err
	}
	switch st, err := opener.OpenOwnedStream(ctx, capID, msgType); {
	case err == nil:
		return st, true, nil
	case errors.Is(err, identity.ErrLocked), errors.Is(err, identity.ErrNoPrivilegeKey):
		st, err := sess.OpenStream(ctx)
		return st, false, err
	default:
		return nil, false, err
	}
}
