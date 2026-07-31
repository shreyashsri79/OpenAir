package mobile

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// sasRecorder answers the digit comparison and remembers what it was shown.
type sasRecorder struct {
	answer bool

	mu   sync.Mutex
	sas  string
	peer *PeerInfo
}

func (s *sasRecorder) ConfirmSAS(sas string, peer *PeerInfo) bool {
	s.mu.Lock()
	s.sas, s.peer = sas, peer
	s.mu.Unlock()
	return s.answer
}

func (s *sasRecorder) shown() (string, *PeerInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sas, s.peer
}

func newIdentity(t *testing.T) *Identity {
	t.Helper()
	id, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	return id
}

// runPairing drives both halves the way a shell would: one device shows an
// offer and awaits, the other is handed that exact string.
func runPairing(t *testing.T, showIden, scanIden *Identity, showAns, scanAns bool) (
	shown *PeerInfo, showErr error, scanned *PeerInfo, scanErr error,
	showSAS, scanSAS *sasRecorder) {
	t.Helper()

	showSAS = &sasRecorder{answer: showAns}
	scanSAS = &sasRecorder{answer: scanAns}

	shower := NewPairing(showIden, "shower")
	shower.SetSASVerifier(showSAS)
	scanner := NewPairing(scanIden, "scanner")
	scanner.SetSASVerifier(scanSAS)

	offer, err := shower.ShowOffer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ShowOffer: %v", err)
	}
	t.Cleanup(func() { _ = shower.Stop() })

	awaited := make(chan struct{})
	go func() {
		defer close(awaited)
		shown, showErr = shower.AwaitPeer()
	}()

	scanned, scanErr = scanner.PairWithOffer(offer)

	select {
	case <-awaited:
	case <-time.After(20 * time.Second):
		t.Fatal("the showing side never finished pairing")
	}
	return shown, showErr, scanned, scanErr, showSAS, scanSAS
}

// The whole binding-level milestone in one test: two devices that have never
// met pair over a real session, both users see the same digits, and each ends
// up holding the other's key.
func TestPairingThroughBinding(t *testing.T) {
	showIden, scanIden := newIdentity(t), newIdentity(t)

	shown, showErr, scanned, scanErr, showSAS, scanSAS := runPairing(t, showIden, scanIden, true, true)
	if showErr != nil {
		t.Fatalf("showing side: %v", showErr)
	}
	if scanErr != nil {
		t.Fatalf("scanning side: %v", scanErr)
	}

	showDigits, showPeer := showSAS.shown()
	scanDigits, scanPeer := scanSAS.shown()
	if showDigits != scanDigits {
		t.Fatalf("the two devices showed different digits: %q and %q", showDigits, scanDigits)
	}
	// Grouped for display, which is the form a shell should render unchanged.
	if !strings.Contains(showDigits, " ") || len(strings.ReplaceAll(showDigits, " ", "")) != 6 {
		t.Fatalf("digits %q are not six characters grouped for display", showDigits)
	}
	if showPeer == nil || scanPeer == nil {
		t.Fatal("a verifier was called without a peer to describe")
	}
	if showPeer.DeviceID() != scanIden.DeviceID() || scanPeer.DeviceID() != showIden.DeviceID() {
		t.Fatal("the verifiers were shown the wrong devices")
	}

	// Both sides pinned the other, and say so through the API a shell uses.
	if !showIden.IsPaired(scanIden.DeviceID()) {
		t.Fatal("the showing side did not pin the scanner")
	}
	if !scanIden.IsPaired(showIden.DeviceID()) {
		t.Fatal("the scanning side did not pin the shower")
	}
	if showIden.PairedCount() != 1 || scanIden.PairedCount() != 1 {
		t.Fatalf("paired counts are %d and %d, want 1 each",
			showIden.PairedCount(), scanIden.PairedCount())
	}

	if shown.DeviceID() != scanIden.DeviceID() || !shown.Trusted() {
		t.Fatalf("AwaitPeer returned %+v", shown)
	}
	if scanned.DeviceID() != showIden.DeviceID() || !scanned.Trusted() {
		t.Fatalf("PairWithOffer returned %+v", scanned)
	}
}

// A user saying the digits differ is a man-in-the-middle report. Nothing may be
// pinned on either side.
func TestPairingDeclinePinsNothing(t *testing.T) {
	showIden, scanIden := newIdentity(t), newIdentity(t)

	_, showErr, _, scanErr, _, _ := runPairing(t, showIden, scanIden, true, false)
	if scanErr == nil {
		t.Fatal("the declining side reported success")
	}
	if showErr == nil {
		t.Fatal("the accepting side reported success after its peer declined")
	}
	if showIden.PairedCount() != 0 || scanIden.PairedCount() != 0 {
		t.Fatal("a declined pairing pinned something")
	}
}

// §5.2 forbids a skip-verification path, so a shell that forgot the callback
// must fail loudly rather than pair silently.
func TestPairingRequiresASASVerifier(t *testing.T) {
	p := NewPairing(newIdentity(t), "no-verifier")

	if _, err := p.ShowOffer("127.0.0.1:0"); !errors.Is(err, ErrNoSASVerifier) {
		t.Fatalf("ShowOffer without a verifier returned %v", err)
	}
	if _, err := p.PairWithOffer("openair://pair/whatever"); !errors.Is(err, ErrNoSASVerifier) {
		t.Fatalf("PairWithOffer without a verifier returned %v", err)
	}
}

// The offer has to be renderable both ways: a QR string, and something a human
// can retype when the other device has no camera.
func TestOfferIsRenderableBothWays(t *testing.T) {
	p := NewPairing(newIdentity(t), "shower")
	p.SetSASVerifier(&sasRecorder{answer: true})

	offer, err := p.ShowOffer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ShowOffer: %v", err)
	}
	defer p.Stop()

	if !strings.HasPrefix(offer, "openair://pair/") {
		t.Fatalf("offer %q is not the scheme a QR reader should hand back", offer)
	}
	grouped := p.OfferGrouped()
	if !strings.Contains(grouped, "-") {
		t.Fatalf("the manual-entry form %q has no separators", grouped)
	}
	if p.ListenAddr() == "" || !p.IsShowingOffer() {
		t.Fatal("ShowOffer did not report itself as running")
	}

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if p.IsShowingOffer() || p.OfferGrouped() != "" {
		t.Fatal("Stop left the pairing screen looking live")
	}
}

func TestPairWithGarbageOffer(t *testing.T) {
	p := NewPairing(newIdentity(t), "scanner")
	p.SetSASVerifier(&sasRecorder{answer: true})

	for _, s := range []string{"", "not-an-offer", "openair://pair/!!!!"} {
		if _, err := p.PairWithOffer(s); err == nil {
			t.Errorf("PairWithOffer(%q) accepted garbage", s)
		}
	}
}

// Unpair is what a shell calls when a user removes a device. It is local by
// design -- §6.3 makes enforcement local -- so it must work with the peer gone.
func TestUnpairIsLocalAndIdempotent(t *testing.T) {
	showIden, scanIden := newIdentity(t), newIdentity(t)
	if _, err, _, err2, _, _ := runPairing(t, showIden, scanIden, true, true); err != nil || err2 != nil {
		t.Fatalf("pairing failed: %v / %v", err, err2)
	}

	if err := showIden.Unpair(scanIden.DeviceID()); err != nil {
		t.Fatalf("Unpair: %v", err)
	}
	if showIden.IsPaired(scanIden.DeviceID()) {
		t.Fatal("the peer is still paired after Unpair")
	}
	// Unpairing something already gone is not an error: a shell must be able to
	// call this without first checking.
	if err := showIden.Unpair(scanIden.DeviceID()); err != nil {
		t.Fatalf("second Unpair: %v", err)
	}
	if err := showIden.Unpair("never-paired-with-this"); err != nil {
		t.Fatalf("Unpair of an unknown device: %v", err)
	}
}
