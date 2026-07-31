package pairing

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	v1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// DefaultTimeout bounds one pairing exchange. PRD R2 wants pairing done in
// under 30 seconds; this is the outer limit before a half-finished exchange is
// abandoned, not a target. It leaves room for a user to walk to the other
// device and read the digits off its screen.
const DefaultTimeout = 2 * time.Minute

// notifyLinger is how long this package waits after writing a message that ends
// the conversation -- a declined PairConfirm, a Revoke that unpairs -- before
// letting the session close.
//
// It exists because QUIC's CONNECTION_CLOSE overtakes stream data that has not
// been flushed. Without it, the peer never receives the last thing it was told
// and waits out the full timeout instead, or worse, keeps a pinned key it was
// just told to discard. Both were observed, not theorised.
const notifyLinger = 250 * time.Millisecond

// PeerInfo is what a user is shown while deciding whether the digits match. It
// is everything the peer claimed in Hello and in its pairing message; none of
// it is authenticated by anything except the SAS the user is comparing.
type PeerInfo struct {
	DeviceID       identity.DeviceID
	DisplayName    string
	Platform       string
	ProtectionTier identity.ProtectionTier
}

// ConfirmFunc shows the six-digit short authentication string to the local user
// and reports whether they confirmed that it matches the other device.
//
// PROTOCOL.md §5.2: the comparison is the entire security of pairing, and there
// is no "skip verification" path. An implementation of this that returns true
// without a human having actually compared the digits defeats pairing
// completely -- a man in the middle is detected by nothing else.
//
// Returning an error aborts pairing and tells the peer it was declined.
type ConfirmFunc func(ctx context.Context, sas string, peer PeerInfo) (bool, error)

// Config is what Handler needs. Local, Store and Confirm are required.
type Config struct {
	Local identity.Identity
	Store identity.TrustStore

	// DisplayName and Platform are what this device calls itself in
	// PairRequest / PairResponse (§5.2).
	DisplayName string
	Platform    string

	// Confirm shows the SAS and reports the user's answer. Required.
	Confirm ConfirmFunc

	// GrantAtPairing is the set of capability IDs (wire values) recorded as
	// persistently granted on a newly paired peer (§6.2, §6.4). Empty is the
	// default and means every capability prompts once per session; it is the
	// conservative choice, and widening it is a product decision rather than a
	// protocol one.
	GrantAtPairing []byte

	// Timeout bounds one exchange. Zero means DefaultTimeout.
	Timeout time.Duration

	// Now is the clock, for tests. Zero value means time.Now.
	Now func() time.Time

	// Logger is optional.
	Logger *slog.Logger
}

// Handler implements session.Handler at capID 0: the pairing exchange (§5.2),
// revocation (§6.1) and capability grants (§6.4) all travel as control
// messages, so one handler owns all three.
//
// One Handler serves every session from one listener or dialer, which is why
// its per-session state is keyed by session rather than held in fields.
type Handler struct {
	cfg Config
	log *slog.Logger
	now func() time.Time

	mu        sync.Mutex
	exchanges map[session.Session]*exchange
	guards    map[session.Session]*Guard
	window    int // open pairing windows; unpaired peers are admitted while > 0
}

var _ session.Handler = (*Handler)(nil)

// exchange is the inbox for one session's pairing messages. Each channel holds
// exactly one message: the exchange is strictly one request, one response and
// one confirm per side, so a second of any of them is a peer misbehaving and is
// dropped rather than queued.
type exchange struct {
	req  chan *v1.PairRequest
	resp chan *v1.PairResponse
	conf chan *v1.PairConfirm
}

func newExchange() *exchange {
	return &exchange{
		req:  make(chan *v1.PairRequest, 1),
		resp: make(chan *v1.PairResponse, 1),
		conf: make(chan *v1.PairConfirm, 1),
	}
}

// NewHandler validates cfg and returns the handler to register at capID 0.
func NewHandler(cfg Config) (*Handler, error) {
	if cfg.Local == nil {
		return nil, errors.New("pairing: Config.Local is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("pairing: Config.Store is required")
	}
	if cfg.Confirm == nil {
		return nil, ErrNoConfirm
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Handler{
		cfg:       cfg,
		log:       log.With("component", "pairing"),
		now:       now,
		exchanges: make(map[session.Session]*exchange),
		guards:    make(map[session.Session]*Guard),
	}, nil
}

// CapID reports 0: pairing, revocation and grants are session-layer control
// messages, not a negotiated capability (PROTOCOL.md §3).
func (h *Handler) CapID() byte { return 0 }

// ServeStream reports ErrUnknownMsgType: nothing in §5 or §6 opens a stream.
func (h *Handler) ServeStream(context.Context, session.Session, session.Stream, uint16, []byte) error {
	return session.ErrUnknownMsgType
}

// Serve routes one inbound control message.
func (h *Handler) Serve(ctx context.Context, sess session.Session, msgType uint16, payload []byte) error {
	switch v1.ControlMessageType(msgType) {
	case v1.ControlMessageType_CONTROL_MESSAGE_TYPE_PAIR_REQUEST:
		var m v1.PairRequest
		if err := proto.Unmarshal(payload, &m); err != nil {
			return fmt.Errorf("pairing: malformed PairRequest: %w", err)
		}
		return deliver(h.inbox(sess).req, &m)

	case v1.ControlMessageType_CONTROL_MESSAGE_TYPE_PAIR_RESPONSE:
		var m v1.PairResponse
		if err := proto.Unmarshal(payload, &m); err != nil {
			return fmt.Errorf("pairing: malformed PairResponse: %w", err)
		}
		return deliver(h.inbox(sess).resp, &m)

	case v1.ControlMessageType_CONTROL_MESSAGE_TYPE_PAIR_CONFIRM:
		var m v1.PairConfirm
		if err := proto.Unmarshal(payload, &m); err != nil {
			return fmt.Errorf("pairing: malformed PairConfirm: %w", err)
		}
		return deliver(h.inbox(sess).conf, &m)

	case v1.ControlMessageType_CONTROL_MESSAGE_TYPE_REVOKE:
		var m v1.Revoke
		if err := proto.Unmarshal(payload, &m); err != nil {
			return fmt.Errorf("pairing: malformed Revoke: %w", err)
		}
		return h.onRevoke(sess, &m)

	case v1.ControlMessageType_CONTROL_MESSAGE_TYPE_CAPABILITY_GRANT:
		var m v1.CapabilityGrant
		if err := proto.Unmarshal(payload, &m); err != nil {
			return fmt.Errorf("pairing: malformed CapabilityGrant: %w", err)
		}
		return h.onGrant(sess, &m)

	case v1.ControlMessageType_CONTROL_MESSAGE_TYPE_PAIR_OFFER:
		// §5.1 puts the offer out of band. A peer sending one on the connection
		// it is already on has authenticated nothing, so there is nothing to do
		// with it.
		return fmt.Errorf("pairing: PairOffer arrived on the connection; §5.1 makes it out-of-band only")

	default:
		return session.ErrUnknownMsgType
	}
}

func deliver[T any](ch chan T, m T) error {
	select {
	case ch <- m:
		return nil
	default:
		return fmt.Errorf("pairing: duplicate or unexpected pairing message dropped")
	}
}

// inbox returns the per-session exchange, creating it if a message arrived
// before Initiate or Await registered one. Without that, a peer that sends its
// PairRequest the instant Hello completes would race the local user pressing
// "pair" and lose.
func (h *Handler) inbox(sess session.Session) *exchange {
	h.mu.Lock()
	defer h.mu.Unlock()
	ex, ok := h.exchanges[sess]
	if !ok {
		ex = newExchange()
		h.exchanges[sess] = ex
	}
	return ex
}

// Detach drops the per-session state. Callers release a session here; nothing
// else prunes the maps, because only the caller knows a session has ended.
func (h *Handler) Detach(sess session.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.exchanges, sess)
	delete(h.guards, sess)
}

// --- the exchange ------------------------------------------------------------

// Initiate runs §5.2 from the side that scanned the offer and dialled: it sends
// PairRequest, waits for PairResponse, derives the SAS, confirms with the local
// user, exchanges PairConfirm, and on mutual acceptance pins both of the peer's
// keys at level Trusted.
//
// offer is what was scanned. Its fingerprint is checked against the key the
// peer actually presented in TLS before anything is sent (§5.1); that check is
// what authenticates the far end to this one, and the SAS authenticates this
// end to the far one.
func (h *Handler) Initiate(ctx context.Context, sess session.Session, offer *v1.PairOffer) (identity.Peer, error) {
	ctx, cancel := context.WithTimeout(ctx, h.cfg.Timeout)
	defer cancel()

	presented := sess.Peer().IdentityPublicKey
	if err := VerifyOffer(offer, presented); err != nil {
		return identity.Peer{}, err
	}

	ex := h.inbox(sess)
	nonce, err := NewNonce()
	if err != nil {
		return identity.Peer{}, err
	}
	local := h.localParty(nonce)

	req := &v1.PairRequest{
		IdentityPublicKey:  local.IdentityKey,
		PrivilegePublicKey: local.PrivilegeKey,
		DisplayName:        h.cfg.DisplayName,
		Platform:           h.cfg.Platform,
		Nonce:              nonce,
		ProtectionTier:     session.ProtectionTierToWire(h.localTier()),
	}
	if err := sess.Send(ctx, 0, session.MsgType(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_PAIR_REQUEST), req); err != nil {
		return identity.Peer{}, fmt.Errorf("pairing: sending PairRequest: %w", err)
	}

	resp, err := wait(ctx, ex.resp, "PairResponse")
	if err != nil {
		return identity.Peer{}, err
	}
	remote, err := h.checkRemote(sess, resp.IdentityPublicKey, resp.PrivilegePublicKey,
		resp.Nonce, nonce, resp.DisplayName, resp.Platform, resp.ProtectionTier)
	if err != nil {
		return identity.Peer{}, err
	}

	// The offerer displayed the code, so its nonce comes first in the
	// transcript. Here the offerer is the remote side.
	sas, err := SAS(remote.party, local)
	if err != nil {
		return identity.Peer{}, err
	}
	return h.settle(ctx, sess, ex, sas, remote)
}

// Await runs §5.2 from the side that displayed the offer and accepted the
// connection: it waits for PairRequest, replies with PairResponse, and then
// takes the same confirm-and-pin path as Initiate.
func (h *Handler) Await(ctx context.Context, sess session.Session) (identity.Peer, error) {
	ctx, cancel := context.WithTimeout(ctx, h.cfg.Timeout)
	defer cancel()

	ex := h.inbox(sess)
	req, err := wait(ctx, ex.req, "PairRequest")
	if err != nil {
		return identity.Peer{}, err
	}

	nonce, err := NewNonce()
	if err != nil {
		return identity.Peer{}, err
	}
	local := h.localParty(nonce)

	remote, err := h.checkRemote(sess, req.IdentityPublicKey, req.PrivilegePublicKey,
		req.Nonce, nonce, req.DisplayName, req.Platform, req.ProtectionTier)
	if err != nil {
		return identity.Peer{}, err
	}

	resp := &v1.PairResponse{
		IdentityPublicKey:  local.IdentityKey,
		PrivilegePublicKey: local.PrivilegeKey,
		DisplayName:        h.cfg.DisplayName,
		Platform:           h.cfg.Platform,
		Nonce:              nonce,
		ProtectionTier:     session.ProtectionTierToWire(h.localTier()),
	}
	if err := sess.Send(ctx, 0, session.MsgType(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_PAIR_RESPONSE), resp); err != nil {
		return identity.Peer{}, fmt.Errorf("pairing: sending PairResponse: %w", err)
	}

	// This side displayed the offer, so the local nonce comes first.
	sas, err := SAS(local, remote.party)
	if err != nil {
		return identity.Peer{}, err
	}
	return h.settle(ctx, sess, ex, sas, remote)
}

// settle is the half of §5.2 both roles share: show the SAS, exchange
// PairConfirm, and pin on mutual acceptance.
func (h *Handler) settle(ctx context.Context, sess session.Session, ex *exchange, sas string, remote remoteInfo) (identity.Peer, error) {
	accepted, confirmErr := h.cfg.Confirm(ctx, sas, remote.info)
	if confirmErr != nil {
		accepted = false
	}

	// Tell the peer either way. A device that declines silently leaves the
	// other user staring at a screen until the timeout.
	sendErr := sess.Send(ctx, 0,
		session.MsgType(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_PAIR_CONFIRM),
		&v1.PairConfirm{Accepted: accepted})

	// A decline ends the exchange here, and the caller will close the session as
	// soon as this returns. Give the "no" time to reach the peer first, or the
	// other user is left watching a screen until the two-minute timeout while
	// this one has already walked away.
	if confirmErr != nil || !accepted {
		time.Sleep(notifyLinger)
	}

	switch {
	case confirmErr != nil:
		return identity.Peer{}, fmt.Errorf("pairing: confirmation failed: %w", confirmErr)
	case !accepted:
		return identity.Peer{}, ErrDeclined
	case sendErr != nil:
		return identity.Peer{}, fmt.Errorf("pairing: sending PairConfirm: %w", sendErr)
	}

	peerConfirm, err := wait(ctx, ex.conf, "PairConfirm")
	if err != nil {
		return identity.Peer{}, err
	}
	if !peerConfirm.Accepted {
		return identity.Peer{}, ErrPeerDeclined
	}

	// §5.2: pairing completes only when both peers send accepted = true. Both
	// keys are then pinned and the peer is recorded at Trusted. Promotion to
	// Owned is a separate deliberate act (PRD R3) and never happens here.
	now := h.now().UnixMilli()
	created := now
	if prev, ok := h.cfg.Store.Get(remote.deviceID); ok {
		created = prev.CreatedAt
	}
	peer := identity.Peer{
		DeviceID:            remote.deviceID,
		IdentityPublicKey:   remote.party.IdentityKey,
		PrivilegePublicKey:  remote.party.PrivilegeKey,
		DisplayName:         remote.info.DisplayName,
		Platform:            remote.info.Platform,
		Level:               identity.LevelTrusted,
		GrantedCapabilities: append([]byte(nil), h.cfg.GrantAtPairing...),
		AuthPolicy:          "timed",
		ProtectionTier:      remote.info.ProtectionTier,
		CreatedAt:           created,
		LastSeen:            now,
	}
	if err := h.cfg.Store.Put(peer); err != nil {
		return identity.Peer{}, fmt.Errorf("pairing: recording peer %s: %w", remote.deviceID, err)
	}
	h.GuardFor(sess).set(identity.LevelTrusted, "paired")
	h.log.Info("paired", "peer", remote.deviceID, "name", remote.info.DisplayName, "level", "trusted")
	return peer, nil
}

// remoteInfo is the validated far side of an exchange.
type remoteInfo struct {
	deviceID identity.DeviceID
	party    Party
	info     PeerInfo
}

// checkRemote validates a PairRequest or PairResponse against what TLS already
// established about the peer.
//
// The binding check is the important one: the identity key in the pairing
// message must be exactly the key that terminated TLS. Trusting the claimed key
// instead would let anything on the path hand us a key of its choosing to pin,
// and the SAS would then be computed over that key and agree perfectly.
func (h *Handler) checkRemote(sess session.Session, idKey, privKey, nonce, localNonce []byte,
	displayName, platform string, tier v1.ProtectionTier) (remoteInfo, error) {

	presented := sess.Peer().IdentityPublicKey
	if len(presented) != ed25519.PublicKeySize {
		return remoteInfo{}, fmt.Errorf("pairing: session has no peer identity key")
	}
	if len(idKey) != ed25519.PublicKeySize || subtle.ConstantTimeCompare(idKey, presented) != 1 {
		return remoteInfo{}, ErrKeyBinding
	}
	if n := len(privKey); n != 0 && n != ed25519.PublicKeySize {
		return remoteInfo{}, fmt.Errorf("pairing: peer privilege key is %d bytes, want %d or none",
			n, ed25519.PublicKeySize)
	}
	if len(nonce) != NonceLen {
		return remoteInfo{}, fmt.Errorf("pairing: peer nonce is %d bytes, want %d", len(nonce), NonceLen)
	}
	if subtle.ConstantTimeCompare(nonce, localNonce) == 1 {
		// Both nonces equal means the transcript was reflected back at us; a
		// peer that picked our nonce by chance is a 2^-256 event.
		return remoteInfo{}, fmt.Errorf("pairing: peer echoed our nonce")
	}

	domainTier := session.ProtectionTierFromWire(tier)
	if domainTier == identity.TierNone && len(privKey) != 0 {
		// D-21 tier 3 holds no privilege key. A peer claiming both is
		// inconsistent; keep the claim that limits it (tier none) and drop the
		// key, so nothing later treats it as protected.
		h.log.Warn("peer claims protection tier none but sent a privilege key; discarding the key",
			"peer", sess.Peer().DeviceID)
		privKey = nil
	}

	derived := identity.DeriveDeviceID(idKey)
	return remoteInfo{
		deviceID: derived,
		party: Party{
			IdentityKey:  append(ed25519.PublicKey(nil), idKey...),
			PrivilegeKey: append(ed25519.PublicKey(nil), privKey...),
			Nonce:        append([]byte(nil), nonce...),
		},
		info: PeerInfo{
			DeviceID:       derived,
			DisplayName:    displayName,
			Platform:       platform,
			ProtectionTier: domainTier,
		},
	}, nil
}

func (h *Handler) localParty(nonce []byte) Party {
	return Party{
		IdentityKey:  h.cfg.Local.IdentityPublic(),
		PrivilegeKey: h.cfg.Local.PrivilegePublic(),
		Nonce:        nonce,
	}
}

// localTierReporter is the accessor identity.Identity does not declare.
// session.go works around the same gap the same way; see the report.
type localTierReporter interface {
	ProtectionTier() identity.ProtectionTier
}

func (h *Handler) localTier() identity.ProtectionTier {
	if r, ok := h.cfg.Local.(localTierReporter); ok {
		return r.ProtectionTier()
	}
	// Under-claiming is the safe direction: a peer at tier none must never be
	// granted Owned (D-21).
	return identity.TierNone
}

func wait[T any](ctx context.Context, ch <-chan T, what string) (T, error) {
	var zero T
	select {
	case m := <-ch:
		return m, nil
	case <-ctx.Done():
		return zero, fmt.Errorf("pairing: waiting for %s: %w", what, ctx.Err())
	}
}
