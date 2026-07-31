package pairing

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

// side is one of the two devices in an end-to-end pairing test.
type side struct {
	id      *identity.FileIdentity
	store   *identity.FileTrustStore
	handler *Handler

	// sas receives the digits this side showed its user.
	sas chan string
}

// newSide builds a device whose Confirm reports the SAS it was shown and
// answers with accept.
func newSide(t *testing.T, name string, accept bool) *side {
	t.Helper()
	dir := t.TempDir()

	id, err := identity.LoadOrCreate(identity.Options{Dir: dir, Tier: identity.TierNone})
	if err != nil {
		t.Fatalf("%s: LoadOrCreate: %v", name, err)
	}
	store, err := identity.OpenTrustStore(filepath.Join(dir, "trust.json"))
	if err != nil {
		t.Fatalf("%s: OpenTrustStore: %v", name, err)
	}

	s := &side{id: id, store: store, sas: make(chan string, 1)}
	h, err := NewHandler(Config{
		Local:       id,
		Store:       store,
		DisplayName: name,
		Platform:    "linux",
		Confirm: func(_ context.Context, sas string, _ PeerInfo) (bool, error) {
			select {
			case s.sas <- sas:
			default:
			}
			return accept, nil
		},
		Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("%s: NewHandler: %v", name, err)
	}
	s.handler = h
	return s
}

func (s *side) handlers() map[byte]session.Handler {
	return map[byte]session.Handler{0: s.handler}
}

type pairResult struct {
	peer identity.Peer
	err  error
}

// runPairing stands up a real QUIC listener and dialler and runs §5.2 across
// them: offerer displays the offer and awaits, scanner decodes it and
// initiates. It returns each side's result plus the two live sessions.
func runPairing(t *testing.T, offerer, scanner *side) (offRes, scanRes pairResult, offSess, scanSess session.Session) {
	t.Helper()

	ln, err := conn.Listen("127.0.0.1:0", offerer.id, "offerer", "linux",
		offerer.handlers(), conn.ListenOptions{Authorize: offerer.handler.Authorize})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	// §5.2 has to run over a session nothing has authenticated yet, so the
	// offerer opens a pairing window for exactly as long as it is waiting.
	closeWindow := offerer.handler.OpenWindow()
	t.Cleanup(closeWindow)

	offer, err := NewOffer(offerer.id, []string{ln.Addr()})
	if err != nil {
		t.Fatalf("NewOffer: %v", err)
	}
	encoded, err := EncodeOffer(offer)
	if err != nil {
		t.Fatalf("EncodeOffer: %v", err)
	}

	type accepted struct {
		res  pairResult
		sess session.Session
	}
	offerDone := make(chan accepted, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		sess, err := ln.Accept(ctx)
		if err != nil {
			offerDone <- accepted{res: pairResult{err: err}}
			return
		}
		peer, err := offerer.handler.Await(ctx, sess)
		offerDone <- accepted{res: pairResult{peer: peer, err: err}, sess: sess}
	}()

	// The scanner only ever sees the encoded string, exactly as a camera or a
	// user retyping it would hand it over.
	scanned, err := DecodeOffer(encoded)
	if err != nil {
		t.Fatalf("DecodeOffer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// No pinned key: during pairing TLS is unauthenticated, and the offer's
	// fingerprint is what VerifyOffer checks the presented key against (§5.1).
	dialSess, err := conn.NewDialer(scanner.id, "scanner", "linux", scanner.handlers()).
		DialAddr(ctx, ln.Addr(), identity.Peer{})
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	t.Cleanup(func() { dialSess.Close(0, "test done") })

	peer, err := scanner.handler.Initiate(ctx, dialSess, scanned)
	scanRes = pairResult{peer: peer, err: err}

	select {
	case got := <-offerDone:
		offRes, offSess = got.res, got.sess
	case <-time.After(20 * time.Second):
		t.Fatal("the offering side never finished pairing")
	}
	if offSess != nil {
		t.Cleanup(func() { offSess.Close(0, "test done") })
	}
	return offRes, scanRes, offSess, dialSess
}

// The whole milestone in one test: two devices that have never met pair over a
// real QUIC session, both users are shown the same six digits, and each ends up
// with the other pinned at Trusted.
func TestPairing_EndToEnd(t *testing.T) {
	offerer := newSide(t, "offerer", true)
	scanner := newSide(t, "scanner", true)

	offRes, scanRes, _, _ := runPairing(t, offerer, scanner)
	if offRes.err != nil {
		t.Fatalf("offering side: %v", offRes.err)
	}
	if scanRes.err != nil {
		t.Fatalf("scanning side: %v", scanRes.err)
	}

	offerShown := <-offerer.sas
	scannerShown := <-scanner.sas
	if offerShown != scannerShown {
		t.Fatalf("the two devices showed different digits: offerer %q, scanner %q", offerShown, scannerShown)
	}
	if len(offerShown) != SASDigits {
		t.Fatalf("SAS shown was %q, want %d digits", offerShown, SASDigits)
	}

	// Each side pinned the other, at Trusted and never above it: promotion to
	// Owned is a separate deliberate act (PRD R3).
	if got, ok := offerer.store.Get(scanner.id.DeviceID()); !ok {
		t.Fatal("the offerer did not pin the scanner")
	} else if got.Level != identity.LevelTrusted {
		t.Fatalf("the offerer pinned the scanner at level %d, want Trusted", got.Level)
	} else if string(got.IdentityPublicKey) != string(scanner.id.IdentityPublic()) {
		t.Fatal("the offerer pinned a key that is not the scanner's")
	}

	if got, ok := scanner.store.Get(offerer.id.DeviceID()); !ok {
		t.Fatal("the scanner did not pin the offerer")
	} else if got.Level != identity.LevelTrusted {
		t.Fatalf("the scanner pinned the offerer at level %d, want Trusted", got.Level)
	} else if string(got.IdentityPublicKey) != string(offerer.id.IdentityPublic()) {
		t.Fatal("the scanner pinned a key that is not the offerer's")
	}

	// And the returned records name the right devices.
	if offRes.peer.DeviceID != scanner.id.DeviceID() {
		t.Fatalf("offerer paired with %s, want %s", offRes.peer.DeviceID, scanner.id.DeviceID())
	}
	if scanRes.peer.DeviceID != offerer.id.DeviceID() {
		t.Fatalf("scanner paired with %s, want %s", scanRes.peer.DeviceID, offerer.id.DeviceID())
	}
}

// §5.2: pairing completes only when both peers accept. One user saying no has
// to leave both trust stores untouched -- a one-sided pairing is exactly the
// state an attacker who got a user to press the wrong button would want.
func TestPairing_DeclineLeavesNothingPinned(t *testing.T) {
	offerer := newSide(t, "offerer", true)
	scanner := newSide(t, "scanner", false) // this user says the digits do not match

	offRes, scanRes, _, _ := runPairing(t, offerer, scanner)

	if scanRes.err == nil {
		t.Fatal("the declining side reported success")
	}
	if !errors.Is(scanRes.err, ErrDeclined) {
		t.Fatalf("declining side: want ErrDeclined, got %v", scanRes.err)
	}
	if offRes.err == nil {
		t.Fatal("the accepting side reported success after the peer declined")
	}
	if !errors.Is(offRes.err, ErrPeerDeclined) {
		t.Fatalf("accepting side: want ErrPeerDeclined, got %v", offRes.err)
	}

	// Declining is a decision, not a fault: nothing should dial again on it.
	if Retryable(scanRes.err) || Retryable(offRes.err) {
		t.Fatal("a declined pairing was reported retryable")
	}

	if _, ok := offerer.store.Get(scanner.id.DeviceID()); ok {
		t.Fatal("the offerer pinned a peer that declined")
	}
	if _, ok := scanner.store.Get(offerer.id.DeviceID()); ok {
		t.Fatal("the declining side pinned the peer anyway")
	}
}

// §6.1: a revoke takes effect mid-session. Both ends have to stop honouring the
// old level without waiting for the connection to end, and an unpair discards
// the pinned keys on both sides.
func TestRevoke_TakesEffectMidSession(t *testing.T) {
	offerer := newSide(t, "offerer", true)
	scanner := newSide(t, "scanner", true)

	offRes, scanRes, offSess, scanSess := runPairing(t, offerer, scanner)
	if offRes.err != nil || scanRes.err != nil {
		t.Fatalf("pairing failed: offerer %v, scanner %v", offRes.err, scanRes.err)
	}

	// Before the revoke, the live session authorises Trusted work on both ends.
	offGuard := offerer.handler.GuardFor(offSess)
	scanGuard := scanner.handler.GuardFor(scanSess)
	if err := offGuard.Authorize(identity.LevelTrusted); err != nil {
		t.Fatalf("a freshly paired peer was not authorised: %v", err)
	}
	if err := scanGuard.Authorize(identity.LevelTrusted); err != nil {
		t.Fatalf("a freshly paired peer was not authorised on the scanner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := offerer.handler.Revoke(ctx, offSess, identity.LevelUnpaired, "revoked by test"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// Local half first: enforcement never depends on the message arriving.
	if err := offGuard.Authorize(identity.LevelTrusted); !errors.Is(err, ErrRevoked) {
		t.Fatalf("the revoking side still authorises Trusted work: %v", err)
	}
	if _, ok := offerer.store.Get(scanner.id.DeviceID()); ok {
		t.Fatal("an unpair left the peer in the revoking side's trust store")
	}

	// The far side applies it on receipt: both pinned keys discarded, and the
	// session stops authorising anything.
	waitFor(t, 10*time.Second, "the revoked peer to drop its pin", func() bool {
		_, ok := scanner.store.Get(offerer.id.DeviceID())
		return !ok
	})
	if err := scanGuard.Authorize(identity.LevelTrusted); !errors.Is(err, ErrRevoked) {
		t.Fatalf("the revoked side still authorises Trusted work: %v", err)
	}

	// And it stays revoked across a restart -- an unpair that came back after a
	// reboot would be worse than never having applied it.
	reopened, err := identity.OpenTrustStore(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("OpenTrustStore: %v", err)
	}
	if _, ok := reopened.Get(offerer.id.DeviceID()); ok {
		t.Fatal("a fresh store somehow holds the revoked peer")
	}
}

// After pairing, the gate admits the peer on a later connection: pairing once
// is the whole point, and this is the path that proves the stored record and
// the presented key line up.
func TestAuthorize_AdmitsAPeerPairedOverTheWire(t *testing.T) {
	offerer := newSide(t, "offerer", true)
	scanner := newSide(t, "scanner", true)

	offRes, scanRes, _, _ := runPairing(t, offerer, scanner)
	if offRes.err != nil || scanRes.err != nil {
		t.Fatalf("pairing failed: offerer %v, scanner %v", offRes.err, scanRes.err)
	}

	// The window the pairing ran under is closed by t.Cleanup only at the end
	// of the test, so shut it here: this must pass on the trust store alone.
	offerer.handler.mu.Lock()
	offerer.handler.window = 0
	offerer.handler.mu.Unlock()

	presented := identity.Peer{
		DeviceID:          scanner.id.DeviceID(),
		IdentityPublicKey: scanner.id.IdentityPublic(),
	}
	if err := offerer.handler.Authorize(presented); err != nil {
		t.Fatalf("a peer that just paired was refused on a later connection: %v", err)
	}
}

func waitFor(t *testing.T, limit time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
