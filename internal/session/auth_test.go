package session

import (
	"context"
	"crypto/ed25519"
	"sync"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	v1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// ownedIdentity is a stubIdentity that actually holds a privilege key, so the
// initiating half of §6 can be exercised end to end rather than stubbed.
//
// The knobs are the failure modes the spec names: a locked key, a proof issued
// outside the ±60s window, and a nonce that repeats.
type ownedIdentity struct {
	*stubIdentity

	privPub  ed25519.PublicKey
	privPriv ed25519.PrivateKey

	locked     bool
	skew       time.Duration
	fixedNonce []byte
}

func newOwnedIdentity(seed byte) *ownedIdentity {
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = seed ^ 0xa5
	}
	priv := ed25519.NewKeyFromSeed(s)
	return &ownedIdentity{
		stubIdentity: newStubIdentity(seed),
		privPub:      priv.Public().(ed25519.PublicKey),
		privPriv:     priv,
	}
}

func (i *ownedIdentity) PrivilegePublic() ed25519.PublicKey { return i.privPub }

func (i *ownedIdentity) SignOwned(target identity.DeviceID, capID byte, msgType uint16) ([]byte, int64, []byte, error) {
	if i.locked {
		return nil, 0, nil, identity.ErrLocked
	}
	nonce := i.fixedNonce
	if nonce == nil {
		nonce = make([]byte, identity.OwnedNonceLen)
		if _, err := randRead(nonce); err != nil {
			return nil, 0, nil, err
		}
	}
	issuedAt := time.Now().Add(i.skew).UnixMilli()
	input, err := identity.OwnedSigningInput(target, capID, msgType, nonce, issuedAt)
	if err != nil {
		return nil, 0, nil, err
	}
	return nonce, issuedAt, ed25519.Sign(i.privPriv, input), nil
}

// randRead is a seam so a test can hand out a repeating nonce without reaching
// into crypto/rand globally.
var randRead = func(b []byte) (int, error) {
	for i := range b {
		b[i] = byte(i * 7)
	}
	b[0] = byte(time.Now().UnixNano())
	b[1] = byte(time.Now().UnixNano() >> 8)
	b[2] = byte(time.Now().UnixNano() >> 16)
	return len(b), nil
}

// ownedHandler declares Owned, so every message it receives needs both the
// trust-store level and a valid proof.
type ownedHandler struct {
	*recordingHandler

	mu    sync.Mutex
	owned []bool
}

func newOwnedHandler(capID byte) *ownedHandler {
	return &ownedHandler{recordingHandler: newRecordingHandler(capID)}
}

func (h *ownedHandler) RequiredLevel() identity.TrustLevel { return identity.LevelOwned }

func (h *ownedHandler) Serve(ctx context.Context, sess Session, msgType uint16, payload []byte) error {
	h.mu.Lock()
	h.owned = append(h.owned, OwnedFromContext(ctx))
	h.mu.Unlock()
	return h.recordingHandler.Serve(ctx, sess, msgType, payload)
}

func (h *ownedHandler) sawOwned() []bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]bool(nil), h.owned...)
}

// events collects the accessed device's authorisation log (§6.3, PRD R4).
type events struct {
	mu   sync.Mutex
	list []AuthEvent
	got  chan AuthEvent
}

func newEvents() *events { return &events{got: make(chan AuthEvent, 16)} }

func (e *events) record(ev AuthEvent) {
	e.mu.Lock()
	e.list = append(e.list, ev)
	e.mu.Unlock()
	select {
	case e.got <- ev:
	default:
	}
}

func (e *events) next(t *testing.T) AuthEvent {
	t.Helper()
	select {
	case ev := <-e.got:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no authorisation event was logged")
		return AuthEvent{}
	}
}

// ownedPair brings up A (initiator, holding a privilege key) and B (accessed
// device, with a trust-store record for A at the given level).
func ownedPair(t *testing.T, level identity.TrustLevel, pinPrivilegeKey bool) (*sess, *ownedIdentity, *ownedHandler, *events) {
	t.Helper()

	idA := newOwnedIdentity(0x11)
	idB := newOwnedIdentity(0x22)

	h := newOwnedHandler(capClip)
	log := newEvents()

	cfgA := Config{Local: idA, DisplayName: "a", Platform: "linux", Handlers: map[byte]Handler{capClip: newOwnedHandler(capClip)}}
	cfgB := Config{
		Local:       idB,
		DisplayName: "b",
		Platform:    "linux",
		Handlers:    map[byte]Handler{capClip: h},
		OnAuthEvent: log.record,
		PeerLookup: func(id identity.DeviceID) (identity.Peer, bool) {
			if id != idA.DeviceID() {
				return identity.Peer{}, false
			}
			p := identity.Peer{
				DeviceID:          idA.DeviceID(),
				IdentityPublicKey: idA.IdentityPublic(),
				Level:             level,
			}
			if pinPrivilegeKey {
				p.PrivilegePublicKey = idA.privPub
			}
			return p, true
		},
	}

	a, _, _, _ := pair(t, cfgA, cfgB)
	return a, idA, h, log
}

func ctx3s(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestOwnedRequestWithValidProofIsServed is §6's happy path: proof first, then
// the operation it authorises, and the capability sees a context that says so.
func TestOwnedRequestWithValidProofIsServed(t *testing.T) {
	a, _, h, log := ownedPair(t, identity.LevelOwned, true)

	if err := a.SendOwned(ctx3s(t), capClip, 1, &v1.SessionEnd{SessionId: "s1"}); err != nil {
		t.Fatalf("SendOwned: %v", err)
	}

	select {
	case <-h.got:
	case <-time.After(3 * time.Second):
		t.Fatal("an Owned message with a valid proof never reached its capability")
	}
	if owned := h.sawOwned(); len(owned) != 1 || !owned[0] {
		t.Fatalf("OwnedFromContext reported %v, want [true]", owned)
	}
	if ev := log.next(t); !ev.Allowed || !ev.Owned {
		t.Fatalf("logged %+v, want an allowed Owned event", ev)
	}
}

// TestOwnedRequestWithoutProofIsRefused: the trust store saying "Owned" is not
// enough on its own. D-18's whole point is that the human was present.
func TestOwnedRequestWithoutProofIsRefused(t *testing.T) {
	a, _, h, log := ownedPair(t, identity.LevelOwned, true)

	if err := a.Send(ctx3s(t), capClip, 1, &v1.SessionEnd{SessionId: "s1"}); err != nil {
		t.Fatal(err)
	}

	ev := log.next(t)
	if ev.Allowed || ev.Code != CodeUnauthorised {
		t.Fatalf("logged %+v, want a refusal with UNAUTHORISED", ev)
	}
	if h.count() != 0 {
		t.Fatal("an Owned message with no AuthProof reached its capability")
	}
}

// TestReplayedProofIsRefused is §6 rule 4. The nonce is the only thing standing
// between a captured proof and a second use of it.
func TestReplayedProofIsRefused(t *testing.T) {
	a, idA, h, log := ownedPair(t, identity.LevelOwned, true)
	idA.fixedNonce = make([]byte, identity.OwnedNonceLen)

	ctx := ctx3s(t)
	if err := a.SendOwned(ctx, capClip, 1, &v1.SessionEnd{SessionId: "first"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.got:
	case <-time.After(3 * time.Second):
		t.Fatal("the first Owned message never arrived")
	}
	if ev := log.next(t); !ev.Allowed {
		t.Fatalf("the first message was refused: %+v", ev)
	}

	// Same nonce, same operation, freshly signed: a captured proof replayed.
	if err := a.SendOwned(ctx, capClip, 1, &v1.SessionEnd{SessionId: "second"}); err != nil {
		t.Fatal(err)
	}
	ev := log.next(t)
	if ev.Allowed || ev.Code != CodeUnauthorised {
		t.Fatalf("logged %+v, want the replay refused with UNAUTHORISED", ev)
	}
	if h.count() != 1 {
		t.Fatalf("%d messages reached the capability, want 1 -- the replay was served", h.count())
	}
}

// TestProofForAnotherPeerIsRefused is §6 rule 2, which is the reason
// target_device_id is in the signed input at all: a proof captured by one peer
// must be useless against another.
func TestProofForAnotherPeerIsRefused(t *testing.T) {
	a, idA, h, log := ownedPair(t, identity.LevelOwned, true)

	// A proof correctly made for a third device, replayed at this one.
	elsewhere := newStubIdentity(0x33).DeviceID()
	nonce, issuedAt, sig, err := idA.SignOwned(elsewhere, capClip, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctx3s(t)
	if err := a.Send(ctx, 0, MsgAuthProof, &v1.AuthProof{Nonce: nonce, IssuedAt: issuedAt, Signature: sig}); err != nil {
		t.Fatal(err)
	}
	if err := a.Send(ctx, capClip, 1, &v1.SessionEnd{SessionId: "s"}); err != nil {
		t.Fatal(err)
	}

	ev := log.next(t)
	if ev.Allowed {
		t.Fatalf("a proof made for %s authorised an operation here: %+v", elsewhere, ev)
	}
	if h.count() != 0 {
		t.Fatal("a proof targeting another device reached the capability")
	}
}

// TestProofIsBoundToItsOperation covers the capID/msgType half of the binding:
// a proof for one operation cannot be spent on another.
func TestProofIsBoundToItsOperation(t *testing.T) {
	a, idA, h, log := ownedPair(t, identity.LevelOwned, true)

	nonce, issuedAt, sig, err := idA.SignOwned(a.Peer().DeviceID, capClip, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctx3s(t)
	if err := a.Send(ctx, 0, MsgAuthProof, &v1.AuthProof{Nonce: nonce, IssuedAt: issuedAt, Signature: sig}); err != nil {
		t.Fatal(err)
	}
	// Same capability, different message type.
	if err := a.Send(ctx, capClip, 2, &v1.SessionEnd{SessionId: "s"}); err != nil {
		t.Fatal(err)
	}

	ev := log.next(t)
	if ev.Allowed {
		t.Fatalf("a proof for msgType 1 authorised msgType 2: %+v", ev)
	}
	if h.count() != 0 {
		t.Fatal("a proof for another operation reached the capability")
	}
}

// TestStaleProofIsAuthExpired is §6 rule 3. AUTH_EXPIRED rather than
// UNAUTHORISED, because the initiator's correct response is to re-authenticate.
func TestStaleProofIsAuthExpired(t *testing.T) {
	a, idA, h, log := ownedPair(t, identity.LevelOwned, true)
	idA.skew = -10 * time.Minute

	if err := a.SendOwned(ctx3s(t), capClip, 1, &v1.SessionEnd{SessionId: "s"}); err != nil {
		t.Fatal(err)
	}

	ev := log.next(t)
	if ev.Allowed || ev.Code != CodeAuthExpired {
		t.Fatalf("logged %+v, want a refusal with AUTH_EXPIRED", ev)
	}
	if h.count() != 0 {
		t.Fatal("a proof issued ten minutes ago reached the capability")
	}
}

// TestTrustedPeerCannotReachOwnedCapability is the trust ladder, independent of
// the proof: a peer the local user never promoted stays out even holding a
// perfectly valid privilege key.
func TestTrustedPeerCannotReachOwnedCapability(t *testing.T) {
	a, _, h, log := ownedPair(t, identity.LevelTrusted, true)

	if err := a.SendOwned(ctx3s(t), capClip, 1, &v1.SessionEnd{SessionId: "s"}); err != nil {
		t.Fatal(err)
	}

	ev := log.next(t)
	if ev.Allowed || ev.Code != CodeUnauthorised {
		t.Fatalf("logged %+v, want UNAUTHORISED for a Trusted peer", ev)
	}
	if h.count() != 0 {
		t.Fatal("a Trusted peer reached an Owned capability")
	}
}

// TestPeerWithNoPinnedPrivilegeKeyCannotReachOwned is D-21 tier 3 seen from the
// other end: a device that protects no privilege key has none pinned here, so
// nothing it sends can verify.
func TestPeerWithNoPinnedPrivilegeKeyCannotReachOwned(t *testing.T) {
	a, _, h, log := ownedPair(t, identity.LevelOwned, false)

	if err := a.SendOwned(ctx3s(t), capClip, 1, &v1.SessionEnd{SessionId: "s"}); err != nil {
		t.Fatal(err)
	}

	ev := log.next(t)
	if ev.Allowed || ev.Code != CodeUnauthorised {
		t.Fatalf("logged %+v, want UNAUTHORISED with no pinned privilege key", ev)
	}
	if h.count() != 0 {
		t.Fatal("a peer with no pinned privilege key reached an Owned capability")
	}
}

// TestProofMarksATrustedCapabilityWithoutGating is the per-message case: files
// stays Trusted so that ordinary transfers work, and an Owned peer's offer is
// distinguishable so it can be accepted with nobody watching (PRD R3, R11).
func TestProofMarksATrustedCapabilityWithoutGating(t *testing.T) {
	idA := newOwnedIdentity(0x44)
	idB := newOwnedIdentity(0x55)
	h := newOwnedHandler(capFiles)

	// Trusted, not Owned: the capability accepts anyone paired.
	trusted := &trustedRecorder{ownedHandler: h}

	cfgA := Config{Local: idA, DisplayName: "a", Platform: "linux",
		Handlers: map[byte]Handler{capFiles: newRecordingHandler(capFiles)}}
	cfgB := Config{Local: idB, DisplayName: "b", Platform: "linux",
		Handlers: map[byte]Handler{capFiles: trusted},
		PeerLookup: func(id identity.DeviceID) (identity.Peer, bool) {
			return identity.Peer{
				DeviceID:           idA.DeviceID(),
				IdentityPublicKey:  idA.IdentityPublic(),
				PrivilegePublicKey: idA.privPub,
				Level:              identity.LevelOwned,
			}, id == idA.DeviceID()
		},
	}
	a, _, _, _ := pair(t, cfgA, cfgB)

	ctx := ctx3s(t)
	if err := a.SendOwned(ctx, capFiles, 1, &v1.SessionEnd{SessionId: "owned"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Send(ctx, capFiles, 1, &v1.SessionEnd{SessionId: "plain"}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "both messages served", func() bool { return h.count() == 2 })
	owned := h.sawOwned()
	if len(owned) != 2 || !owned[0] || owned[1] {
		t.Fatalf("OwnedFromContext reported %v, want [true false]", owned)
	}
}

// trustedRecorder is the ownedHandler at Trusted level: same recording, no gate.
type trustedRecorder struct{ *ownedHandler }

func (h *trustedRecorder) RequiredLevel() identity.TrustLevel { return identity.LevelTrusted }

// TestLockedInitiatorCannotSend is the initiating half of expiry: with no live
// unlock session there is no signature to make, so the request never leaves.
func TestLockedInitiatorCannotSend(t *testing.T) {
	a, idA, h, _ := ownedPair(t, identity.LevelOwned, true)
	idA.locked = true

	err := a.SendOwned(ctx3s(t), capClip, 1, &v1.SessionEnd{SessionId: "s"})
	if err == nil {
		t.Fatal("SendOwned succeeded with the privilege key locked")
	}
	if h.count() != 0 {
		t.Fatal("something was sent despite the failure")
	}
}
