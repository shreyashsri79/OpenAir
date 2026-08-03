// Package input is PROTOCOL.md §13: keyboard, pointer and touch injection for
// controlling another machine (capID 5, PRD R25).
//
// It is the one capability with no protobuf at all, and that is deliberate: a
// pointer move is nine bytes of payload, so protobuf framing would cost more
// than the event, and a fixed layout demultiplexes without allocating. Events
// travel on QUIC datagrams, so a burst of mouse movement is never queued behind
// a file transfer's stream and a lost event is lost rather than retransmitted
// late — for a pointer, arriving late is worse than not arriving.
//
// # What is dropped and what is not
//
// §13 divides events in two, and the division is the whole of the correctness
// argument. Pointer moves and scrolls are *samples* of a position: a receiver
// that sees a sequence number lower than one it has already applied throws it
// away, because applying it would move the pointer backwards. Keys and buttons
// are *state transitions*: dropping one leaves a key held down on someone
// else's machine, so they are applied in whatever order they arrive and never
// discarded on sequence.
//
// That asymmetry is why the safety release exists. A key-up can still be lost —
// datagrams are unreliable and the network does not care about our taxonomy —
// so a receiver releases anything held for more than five seconds with no
// traffic. Without it, one dropped packet ends with a machine typing "aaaaa..."
// into whatever is focused, and the person controlling it cannot stop it by
// letting go.
//
// # Authorisation
//
// Controlling a machine nobody is sitting at is exactly what §6's Owned level
// is for, so this requires Owned. A datagram cannot carry an AuthProof (§13
// gives it nowhere to go, and one proof per pointer move would be absurd), so
// the proof travels on the `SessionAnnounce` that §6.3 already requires before
// any use of input, and events are accepted only while that announcement stands
// (D-82). The accessed device shows an indicator for as long as it does, which
// §6.3 also requires.
package input

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
)

// CapID is the input capability's wire ID (Appendix B).
const CapID byte = 0x05

// Event kinds (§13).
const (
	KindKey           byte = 0x01
	KindPointerMove   byte = 0x02
	KindPointerButton byte = 0x03
	KindScroll        byte = 0x04
	KindTouch         byte = 0x05
)

// Touch phases. §13 names the field and leaves the values to the platforms;
// these are the three every touch stack has.
const (
	TouchDown byte = 0x01
	TouchMove byte = 0x02
	TouchUp   byte = 0x03
)

// Pointer buttons, in the order every platform numbers them.
const (
	ButtonLeft    byte = 0x01
	ButtonRight   byte = 0x02
	ButtonMiddle  byte = 0x03
	ButtonBack    byte = 0x04
	ButtonForward byte = 0x05
)

// Modifier bits (§13's `modifiers`), matching the HID keyboard modifier byte,
// because that is where the numbers come from.
const (
	ModLeftCtrl byte = 1 << iota
	ModLeftShift
	ModLeftAlt
	ModLeftGUI
	ModRightCtrl
	ModRightShift
	ModRightAlt
	ModRightGUI
)

const (
	// headerLen is capID, kind and the u32 sequence number.
	headerLen = 6

	// SafetyRelease is how long a key or button may stay held with no traffic
	// before the receiver lets go of it (§13). Five seconds is long enough that
	// a person holding a key deliberately is not interrupted -- key repeat
	// arrives long before then -- and short enough that a lost key-up is an
	// annoyance rather than a machine that has to be rebooted.
	SafetyRelease = 5 * time.Second

	// releaseCheck is how often the safety release is evaluated.
	releaseCheck = time.Second
)

// ErrShortEvent is a datagram too small to be the event it claims to be.
var ErrShortEvent = errors.New("input: truncated event")

// Event is one §13 event, decoded.
//
// One struct for every kind because they are tiny and a type switch at every
// call site would be more code than the fields it saves. Which fields mean
// anything depends on Kind.
type Event struct {
	Kind byte
	Seq  uint32

	// Key
	Usage     uint16 // USB HID usage code, not a platform keycode
	Down      bool
	Modifiers byte

	// Pointer and touch
	X, Y     int32
	Absolute bool

	// Pointer button
	Button byte

	// Scroll
	DX, DY  int32
	Precise bool

	// Touch
	TouchID uint8
	Phase   byte
}

// Encode writes an event in §13's layout: capID, kind, sequence, then a
// kind-dependent body, little-endian throughout (§0).
func Encode(e Event) ([]byte, error) {
	var body []byte
	switch e.Kind {
	case KindKey:
		body = make([]byte, 4)
		binary.LittleEndian.PutUint16(body[0:2], e.Usage)
		body[2] = boolByte(e.Down)
		body[3] = e.Modifiers
	case KindPointerMove:
		body = make([]byte, 9)
		binary.LittleEndian.PutUint32(body[0:4], uint32(e.X))
		binary.LittleEndian.PutUint32(body[4:8], uint32(e.Y))
		body[8] = boolByte(e.Absolute)
	case KindPointerButton:
		body = make([]byte, 2)
		body[0] = e.Button
		body[1] = boolByte(e.Down)
	case KindScroll:
		body = make([]byte, 9)
		binary.LittleEndian.PutUint32(body[0:4], uint32(e.DX))
		binary.LittleEndian.PutUint32(body[4:8], uint32(e.DY))
		body[8] = boolByte(e.Precise)
	case KindTouch:
		body = make([]byte, 10)
		body[0] = e.TouchID
		body[1] = e.Phase
		binary.LittleEndian.PutUint32(body[2:6], uint32(e.X))
		binary.LittleEndian.PutUint32(body[6:10], uint32(e.Y))
	default:
		return nil, fmt.Errorf("input: unknown event kind 0x%02x", e.Kind)
	}

	out := make([]byte, headerLen+len(body))
	out[0] = CapID
	out[1] = e.Kind
	binary.LittleEndian.PutUint32(out[2:6], e.Seq)
	copy(out[headerLen:], body)
	return out, nil
}

// Decode parses a datagram, including its leading capID byte.
func Decode(b []byte) (Event, error) {
	if len(b) < headerLen {
		return Event{}, ErrShortEvent
	}
	if b[0] != CapID {
		return Event{}, fmt.Errorf("input: datagram for capID %d", b[0])
	}
	e := Event{Kind: b[1], Seq: binary.LittleEndian.Uint32(b[2:6])}
	body := b[headerLen:]

	switch e.Kind {
	case KindKey:
		if len(body) < 4 {
			return Event{}, ErrShortEvent
		}
		e.Usage = binary.LittleEndian.Uint16(body[0:2])
		e.Down = body[2] != 0
		e.Modifiers = body[3]
	case KindPointerMove:
		if len(body) < 9 {
			return Event{}, ErrShortEvent
		}
		e.X = int32(binary.LittleEndian.Uint32(body[0:4]))
		e.Y = int32(binary.LittleEndian.Uint32(body[4:8]))
		e.Absolute = body[8] != 0
	case KindPointerButton:
		if len(body) < 2 {
			return Event{}, ErrShortEvent
		}
		e.Button = body[0]
		e.Down = body[1] != 0
	case KindScroll:
		if len(body) < 9 {
			return Event{}, ErrShortEvent
		}
		e.DX = int32(binary.LittleEndian.Uint32(body[0:4]))
		e.DY = int32(binary.LittleEndian.Uint32(body[4:8]))
		e.Precise = body[8] != 0
	case KindTouch:
		if len(body) < 10 {
			return Event{}, ErrShortEvent
		}
		e.TouchID = body[0]
		e.Phase = body[1]
		e.X = int32(binary.LittleEndian.Uint32(body[2:6]))
		e.Y = int32(binary.LittleEndian.Uint32(body[6:10]))
	default:
		// §3.1's spirit: an unknown kind is ignored rather than fatal, so a
		// later version can add one.
		return e, ErrUnknownKind
	}
	return e, nil
}

// ErrUnknownKind is a well-formed event of a kind this build does not know.
var ErrUnknownKind = errors.New("input: unknown event kind")

// Injector is what actually moves the pointer. Implementations are per
// platform; the capability holds one and knows nothing about how it works.
//
// Every method may be called from the datagram goroutine, so an implementation
// that blocks delays every event after it.
type Injector interface {
	Key(usage uint16, down bool, modifiers byte) error
	PointerMove(x, y int32, absolute bool) error
	PointerButton(button byte, down bool) error
	Scroll(dx, dy int32, precise bool) error
	Touch(id uint8, phase byte, x, y int32) error
	Close() error
}

// Config configures the receiving half.
type Config struct {
	// Injector applies events locally. Nil means this device accepts input
	// events and does nothing with them, which is the right behaviour for a
	// build with no backend rather than a refusal that looks like a bug.
	Injector Injector

	// Allowed reports whether this peer may inject right now. It is how the
	// daemon enforces §6.3's announcement: events outside an announced session
	// are discarded (D-82).
	//
	// Nil allows everything, which only a test should want.
	Allowed func(identity.DeviceID) bool

	// OnEvent is called for every event that was applied, for the visible
	// indicator and the local log §6.3 requires.
	OnEvent func(peer identity.DeviceID, e Event)

	Logf func(format string, args ...any)
}

// Capability is the input receiver (§13).
type Capability struct {
	cfg Config

	mu    sync.Mutex
	state map[identity.DeviceID]*peerState

	stop chan struct{}
	once sync.Once
}

// peerState is one controlling peer's sequence and held-key bookkeeping.
type peerState struct {
	// highest sequence applied per kind, for the latest-wins kinds only.
	highest map[byte]uint32

	keys    map[uint16]byte // usage to modifiers it was pressed with
	buttons map[byte]bool
	touches map[uint8]struct{}

	lastSeen time.Time
}

var _ session.DatagramHandler = (*Capability)(nil)

// New builds the receiving capability and starts the safety-release timer.
func New(cfg Config) *Capability {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	c := &Capability{
		cfg:   cfg,
		state: make(map[identity.DeviceID]*peerState),
		stop:  make(chan struct{}),
	}
	go c.releaseLoop()
	return c
}

// Close stops the safety-release timer and releases anything still held.
func (c *Capability) Close() error {
	c.once.Do(func() { close(c.stop) })
	c.ReleaseAll()
	if c.cfg.Injector != nil {
		return c.cfg.Injector.Close()
	}
	return nil
}

// CapID is §13's capability ID.
func (c *Capability) CapID() byte { return CapID }

// RequiredLevel is Owned: controlling a machine unattended is the operation §6
// exists for (PRD R25, D-82).
func (c *Capability) RequiredLevel() identity.TrustLevel { return identity.LevelOwned }

// Serve handles control-stream messages, of which input has none.
func (c *Capability) Serve(context.Context, session.Session, uint16, []byte) error {
	return session.ErrUnknownMsgType
}

// ServeStream is likewise unused: §13 is datagrams only.
func (c *Capability) ServeStream(_ context.Context, _ session.Session, st session.Stream, _ uint16, _ []byte) error {
	st.Reset(uint32(session.CodeCapabilityUnavailable))
	return nil
}

// ServeDatagram applies one event from a peer (§13).
func (c *Capability) ServeDatagram(_ context.Context, sess session.Session, payload []byte) error {
	peer := sess.Peer().DeviceID
	if c.cfg.Allowed != nil && !c.cfg.Allowed(peer) {
		// Not an error: an event arriving a moment after a session was killed
		// is ordinary, and answering it would tell a peer that guessing works.
		return nil
	}

	e, err := Decode(payload)
	if err != nil {
		if errors.Is(err, ErrUnknownKind) {
			return nil
		}
		return err
	}

	if !c.accept(peer, e) {
		return nil
	}
	if err := c.apply(peer, e); err != nil {
		c.cfg.Logf("input: %v", err)
		return nil
	}
	if c.cfg.OnEvent != nil {
		c.cfg.OnEvent(peer, e)
	}
	return nil
}

// accept applies §13's drop-stale rule and records what is now held.
func (c *Capability) accept(peer identity.DeviceID, e Event) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	st := c.state[peer]
	if st == nil {
		st = &peerState{
			highest: map[byte]uint32{},
			keys:    map[uint16]byte{},
			buttons: map[byte]bool{},
			touches: map[uint8]struct{}{},
		}
		c.state[peer] = st
	}
	st.lastSeen = time.Now()

	switch e.Kind {
	case KindPointerMove, KindScroll:
		// Latest wins. An older sample would move the pointer backwards, which
		// is worse than not moving it at all.
		if last, ok := st.highest[e.Kind]; ok && e.Seq <= last {
			return false
		}
		st.highest[e.Kind] = e.Seq
	case KindKey:
		// Never dropped on sequence: a key-down and its key-up are a pair, and
		// discarding either leaves the far end holding a key.
		if e.Down {
			st.keys[e.Usage] = e.Modifiers
		} else {
			delete(st.keys, e.Usage)
		}
	case KindPointerButton:
		if e.Down {
			st.buttons[e.Button] = true
		} else {
			delete(st.buttons, e.Button)
		}
	case KindTouch:
		switch e.Phase {
		case TouchDown, TouchMove:
			st.touches[e.TouchID] = struct{}{}
		case TouchUp:
			delete(st.touches, e.TouchID)
		}
	}
	return true
}

// apply hands one event to the injector.
func (c *Capability) apply(peer identity.DeviceID, e Event) error {
	if c.cfg.Injector == nil {
		return nil
	}
	switch e.Kind {
	case KindKey:
		return c.cfg.Injector.Key(e.Usage, e.Down, e.Modifiers)
	case KindPointerMove:
		return c.cfg.Injector.PointerMove(e.X, e.Y, e.Absolute)
	case KindPointerButton:
		return c.cfg.Injector.PointerButton(e.Button, e.Down)
	case KindScroll:
		return c.cfg.Injector.Scroll(e.DX, e.DY, e.Precise)
	case KindTouch:
		return c.cfg.Injector.Touch(e.TouchID, e.Phase, e.X, e.Y)
	}
	_ = peer
	return nil
}

// releaseLoop is §13's safety release: anything held with no traffic for
// SafetyRelease is let go of.
func (c *Capability) releaseLoop() {
	ticker := time.NewTicker(releaseCheck)
	defer ticker.Stop()

	for {
		select {
		case <-c.stop:
			return
		case now := <-ticker.C:
			c.releaseStale(now)
		}
	}
}

func (c *Capability) releaseStale(now time.Time) {
	c.mu.Lock()
	var stale []identity.DeviceID
	for peer, st := range c.state {
		if now.Sub(st.lastSeen) >= SafetyRelease && st.holding() {
			stale = append(stale, peer)
		}
	}
	c.mu.Unlock()

	for _, peer := range stale {
		c.Release(peer)
	}
}

func (st *peerState) holding() bool {
	return len(st.keys) > 0 || len(st.buttons) > 0 || len(st.touches) > 0
}

// Release lets go of everything one peer is holding. It is what the safety
// timer calls, and what a killed session calls immediately (§6.3).
func (c *Capability) Release(peer identity.DeviceID) {
	c.mu.Lock()
	st := c.state[peer]
	if st == nil {
		c.mu.Unlock()
		return
	}
	keys := make([]uint16, 0, len(st.keys))
	for usage := range st.keys {
		keys = append(keys, usage)
	}
	buttons := make([]byte, 0, len(st.buttons))
	for b := range st.buttons {
		buttons = append(buttons, b)
	}
	touches := make([]uint8, 0, len(st.touches))
	for id := range st.touches {
		touches = append(touches, id)
	}
	st.keys = map[uint16]byte{}
	st.buttons = map[byte]bool{}
	st.touches = map[uint8]struct{}{}
	c.mu.Unlock()

	if c.cfg.Injector == nil {
		return
	}
	for _, usage := range keys {
		if err := c.cfg.Injector.Key(usage, false, 0); err != nil {
			c.cfg.Logf("input: releasing key 0x%02x: %v", usage, err)
		}
	}
	for _, b := range buttons {
		if err := c.cfg.Injector.PointerButton(b, false); err != nil {
			c.cfg.Logf("input: releasing button %d: %v", b, err)
		}
	}
	for _, id := range touches {
		if err := c.cfg.Injector.Touch(id, TouchUp, 0, 0); err != nil {
			c.cfg.Logf("input: releasing touch %d: %v", id, err)
		}
	}
	if len(keys)+len(buttons)+len(touches) > 0 {
		c.cfg.Logf("input: released %d key(s), %d button(s) and %d touch(es) held by %s",
			len(keys), len(buttons), len(touches), peer)
	}
}

// ReleaseAll lets go of everything every peer is holding.
func (c *Capability) ReleaseAll() {
	c.mu.Lock()
	peers := make([]identity.DeviceID, 0, len(c.state))
	for peer := range c.state {
		peers = append(peers, peer)
	}
	c.mu.Unlock()
	for _, peer := range peers {
		c.Release(peer)
	}
}

// Holding reports what a peer currently has pressed, for the indicator §6.3
// requires and for tests.
func (c *Capability) Holding(peer identity.DeviceID) (keys []uint16, buttons []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.state[peer]
	if st == nil {
		return nil, nil
	}
	for usage := range st.keys {
		keys = append(keys, usage)
	}
	for b := range st.buttons {
		buttons = append(buttons, b)
	}
	return keys, buttons
}

// Forget drops a peer's state entirely, after releasing it.
func (c *Capability) Forget(peer identity.DeviceID) {
	c.Release(peer)
	c.mu.Lock()
	delete(c.state, peer)
	c.mu.Unlock()
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}
