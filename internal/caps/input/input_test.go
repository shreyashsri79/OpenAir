package input

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/caps"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

// M14's two rules are the whole of these tests: a stale pointer sample is
// dropped, and a key event never is. Everything else follows from those.

var _ caps.Capability = (*Capability)(nil)

// recorder is an Injector that remembers what it was told to do.
type recorder struct {
	mu      sync.Mutex
	keys    []keyCall
	moves   []moveCall
	buttons []buttonCall
	scrolls []scrollCall
	touches []touchCall
	fail    error
}

type keyCall struct {
	usage uint16
	down  bool
	mods  byte
}
type moveCall struct {
	x, y     int32
	absolute bool
}
type buttonCall struct {
	button byte
	down   bool
}
type scrollCall struct {
	dx, dy  int32
	precise bool
}
type touchCall struct {
	id    uint8
	phase byte
	x, y  int32
}

func (r *recorder) Key(usage uint16, down bool, mods byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, keyCall{usage, down, mods})
	return r.fail
}

func (r *recorder) PointerMove(x, y int32, absolute bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.moves = append(r.moves, moveCall{x, y, absolute})
	return r.fail
}

func (r *recorder) PointerButton(button byte, down bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buttons = append(r.buttons, buttonCall{button, down})
	return r.fail
}

func (r *recorder) Scroll(dx, dy int32, precise bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scrolls = append(r.scrolls, scrollCall{dx, dy, precise})
	return r.fail
}

func (r *recorder) Touch(id uint8, phase byte, x, y int32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touches = append(r.touches, touchCall{id, phase, x, y})
	return r.fail
}

func (r *recorder) Close() error { return nil }

func (r *recorder) keysSeen() []keyCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]keyCall(nil), r.keys...)
}

func (r *recorder) movesSeen() []moveCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]moveCall(nil), r.moves...)
}

func (r *recorder) buttonsSeen() []buttonCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]buttonCall(nil), r.buttons...)
}

// fakeSession is enough of a session for the receiving half: a peer, and a
// datagram sink.
type fakeSession struct {
	peer identity.Peer

	mu   sync.Mutex
	sent [][]byte
}

func (s *fakeSession) Peer() identity.Peer        { return s.peer }
func (s *fakeSession) PathInfo() session.PathInfo { return session.PathInfo{Class: "lan"} }
func (s *fakeSession) Close(uint16, string) error { return nil }
func (s *fakeSession) Done() <-chan struct{}      { return nil }
func (s *fakeSession) Quiesce(context.Context, uint32, string) (func(), error) {
	return func() {}, nil
}
func (s *fakeSession) Send(context.Context, byte, uint16, proto.Message) error { return nil }
func (s *fakeSession) OpenStream(context.Context) (session.Stream, error) {
	return nil, errors.New("fake: input opens no streams")
}

func (s *fakeSession) SendDatagram(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, append([]byte(nil), b...))
	return nil
}

func (s *fakeSession) datagrams() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.sent...)
}

func newReceiver(t *testing.T) (*Capability, *recorder, *fakeSession) {
	t.Helper()
	rec := &recorder{}
	c := New(Config{Injector: rec, Logf: t.Logf})
	t.Cleanup(func() { c.Close() })
	return c, rec, &fakeSession{peer: identity.Peer{DeviceID: "aaaaaaaaaaaaaaaa"}}
}

func deliver(t *testing.T, c *Capability, sess *fakeSession, e Event) {
	t.Helper()
	b, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ServeDatagram(context.Background(), sess, b); err != nil {
		t.Fatalf("serving %+v: %v", e, err)
	}
}

// TestEveryEventKindSurvivesTheWire. The layout is hand-rolled bytes with no
// schema to check it, so this is the only thing standing between a field
// ordering mistake and a pointer that moves sideways.
func TestEveryEventKindSurvivesTheWire(t *testing.T) {
	for _, want := range []Event{
		{Kind: KindKey, Seq: 1, Usage: 0x04, Down: true, Modifiers: ModLeftCtrl | ModLeftShift},
		{Kind: KindPointerMove, Seq: 2, X: -400, Y: 900, Absolute: true},
		{Kind: KindPointerButton, Seq: 3, Button: ButtonRight, Down: false},
		{Kind: KindScroll, Seq: 4, DX: -3, DY: 120, Precise: true},
		{Kind: KindTouch, Seq: 5, TouchID: 2, Phase: TouchMove, X: 10, Y: -20},
	} {
		b, err := Encode(want)
		if err != nil {
			t.Fatalf("encoding %+v: %v", want, err)
		}
		if b[0] != CapID {
			t.Fatalf("a datagram for capID %d, want %d", b[0], CapID)
		}
		got, err := Decode(b)
		if err != nil {
			t.Fatalf("decoding %+v: %v", want, err)
		}
		if got != want {
			t.Fatalf("round trip changed the event:\n got %+v\nwant %+v", got, want)
		}
	}
}

// TestATruncatedEventIsRefused, because these arrive from the network and the
// decoder indexes into them.
func TestATruncatedEventIsRefused(t *testing.T) {
	full, err := Encode(Event{Kind: KindPointerMove, Seq: 1, X: 5, Y: 5})
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(full); n++ {
		if _, err := Decode(full[:n]); err == nil {
			t.Fatalf("a %d-byte event was accepted; the whole one is %d bytes", n, len(full))
		}
	}
}

// TestAStalePointerMoveIsDropped is §13's latest-wins rule. Reordering is
// normal on a datagram path, and applying an older sample moves the pointer
// backwards -- visible, and worse than the packet never arriving.
func TestAStalePointerMoveIsDropped(t *testing.T) {
	c, rec, sess := newReceiver(t)

	deliver(t, c, sess, Event{Kind: KindPointerMove, Seq: 10, X: 100, Y: 100})
	deliver(t, c, sess, Event{Kind: KindPointerMove, Seq: 12, X: 300, Y: 300})
	deliver(t, c, sess, Event{Kind: KindPointerMove, Seq: 11, X: 200, Y: 200}) // late
	deliver(t, c, sess, Event{Kind: KindPointerMove, Seq: 13, X: 400, Y: 400})

	moves := rec.movesSeen()
	if len(moves) != 3 {
		t.Fatalf("%d moves applied, want 3: %+v", len(moves), moves)
	}
	for _, m := range moves {
		if m.x == 200 {
			t.Fatal("a pointer move that arrived late was applied, moving the pointer backwards")
		}
	}

	// Scroll is the same rule and its own sequence space, so a scroll numbered
	// below the last *move* is still applied.
	deliver(t, c, sess, Event{Kind: KindScroll, Seq: 5, DY: 1})
	rec.mu.Lock()
	scrolls := len(rec.scrolls)
	rec.mu.Unlock()
	if scrolls != 1 {
		t.Fatal("a scroll was dropped because a pointer move had a higher sequence number")
	}
}

// TestKeyEventsAreNeverDroppedOnSequence is the other half, and the one whose
// failure mode is a machine typing on its own. A key-up that arrives out of
// order is still a key-up.
func TestKeyEventsAreNeverDroppedOnSequence(t *testing.T) {
	c, rec, sess := newReceiver(t)

	deliver(t, c, sess, Event{Kind: KindKey, Seq: 20, Usage: 0x04, Down: true})
	deliver(t, c, sess, Event{Kind: KindKey, Seq: 19, Usage: 0x04, Down: false}) // out of order
	deliver(t, c, sess, Event{Kind: KindPointerButton, Seq: 5, Button: ButtonLeft, Down: true})
	deliver(t, c, sess, Event{Kind: KindPointerButton, Seq: 4, Button: ButtonLeft, Down: false})

	if got := len(rec.keysSeen()); got != 2 {
		t.Fatalf("%d key events applied, want 2 -- a key-up was dropped and the key is now stuck", got)
	}
	if got := len(rec.buttonsSeen()); got != 2 {
		t.Fatalf("%d button events applied, want 2", got)
	}

	keys, buttons := c.Holding("aaaaaaaaaaaaaaaa")
	if len(keys) != 0 || len(buttons) != 0 {
		t.Fatalf("still holding keys=%v buttons=%v after both were released", keys, buttons)
	}
}

// TestAHeldKeyIsReleasedWhenTheEventsStop is §13's safety release. A dropped
// key-up must not leave a key held on somebody else's machine.
func TestAHeldKeyIsReleasedWhenTheEventsStop(t *testing.T) {
	rec := &recorder{}
	c := New(Config{Injector: rec, Logf: t.Logf})
	defer c.Close()
	sess := &fakeSession{peer: identity.Peer{DeviceID: "aaaaaaaaaaaaaaaa"}}

	deliver(t, c, sess, Event{Kind: KindKey, Seq: 1, Usage: 0x04, Down: true})
	deliver(t, c, sess, Event{Kind: KindPointerButton, Seq: 2, Button: ButtonLeft, Down: true})

	keys, buttons := c.Holding("aaaaaaaaaaaaaaaa")
	if len(keys) != 1 || len(buttons) != 1 {
		t.Fatalf("the receiver is not tracking what is held: keys=%v buttons=%v", keys, buttons)
	}

	// The safety release is five seconds of no traffic. Rather than waiting
	// that long, the check is driven directly with a clock far enough ahead --
	// the timer that calls it is a ticker and has nothing else to prove.
	c.releaseStale(time.Now().Add(SafetyRelease + time.Second))

	keys, buttons = c.Holding("aaaaaaaaaaaaaaaa")
	if len(keys) != 0 || len(buttons) != 0 {
		t.Fatalf("still holding keys=%v buttons=%v after the safety release", keys, buttons)
	}
	released := rec.keysSeen()
	if len(released) != 2 || released[1].down {
		t.Fatalf("the safety release did not send a key-up: %+v", released)
	}
	if ups := rec.buttonsSeen(); len(ups) != 2 || ups[1].down {
		t.Fatalf("the safety release did not send a button-up: %+v", ups)
	}
}

// TestAReleasedKeyIsNotReleasedAgain: the safety net must not fire for a peer
// that let go by itself, or every idle session would inject spurious key-ups.
func TestAReleasedKeyIsNotReleasedAgain(t *testing.T) {
	c, rec, sess := newReceiver(t)

	deliver(t, c, sess, Event{Kind: KindKey, Seq: 1, Usage: 0x04, Down: true})
	deliver(t, c, sess, Event{Kind: KindKey, Seq: 2, Usage: 0x04, Down: false})

	c.releaseStale(time.Now().Add(SafetyRelease + time.Second))

	if got := len(rec.keysSeen()); got != 2 {
		t.Fatalf("%d key events, want 2: the safety release invented one", got)
	}
}

// TestEventsFromAPeerWithNoSessionAreIgnored is D-82: the announcement is the
// authorisation, so events outside one are discarded rather than applied.
func TestEventsFromAPeerWithNoSessionAreIgnored(t *testing.T) {
	rec := &recorder{}
	allowed := false
	c := New(Config{
		Injector: rec,
		Allowed:  func(identity.DeviceID) bool { return allowed },
		Logf:     t.Logf,
	})
	defer c.Close()
	sess := &fakeSession{peer: identity.Peer{DeviceID: "aaaaaaaaaaaaaaaa"}}

	deliver(t, c, sess, Event{Kind: KindKey, Seq: 1, Usage: 0x04, Down: true})
	if got := len(rec.keysSeen()); got != 0 {
		t.Fatalf("%d events applied from a peer with no announced session", got)
	}

	allowed = true
	deliver(t, c, sess, Event{Kind: KindKey, Seq: 2, Usage: 0x04, Down: true})
	if got := len(rec.keysSeen()); got != 1 {
		t.Fatalf("%d events applied once the session was announced, want 1", got)
	}
}

// TestInputRequiresOwned. It is the one capability that hands over the machine,
// so the level is not a detail to get wrong quietly.
func TestInputRequiresOwned(t *testing.T) {
	c := New(Config{})
	defer c.Close()
	if c.RequiredLevel() != identity.LevelOwned {
		t.Fatalf("input requires %v, want Owned", c.RequiredLevel())
	}
	if c.CapID() != 0x05 {
		t.Fatalf("input is capID %d, want 5 (Appendix B)", c.CapID())
	}
}

// TestTheControllerNumbersEventsInOrder, because the receiver's drop-stale rule
// is only as good as the numbering it compares.
func TestTheControllerNumbersEventsInOrder(t *testing.T) {
	sess := &fakeSession{peer: identity.Peer{DeviceID: "bbbbbbbbbbbbbbbb"}}
	ctl := NewController(sess)

	for i := 0; i < 5; i++ {
		if err := ctl.Move(int32(i), int32(i), false); err != nil {
			t.Fatal(err)
		}
	}
	var last uint32
	for i, b := range sess.datagrams() {
		e, err := Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && e.Seq <= last {
			t.Fatalf("event %d has sequence %d after %d", i, e.Seq, last)
		}
		last = e.Seq
	}
}

// TestTypingSendsKeyPairs: `Type` is a convenience over key events, and every
// press it sends has to have a matching release or the far end is left holding
// keys.
func TestTypingSendsKeyPairs(t *testing.T) {
	sess := &fakeSession{peer: identity.Peer{DeviceID: "bbbbbbbbbbbbbbbb"}}
	ctl := NewController(sess)

	if err := ctl.Type(context.Background(), "Hi!"); err != nil {
		t.Fatalf("Type: %v", err)
	}

	held := map[uint16]int{}
	for _, b := range sess.datagrams() {
		e, err := Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind != KindKey {
			t.Fatalf("typing sent a %#x event", e.Kind)
		}
		if e.Down {
			held[e.Usage]++
		} else {
			held[e.Usage]--
		}
	}
	for usage, n := range held {
		if n != 0 {
			t.Fatalf("usage 0x%02x was pressed %d more times than it was released", usage, n)
		}
	}

	// A capital letter needs shift, which is what makes this more than a
	// lookup table.
	var sawShift bool
	for _, b := range sess.datagrams() {
		if e, _ := Decode(b); e.Usage == UsageLeftShift {
			sawShift = true
		}
	}
	if !sawShift {
		t.Fatal("typing a capital letter sent no shift")
	}
}

// TestKeyNamesResolve covers the names a person actually types at a shell.
func TestKeyNamesResolve(t *testing.T) {
	for name, want := range map[string]uint16{
		"enter": UsageEnter,
		"esc":   UsageEscape,
		"f5":    UsageF1 + 4,
		"up":    UsageUp,
		"a":     0x04,
		"tab":   UsageTab,
	} {
		got, _, ok := Usage(name)
		if !ok || got != want {
			t.Fatalf("Usage(%q) = 0x%02x, %v; want 0x%02x", name, got, ok, want)
		}
	}
	if _, _, ok := Usage("no-such-key"); ok {
		t.Fatal("an unknown key name resolved")
	}
}

// TestAChordHoldsModifiersAroundTheKey. Applications watch for key-down with
// modifiers already held; sending the modifier byte alone does not do it on
// every platform.
func TestAChordHoldsModifiersAroundTheKey(t *testing.T) {
	sess := &fakeSession{peer: identity.Peer{DeviceID: "bbbbbbbbbbbbbbbb"}}
	ctl := NewController(sess)

	if err := ctl.Chord(0x06, ModLeftCtrl); err != nil { // ctrl+c
		t.Fatal(err)
	}
	var order []Event
	for _, b := range sess.datagrams() {
		e, err := Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, e)
	}
	if len(order) != 4 {
		t.Fatalf("a chord sent %d events, want 4", len(order))
	}
	if order[0].Usage != UsageLeftCtrl || !order[0].Down {
		t.Fatalf("a chord did not press its modifier first: %+v", order[0])
	}
	if order[3].Usage != UsageLeftCtrl || order[3].Down {
		t.Fatalf("a chord did not release its modifier last: %+v", order[3])
	}
	if order[1].Usage != 0x06 || !order[1].Down {
		t.Fatalf("a chord did not press its key inside the modifier: %+v", order[1])
	}
}

// TestAnUnknownEventKindIsIgnored, so a later version adding one does not take
// a session down (§3.1's spirit).
func TestAnUnknownEventKindIsIgnored(t *testing.T) {
	c, rec, sess := newReceiver(t)

	unknown := []byte{CapID, 0x7f, 1, 0, 0, 0, 9, 9, 9}
	if err := c.ServeDatagram(context.Background(), sess, unknown); err != nil {
		t.Fatalf("an unknown event kind returned %v", err)
	}
	if len(rec.keysSeen())+len(rec.movesSeen()) != 0 {
		t.Fatal("an unknown event kind was applied as something")
	}
}
