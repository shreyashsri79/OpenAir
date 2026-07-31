package session

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/apernet/quic-go"
	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	v1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// ProtocolVersion is the highest `proto_version` this build advertises in
// Hello. PROTOCOL.md is version 1 (§4).
const ProtocolVersion uint32 = 1

// capQueueDepth bounds the per-capability inbound queue. Control messages are
// small and infrequent; a capability that lets this fill is wedged, and
// blocking the control loop is the honest signal for that.
const capQueueDepth = 64

// transport is everything the session needs from the connection underneath it.
//
// It exists so the control loop and Hello exchange can be tested over an
// in-memory pipe: New's signature takes a concrete *quic.Conn (the agreed seam
// with internal/conn), and a concrete type cannot be faked. Nothing outside
// this package sees it.
type transport interface {
	// OpenStream opens a new bidirectional stream.
	OpenStream(ctx context.Context) (Stream, error)
	// AcceptStream accepts the next bidirectional stream opened by the peer.
	AcceptStream(ctx context.Context) (Stream, error)
	SendDatagram(b []byte) error
	// PeerPublicKey is the peer's TLS identity key, against which its claimed
	// DeviceID is checked (§4).
	PeerPublicKey() (ed25519.PublicKey, error)
	PathInfo() PathInfo
	CloseWithError(code ErrorCode, reason string) error
}

// --- quic-go binding ---------------------------------------------------------

type quicTransport struct{ qc *quic.Conn }

func (t quicTransport) OpenStream(ctx context.Context) (Stream, error) {
	st, err := t.qc.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return quicStream{st}, nil
}

func (t quicTransport) AcceptStream(ctx context.Context) (Stream, error) {
	st, err := t.qc.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return quicStream{st}, nil
}

func (t quicTransport) SendDatagram(b []byte) error { return t.qc.SendDatagram(b) }

func (t quicTransport) PeerPublicKey() (ed25519.PublicKey, error) {
	certs := t.qc.ConnectionState().TLS.PeerCertificates
	if len(certs) == 0 {
		return nil, protoErr(CodeProtocolViolation, nil, "peer presented no TLS certificate")
	}
	pub, ok := certs[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, protoErr(CodeProtocolViolation, nil,
			"peer TLS key is %T, expected ed25519.PublicKey", certs[0].PublicKey)
	}
	return pub, nil
}

func (t quicTransport) PathInfo() PathInfo {
	st := t.qc.ConnectionStats()
	rtt := st.SmoothedRTT
	if rtt == 0 {
		rtt = st.LatestRTT
	}
	// Bandwidth still needs §7.2's PathInfo control message, which remains a
	// declared gap. The class here is the transport's own guess -- a direct
	// dial to an address -- and Config.PathClass overrides it for a caller that
	// knows better, which since M9 is the path layer underneath.
	return PathInfo{
		RTTMillis: uint32(rtt / time.Millisecond),
		Class:     "lan",
	}
}

func (t quicTransport) CloseWithError(code ErrorCode, reason string) error {
	return t.qc.CloseWithError(quic.ApplicationErrorCode(code), reason)
}

type quicStream struct{ st *quic.Stream }

func (s quicStream) Read(p []byte) (int, error)  { return s.st.Read(p) }
func (s quicStream) Write(p []byte) (int, error) { return s.st.Write(p) }
func (s quicStream) Close() error                { return s.st.Close() }

func (s quicStream) Reset(code uint32) {
	s.st.CancelWrite(quic.StreamErrorCode(code))
	s.st.CancelRead(quic.StreamErrorCode(code))
}

// inbound is one queued message plus the authorisation decision made for it on
// the control loop. The decision travels with the message rather than being
// recomputed in the queue goroutine, because the proof it rests on has already
// been spent by then (§6, auth.go).
type inbound struct {
	env   Envelope
	owned bool
}

// --- session -----------------------------------------------------------------

type sess struct {
	cfg      Config
	tr       transport
	ctrl     Stream
	log      *slog.Logger
	handlers map[byte]Handler

	ctx    context.Context
	cancel context.CancelFunc

	writeMu sync.Mutex // serialises whole frames onto the control stream

	mu      sync.RWMutex
	peer    identity.Peer
	version uint32          // effective protocol version: min of the two
	caps    map[byte]uint32 // negotiated capID -> effective capability version

	queues map[byte]chan inbound

	// auth is §6's verifier: the proofs this peer has offered and the nonces it
	// has already spent. See auth.go.
	auth *authVerifier
	// haveRecord reports that Config.PeerLookup produced a stored record for
	// this peer, so peer.Level is trust-store truth rather than a zero value.
	haveRecord bool

	closeOnce sync.Once
	closeErr  error
	done      chan struct{}
}

// newSession is New's body, taking the internal transport rather than a
// concrete *quic.Conn. See the doc comment on New in types.go.
func newSession(ctx context.Context, tr transport, cfg Config) (Session, error) {
	peerKey, err := tr.PeerPublicKey()
	if err != nil {
		return nil, err
	}
	// §4: the DeviceID the peer claims in Hello must be the one its TLS key
	// derives. Derived here, before Hello, so there is a fixed value to compare
	// against rather than one computed from what the peer sent.
	derived := identity.DeriveDeviceID(peerKey)

	// If a key is already pinned for this peer, the TLS key must be it. An
	// unpinned Peer (empty DeviceID) is the pairing case, §5, where there is
	// nothing yet to compare against.
	if len(cfg.Peer.IdentityPublicKey) > 0 && !cfg.Peer.IdentityPublicKey.Equal(peerKey) {
		return nil, protoErr(CodeKeyMismatch, nil,
			"peer TLS key does not match the pinned key for %s", cfg.Peer.DeviceID)
	}
	if cfg.Peer.DeviceID != "" && cfg.Peer.DeviceID != derived {
		return nil, protoErr(CodeKeyMismatch, nil,
			"peer TLS key derives DeviceID %s, pinned record says %s", derived, cfg.Peer.DeviceID)
	}

	// §1.1: the control stream is the first bidirectional stream, opened by the
	// initiator. It stays open for the life of the session.
	var ctrl Stream
	if cfg.Initiator {
		ctrl, err = tr.OpenStream(ctx)
	} else {
		ctrl, err = tr.AcceptStream(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("session: control stream: %w", err)
	}

	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &sess{
		cfg:      cfg,
		tr:       tr,
		ctrl:     ctrl,
		log:      slog.Default().With("component", "session"),
		handlers: make(map[byte]Handler, len(cfg.Handlers)),
		ctx:      sctx,
		cancel:   cancel,
		peer:     cfg.Peer,
		caps:     map[byte]uint32{},
		queues:   map[byte]chan inbound{},
		auth:     newAuthVerifier(cfg.Local.DeviceID(), nil),
		done:     make(chan struct{}),
	}
	for id, h := range cfg.Handlers {
		if h != nil {
			s.handlers[id] = h
		}
	}

	if err := s.exchangeHello(ctx, derived, peerKey); err != nil {
		cancel()
		return nil, err
	}

	// Authorisation happens here: after Hello has populated the peer record,
	// and before startQueues makes it possible for a capability message to be
	// dispatched. See Config.Authorize.
	if cfg.Authorize != nil {
		if err := cfg.Authorize(s.Peer()); err != nil {
			cancel()
			_ = ctrl.Close()
			return nil, fmt.Errorf("session: peer %s not authorised: %w", derived, err)
		}
	}

	s.startQueues()
	go s.readLoop()
	go s.acceptLoop()
	return s, nil
}

// exchangeHello implements §4: both peers send Hello and neither sends anything
// else until it has both sent and received one.
func (s *sess) exchangeHello(ctx context.Context, derived identity.DeviceID, peerKey ed25519.PublicKey) error {
	local := &v1.Hello{
		ProtoVersion:   ProtocolVersion,
		DeviceId:       string(s.cfg.Local.DeviceID()),
		DisplayName:    s.cfg.DisplayName,
		Platform:       s.cfg.Platform,
		Capabilities:   s.localCapabilities(),
		ProtectionTier: ProtectionTierToWire(s.localProtectionTier()),
	}
	if err := s.Send(ctx, 0, MsgType(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_HELLO), local); err != nil {
		return fmt.Errorf("session: sending hello: %w", err)
	}

	env, err := s.readOne(ctx)
	if err != nil {
		return fmt.Errorf("session: awaiting hello: %w", err)
	}
	if env.CapID != 0 || env.MsgType != MsgType(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_HELLO) {
		// §4 is explicit that nothing may precede Hello, so §3.1's
		// ignore-and-continue does not apply here: there is no negotiated state
		// yet to interpret the message against.
		return protoErr(CodeProtocolViolation, nil,
			"expected Hello, got capID %d msgType %d before the handshake completed", env.CapID, env.MsgType)
	}

	var remote v1.Hello
	if err := proto.Unmarshal(env.Payload, &remote); err != nil {
		return protoErr(CodeProtocolViolation, err, "malformed Hello")
	}
	if identity.DeviceID(remote.DeviceId) != derived {
		return protoErr(CodeProtocolViolation, nil,
			"Hello claims DeviceID %q but the TLS key derives %q", remote.DeviceId, derived)
	}

	version := min(ProtocolVersion, remote.ProtoVersion)
	caps := s.negotiate(remote.Capabilities)

	s.mu.Lock()
	s.version = version
	s.caps = caps
	s.peer.DeviceID = derived
	s.peer.IdentityPublicKey = peerKey
	if remote.DisplayName != "" {
		s.peer.DisplayName = remote.DisplayName
	}
	if remote.Platform != "" {
		s.peer.Platform = remote.Platform
	}
	s.peer.ProtectionTier = ProtectionTierFromWire(remote.ProtectionTier)

	// Hello says who the peer claims to be; the trust store says what it is
	// allowed to do. The accepting side has no pinned record until here -- a
	// listener does not know who is calling until Hello arrives -- so this is
	// where the stored keys and level join the session. Without it the pinned
	// privilege key would be missing on exactly the side that has to verify
	// AuthProof against it (§6).
	if s.cfg.PeerLookup != nil {
		if stored, ok := s.cfg.PeerLookup(derived); ok {
			s.peer.PrivilegePublicKey = stored.PrivilegePublicKey
			s.peer.Level = stored.Level
			s.peer.GrantedCapabilities = stored.GrantedCapabilities
			s.peer.AuthPolicy = stored.AuthPolicy
			s.peer.TokenGrantedAt = stored.TokenGrantedAt
			if s.peer.DisplayName == "" {
				s.peer.DisplayName = stored.DisplayName
			}
			s.haveRecord = true
		}
	} else if len(s.cfg.Peer.PrivilegePublicKey) > 0 {
		// The dialling side already carries the stored record in Config.Peer.
		s.haveRecord = true
	}
	s.mu.Unlock()
	return nil
}

// localProtectionTierReporter is the optional accessor this package needs and
// identity.Identity does not currently declare. Hello carries a protection tier
// (§4, §7.3) and neither identity.Identity nor session.Config exposes the local
// one, so this bridges the gap without touching either seam: if the concrete
// Identity grows the method, Hello starts reporting the truth with no change
// here. Until then it reports tier 3 (none), which fails in the safe direction:
// a peer at tier 3 must not be granted Owned (§7.3), so an unknown tier
// under-claims rather than over-claims.
type localProtectionTierReporter interface {
	ProtectionTier() identity.ProtectionTier
}

func (s *sess) localProtectionTier() identity.ProtectionTier {
	if r, ok := s.cfg.Local.(localProtectionTierReporter); ok {
		return r.ProtectionTier()
	}
	s.log.Debug("identity does not report a protection tier; advertising none")
	return identity.TierNone
}

// localCapabilities advertises every registered handler, sorted so the encoding
// is deterministic (golden vectors depend on it). capID 0 is the session layer
// itself, not a negotiable capability, so it is never advertised even if a
// handler is registered for it.
func (s *sess) localCapabilities() []*v1.Capability {
	ids := make([]int, 0, len(s.handlers))
	for id := range s.handlers {
		if id == 0 {
			continue
		}
		ids = append(ids, int(id))
	}
	sort.Ints(ids)

	out := make([]*v1.Capability, 0, len(ids))
	for _, id := range ids {
		enumID, ok := CapIDFromWire(byte(id))
		if !ok {
			// A handler registered under a capID this build's schema does not
			// know cannot be advertised; there is no enum value for it.
			s.log.Warn("skipping unrepresentable capability", "capID", id)
			continue
		}
		out = append(out, &v1.Capability{Id: enumID, Version: localCapVersion})
	}
	return out
}

// localCapVersion is the capability-local version advertised for every
// capability. session.Handler exposes no version accessor, so there is nothing
// per-capability to report yet; see the report note.
const localCapVersion uint32 = 1

// negotiate implements §4: a capability is usable only if both peers list it,
// its effective version is the lower of the two, and one-sided capabilities are
// silently dropped rather than treated as an error.
func (s *sess) negotiate(remote []*v1.Capability) map[byte]uint32 {
	out := map[byte]uint32{}
	for _, c := range remote {
		if c == nil {
			continue
		}
		capID, ok := CapIDToWire(c.Id)
		if !ok {
			continue
		}
		if capID == 0 {
			continue
		}
		if _, have := s.handlers[capID]; !have {
			continue
		}
		out[capID] = min(localCapVersion, c.Version)
	}
	return out
}

// readOne reads a single envelope while honouring ctx. quic-go streams take
// deadlines, not contexts, so cancellation is a watchdog that resets the
// stream; the read then fails out rather than hanging until the idle timeout.
func (s *sess) readOne(ctx context.Context) (Envelope, error) {
	type result struct {
		env Envelope
		err error
	}
	ch := make(chan result, 1)
	go func() {
		env, err := DecodeEnvelope(s.ctrl)
		ch <- result{env, err}
	}()
	select {
	case r := <-ch:
		return r.env, r.err
	case <-ctx.Done():
		s.ctrl.Reset(uint32(CodeNoError))
		return Envelope{}, ctx.Err()
	}
}

// --- control loop ------------------------------------------------------------

// startQueues runs once, before readLoop and acceptLoop exist, so it needs no
// locking and the maps it fills are only read afterwards.
func (s *sess) startQueues() {
	for capID := range s.handlers {
		if capID != 0 {
			if _, negotiated := s.caps[capID]; !negotiated {
				continue
			}
		}
		q := make(chan inbound, capQueueDepth)
		s.queues[capID] = q
		go s.serveQueue(capID, q)
	}
}

// serveQueue runs one capability's inbound messages in order. Per-capability
// rather than one shared goroutine, because a capability that does a
// request/response round trip on the control stream would deadlock a
// synchronous dispatcher, and one goroutine per message would lose the ordering
// that offer/cancel sequences depend on.
func (s *sess) serveQueue(capID byte, q <-chan inbound) {
	h := s.handlers[capID]
	for {
		select {
		case <-s.ctx.Done():
			return
		case in := <-q:
			env := in.env
			err := h.Serve(withOwned(s.ctx, in.owned), s, env.MsgType, env.Payload)
			switch {
			case err == nil:
			case errors.Is(err, ErrUnknownMsgType):
				// §3.1: known capID, unknown msgType -- ignore and continue.
				s.log.Debug("ignoring unknown message type",
					"capID", capID, "msgType", env.MsgType)
			default:
				// A capability failing one message is operation-fatal at worst
				// (§10); it does not take the connection down.
				s.log.Warn("capability handler failed",
					"capID", capID, "msgType", env.MsgType, "err", err)
			}
		}
	}
}

func (s *sess) readLoop() {
	for {
		env, err := DecodeEnvelope(s.ctrl)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// §1.1: closing the control stream terminates the session.
				s.closeWith(CodeNoError, "peer closed the control stream", nil)
				return
			}
			code, ok := ErrorCodeOf(err)
			if !ok {
				code = CodeNoError
			}
			s.closeWith(code, err.Error(), err)
			return
		}
		s.dispatch(env)
	}
}

func (s *sess) dispatch(env Envelope) {
	if env.CapID == 0 && env.MsgType == MsgAuthProof {
		// §6's proof is consumed by the session layer, never by a handler: it
		// authorises the *next* message, and a capability has no way to know
		// that. Malformed proofs are dropped rather than fatal -- the operation
		// they would have authorised is refused a moment later for having none.
		if err := s.auth.receive(env.Payload); err != nil {
			s.log.Warn("discarding AuthProof", "peer", s.Peer().DeviceID, "err", err)
		}
		return
	}
	if env.CapID == 0 && !knownControlMsgType(env.MsgType) {
		// §3.1: capID 0 is a capID we recognise, so an unrecognised msgType
		// within it is ignored rather than fatal.
		s.log.Debug("ignoring unknown control message type", "msgType", env.MsgType)
		return
	}
	if env.CapID != 0 {
		s.mu.RLock()
		_, negotiated := s.caps[env.CapID]
		s.mu.RUnlock()
		if !negotiated {
			// §3.1: unrecognised capID -- and a capability that was never
			// negotiated is, for this session, exactly that.
			s.log.Debug("ignoring message for unnegotiated capability", "capID", env.CapID)
			return
		}
	}

	q, ok := s.queues[env.CapID]
	if !ok {
		s.log.Debug("ignoring message with no handler", "capID", env.CapID)
		return
	}

	// §6's gate, on the control loop and before the message is queued. Doing it
	// in the queue goroutine instead would authorise messages concurrently with
	// each other, and the proofs they spend are single-use.
	owned, allowed := s.authorizeInbound(s.handlers[env.CapID], env.CapID, env.MsgType)
	if !allowed {
		return
	}

	select {
	case q <- inbound{env: env, owned: owned}:
	case <-s.ctx.Done():
	}
}

func knownControlMsgType(t uint16) bool {
	if t == 0 {
		return false
	}
	_, ok := v1.ControlMessageType_name[int32(t)]
	return ok
}

// acceptLoop handles capability streams. §3: the first message on every
// capability stream is an envelope; ServeStream takes it from there.
func (s *sess) acceptLoop() {
	for {
		st, err := s.tr.AcceptStream(s.ctx)
		if err != nil {
			return
		}
		go s.serveStream(st)
	}
}

func (s *sess) serveStream(st Stream) {
	env, err := DecodeEnvelope(st)
	if err != nil {
		code, ok := ErrorCodeOf(err)
		if !ok {
			code = CodeProtocolViolation
		}
		st.Reset(uint32(code))
		return
	}

	// §6 on a stream: an Owned-level capability sends its AuthProof as the
	// first frame of the stream it authorises, because a proof on the control
	// stream is not ordered against a stream opened after it (D-57, D-71). The
	// proof joins the same pending set the control path uses and is spent by
	// the operation that follows it here.
	if env.CapID == 0 && env.MsgType == MsgAuthProof {
		if err := s.auth.receive(env.Payload); err != nil {
			code, ok := ErrorCodeOf(err)
			if !ok {
				code = CodeUnauthorised
			}
			st.Reset(uint32(code))
			return
		}
		env, err = DecodeEnvelope(st)
		if err != nil {
			code, ok := ErrorCodeOf(err)
			if !ok {
				code = CodeProtocolViolation
			}
			st.Reset(uint32(code))
			return
		}
		if env.CapID == 0 {
			// A proof authorises a capability operation. Another proof, or any
			// other control message, is not one.
			st.Reset(uint32(CodeProtocolViolation))
			return
		}
	}

	s.mu.RLock()
	_, negotiated := s.caps[env.CapID]
	s.mu.RUnlock()
	h, have := s.handlers[env.CapID]
	if env.CapID == 0 || !negotiated || !have {
		// §3.1 says ignore and continue, which for a stream means declining
		// this stream without disturbing the connection. There is no way to
		// "skip" a stream whose framing beyond the envelope is unknown.
		st.Reset(uint32(CodeCapabilityUnavailable))
		return
	}
	owned, allowed := s.authorizeInbound(h, env.CapID, env.MsgType)
	if !allowed {
		st.Reset(uint32(CodeUnauthorised))
		return
	}
	if err := h.ServeStream(withOwned(s.ctx, owned), s, st, env.MsgType, env.Payload); err != nil {
		if errors.Is(err, ErrUnknownMsgType) {
			st.Reset(uint32(CodeCapabilityUnavailable))
			return
		}
		// A capability that names a §10 code has said something the peer can
		// act on -- UNAUTHORISED means "ask for access", NOT_FOUND means "that
		// path is not there". Flattening those to RESOURCE_EXHAUSTED would
		// leave every failure looking like the source was overloaded.
		code, ok := ErrorCodeOf(err)
		if !ok {
			code = CodeResourceExhausted
		}
		s.log.Warn("capability stream handler failed",
			"capID", env.CapID, "msgType", env.MsgType, "code", code.String(), "err", err)
		st.Reset(uint32(code))
	}
}

// --- Session interface -------------------------------------------------------

func (s *sess) Peer() identity.Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.peer
}

// Negotiated exposes the outcome of §4's negotiation. It is deliberately not
// part of Session: Session is the seam agreed with internal/conn and M1a may
// not widen it alone. Callers that need the negotiated set -- a capability
// asking whether the peer supports it, the CLI reporting what a link can do --
// type-assert for this.
//
// If this survives M1, it belongs folded into Session; see the report note.
type Negotiated interface {
	// ProtocolVersion is the effective protocol version: the lower of the two
	// advertised values.
	ProtocolVersion() uint32
	// Capabilities is the negotiated set: wire capID to effective capability
	// version. A capID absent from it was listed by only one peer and is
	// silently unusable, not an error.
	Capabilities() map[byte]uint32
}

var _ Negotiated = (*sess)(nil)

// ProtocolVersion is the negotiated effective version (§4): the lower of the
// two advertised values.
func (s *sess) ProtocolVersion() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// Capabilities is the negotiated set: capID to effective capability version.
func (s *sess) Capabilities() map[byte]uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[byte]uint32, len(s.caps))
	for k, v := range s.caps {
		out[k] = v
	}
	return out
}

func (s *sess) OpenStream(ctx context.Context) (Stream, error) {
	select {
	case <-s.done:
		return nil, s.closedErr()
	default:
	}
	// The caller owes this stream an opening envelope (§3); the session does
	// not write one, because only the capability knows its msgType.
	return s.tr.OpenStream(ctx)
}

func (s *sess) SendDatagram(b []byte) error {
	select {
	case <-s.done:
		return s.closedErr()
	default:
	}
	return s.tr.SendDatagram(b)
}

// PathInfo is §7.2's advisory hint: the transport's measured RTT, plus the
// path class from whoever knows it. The session does not know its own class --
// the layer that switched the path is the layer that can say.
func (s *sess) PathInfo() PathInfo {
	info := s.tr.PathInfo()
	if s.cfg.PathClass != nil {
		if class := s.cfg.PathClass(s.Peer().DeviceID); class != "" {
			info.Class = class
		}
	}
	return info
}

func (s *sess) Send(ctx context.Context, capID byte, msgType uint16, msg proto.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var payload []byte
	if msg != nil {
		var err error
		payload, err = proto.Marshal(msg)
		if err != nil {
			return fmt.Errorf("session: marshalling capID %d msgType %d: %w", capID, msgType, err)
		}
	}
	if len(payload) > MaxMessageSize {
		return protoErr(CodeMessageTooLarge, nil,
			"capID %d msgType %d marshals to %d bytes", capID, msgType, len(payload))
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return EncodeEnvelope(s.ctrl, Envelope{
		Version: EnvelopeVersion,
		CapID:   capID,
		MsgType: msgType,
		Payload: payload,
	})
}

// Quiesce is a deliberate no-op in M1. PROTOCOL.md §7.1 defines the
// QuiesceRequest / QuiesceRelease exchange and the arbitration rules; neither is
// implemented here, and guessing at them would be worse than not having them.
// The returned release func is safe to call and does nothing.
func (s *sess) Quiesce(ctx context.Context, floorBytesPerSec uint32, reason string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return func() {}, err
	}
	s.log.Debug("quiesce requested but not implemented in M1",
		"floorBytesPerSec", floorBytesPerSec, "reason", reason)
	return func() {}, nil
}

func (s *sess) Done() <-chan struct{} { return s.done }

func (s *sess) Close(code uint16, reason string) error {
	return s.closeWith(ErrorCode(code), reason, nil)
}

func (s *sess) closeWith(code ErrorCode, reason string, cause error) error {
	s.closeOnce.Do(func() {
		s.closeErr = cause
		if s.closeErr == nil && code != CodeNoError {
			s.closeErr = protoErr(code, nil, "%s", reason)
		}
		s.cancel()
		close(s.done)
		_ = s.ctrl.Close()
		_ = s.tr.CloseWithError(code, reason)
	})
	return nil
}

func (s *sess) closedErr() error {
	if s.closeErr != nil {
		return s.closeErr
	}
	return errors.New("session: closed")
}
