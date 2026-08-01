package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/input"
	"github.com/shreyashsri79/openair/internal/identity"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// M14 through the daemon, which is where the parts the capability tests cannot
// reach live: §6's gate on the announcement, §6.3's indicator, and the fact
// that a device only accepts input at all if it was told to.

// spyInjector records what was injected into the machine being driven.
type spyInjector struct {
	mu      sync.Mutex
	keys    []uint16
	ups     []uint16
	moves   int
	clicks  int
	scrolls int
}

func (s *spyInjector) Key(usage uint16, down bool, _ byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if down {
		s.keys = append(s.keys, usage)
	} else {
		s.ups = append(s.ups, usage)
	}
	return nil
}

func (s *spyInjector) PointerMove(int32, int32, bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.moves++
	return nil
}

func (s *spyInjector) PointerButton(_ byte, down bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if down {
		s.clicks++
	}
	return nil
}

func (s *spyInjector) Scroll(int32, int32, bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scrolls++
	return nil
}

func (s *spyInjector) Touch(uint8, byte, int32, int32) error { return nil }
func (s *spyInjector) Close() error                          { return nil }

func (s *spyInjector) pressed() []uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint16(nil), s.keys...)
}

func (s *spyInjector) released() []uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint16(nil), s.ups...)
}

func (s *spyInjector) counts() (moves, clicks, scrolls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.moves, s.clicks, s.scrolls
}

// controllablePair returns a target accepting input and a controller unlocked
// for it.
func controllablePair(t *testing.T) (target, controller *Daemon, cc *Client, spy *spyInjector) {
	t.Helper()
	spy = &spyInjector{}
	target = newTestDaemon(t, func(cfg *Config) {
		protect(t, cfg.KeyDir)
		cfg.AcceptInput = true
		cfg.InputInjector = spy
	})
	controller = newProtectedDaemon(t)
	pinOwned(t, controller, target)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cc = connect(t, controller, nil, nil)
	if _, err := cc.Unlock(ctx, string(target.DeviceID()), []byte(testPassphrase), nil, false, 5*time.Minute); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	return target, controller, cc, spy
}

// TestTypingOnAnotherMachine is the milestone: text typed here appears as key
// events there, with nobody sitting at it.
func TestTypingOnAnotherMachine(t *testing.T) {
	target, _, cc, spy := controllablePair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sent, err := cc.Input(ctx, target.Addr(), []*openairv1.InputAction{
		{Text: "hi"},
		{Key: "enter"},
		{Move: &openairv1.InputMove{X: 40, Y: 10}},
		{Click: "left"},
		{Scroll: &openairv1.InputScroll{Dy: -2}},
	})
	if err != nil {
		t.Fatalf("input: %v", err)
	}
	if sent == 0 {
		t.Fatal("nothing was sent")
	}

	waitFor(t, "the key events to arrive", func() bool {
		return len(spy.pressed()) >= 3 // h, i, enter
	})
	waitFor(t, "the pointer events to arrive", func() bool {
		moves, clicks, scrolls := spy.counts()
		return moves == 1 && clicks == 1 && scrolls == 1
	})

	// Every press was released: a session that leaves keys down on another
	// machine is the failure mode §13's safety release exists for, and the
	// ordinary path must not need it.
	waitFor(t, "every key to be released", func() bool {
		return len(spy.released()) == len(spy.pressed())
	})
}

// TestADeviceThatDidNotAgreeIsNotControllable. --accept-input is off by
// default, and a device that never opted in refuses rather than quietly
// accepting.
func TestADeviceThatDidNotAgreeIsNotControllable(t *testing.T) {
	spy := &spyInjector{}
	target := newTestDaemon(t, func(cfg *Config) {
		protect(t, cfg.KeyDir)
		cfg.InputInjector = spy // but AcceptInput stays false
	})
	controller := newProtectedDaemon(t)
	pinOwned(t, controller, target)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cc := connect(t, controller, nil, nil)
	if _, err := cc.Unlock(ctx, string(target.DeviceID()), []byte(testPassphrase), nil, false, time.Minute); err != nil {
		t.Fatalf("unlock: %v", err)
	}

	// The announcement itself succeeds -- §6.3 is not capability-specific --
	// and the events go nowhere, which is the enforcement.
	if _, err := cc.Input(ctx, target.Addr(), []*openairv1.InputAction{{Text: "hello"}}); err != nil {
		t.Logf("input refused outright: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := len(spy.pressed()); got != 0 {
		t.Fatalf("%d key events reached a device that never agreed to be controlled", got)
	}
}

// TestControllingNeedsAnUnlock. Input is Owned, and the proof travels on the
// announcement (D-82) -- so a locked controller cannot even start.
func TestControllingNeedsAnUnlock(t *testing.T) {
	spy := &spyInjector{}
	target := newTestDaemon(t, func(cfg *Config) {
		protect(t, cfg.KeyDir)
		cfg.AcceptInput = true
		cfg.InputInjector = spy
	})
	controller := newProtectedDaemon(t)
	pinOwned(t, controller, target)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cc := connect(t, controller, nil, nil)
	_, err := cc.Input(ctx, target.Addr(), []*openairv1.InputAction{{Text: "hello"}})
	if err == nil {
		t.Fatal("a locked device drove another one")
	}
	if !strings.Contains(err.Error(), "unlock") {
		t.Fatalf("the refusal does not say what to do: %v", err)
	}
	if got := len(spy.pressed()); got != 0 {
		t.Fatalf("%d key events arrived without an unlock", got)
	}
}

// TestTheIndicatorIsRaisedAndLowered is §6.3's requirement, and the only sign
// on the controlled machine that anything is happening.
func TestTheIndicatorIsRaisedAndLowered(t *testing.T) {
	target, _, cc, _ := controllablePair(t)
	events := watching(t, target)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := cc.Input(ctx, target.Addr(), []*openairv1.InputAction{{Key: "a"}}); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the indicator to go up", func() bool {
		return len(events.of(openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_ANNOUNCED)) == 1
	})
	shown := events.of(openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_ANNOUNCED)[0].GetText()
	if !strings.Contains(shown, "keyboard and mouse") {
		t.Fatalf("the indicator says %q, which does not name what is being used", shown)
	}
	if got := target.AnnouncedSessions(); len(got) != 1 {
		t.Fatalf("the daemon reports %d live sessions, want 1", len(got))
	}

	if err := cc.StopInput(ctx, target.Addr()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitFor(t, "the indicator to go down", func() bool {
		return len(events.of(openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_ENDED)) == 1
	})
	if got := target.AnnouncedSessions(); len(got) != 0 {
		t.Fatalf("%d sessions still shown after the controller finished", len(got))
	}
}

// TestKillingASessionStopsTheEventsAndReleasesTheKeys. §6.3 says a local user
// can end a session instantly and that the enforcement is local, not the
// message -- so this asserts both halves.
func TestKillingASessionStopsTheEventsAndReleasesTheKeys(t *testing.T) {
	target, _, cc, spy := controllablePair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := cc.Input(ctx, target.Addr(), []*openairv1.InputAction{{Key: "a"}}); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the first events", func() bool { return len(spy.pressed()) > 0 })

	// The person in front of the controlled machine stops it.
	if killed := target.KillSessions(ctx, ""); killed == 0 {
		t.Fatal("nothing was killed")
	}
	before := len(spy.pressed())

	// Further events are refused, whatever the controller does.
	_, _ = cc.Input(ctx, target.Addr(), []*openairv1.InputAction{{Text: "more"}})
	time.Sleep(300 * time.Millisecond)
	if after := len(spy.pressed()); after != before {
		t.Fatalf("%d more events were applied after the session was killed", after-before)
	}
	if got := target.AnnouncedSessions(); len(got) != 0 {
		t.Fatalf("%d sessions survived a kill", len(got))
	}
}

// TestRevokingOwnedStopsInputImmediately. The level is re-read per datagram
// (D-74, D-82), so demoting a peer stops it at the next event rather than at
// the next announcement.
func TestRevokingOwnedStopsInputImmediately(t *testing.T) {
	target, _, cc, spy := controllablePair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := cc.Input(ctx, target.Addr(), []*openairv1.InputAction{{Key: "a"}}); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the first event", func() bool { return len(spy.pressed()) > 0 })
	before := len(spy.pressed())

	// The controlled device demotes the controller. Nothing is reconnected.
	tc := connect(t, target, nil, nil)
	if _, err := tc.Trust(ctx, string(controllerID(t, target)),
		openairv1.TrustLevel_TRUST_LEVEL_TRUSTED, ""); err != nil {
		t.Fatalf("demote: %v", err)
	}

	_, _ = cc.Input(ctx, target.Addr(), []*openairv1.InputAction{{Text: "still here"}})
	time.Sleep(300 * time.Millisecond)
	if after := len(spy.pressed()); after != before {
		t.Fatalf("%d events were applied after the peer was demoted from Owned", after-before)
	}
}

// controllerID is the DeviceID the target knows the controller by.
func controllerID(t *testing.T, target *Daemon) identity.DeviceID {
	t.Helper()
	for _, sess := range target.liveSessions() {
		return sess.Peer().DeviceID
	}
	t.Fatal("the target has no session to name the controller from")
	return ""
}

// TestInputIsNegotiatedEvenByADeviceThatWillNotApplyIt.
//
// §4's capability negotiation is symmetric — a capID is usable only if both
// ends offered it — so a controller that did not advertise input would find its
// events discarded by the device it is driving, which registered the capability
// perfectly well. Advertising says "this build speaks §13"; whether anything is
// applied is inputAllowed's business.
func TestInputIsNegotiatedEvenByADeviceThatWillNotApplyIt(t *testing.T) {
	quiet := newTestDaemon(t, nil)
	if _, ok := quiet.handlers()[input.CapID]; !ok {
		t.Fatal("a daemon with remote input off does not negotiate input at all, so it cannot drive anything either")
	}
	if quiet.inputAllowed("aaaaaaaaaaaaaaaa") {
		t.Fatal("a daemon with remote input off would apply an event")
	}
}
