package input

import (
	"context"
	"fmt"
	"sync/atomic"
	"unicode"

	"github.com/shreyashsri79/openair/internal/session"
)

// The sending half of §13.
//
// A Controller numbers events and puts them on the wire. It keeps no model of
// the far end at all: §13's receiver decides what to drop, and a sender that
// tried to predict that would be guessing about a machine it cannot see.

// Controller sends input events to one peer.
type Controller struct {
	sess session.Session
	seq  atomic.Uint32
}

// NewController wraps a session.
func NewController(sess session.Session) *Controller {
	return &Controller{sess: sess}
}

// Send numbers and sends one event.
//
// The sequence number is per Controller rather than per kind, and it is the
// receiver that compares within a kind. One counter is enough for that, and two
// senders numbering independently would be a receiver comparing unrelated
// numbers.
func (c *Controller) Send(e Event) error {
	e.Seq = c.seq.Add(1)
	b, err := Encode(e)
	if err != nil {
		return err
	}
	return c.sess.SendDatagram(b)
}

// Key presses or releases one HID usage code.
func (c *Controller) Key(usage uint16, down bool, modifiers byte) error {
	return c.Send(Event{Kind: KindKey, Usage: usage, Down: down, Modifiers: modifiers})
}

// Tap presses and releases a key.
func (c *Controller) Tap(usage uint16, modifiers byte) error {
	if err := c.Key(usage, true, modifiers); err != nil {
		return err
	}
	return c.Key(usage, false, modifiers)
}

// Move moves the pointer, relatively or in the target's screen space.
func (c *Controller) Move(x, y int32, absolute bool) error {
	return c.Send(Event{Kind: KindPointerMove, X: x, Y: y, Absolute: absolute})
}

// Button presses or releases a pointer button.
func (c *Controller) Button(button byte, down bool) error {
	return c.Send(Event{Kind: KindPointerButton, Button: button, Down: down})
}

// Click presses and releases a pointer button.
func (c *Controller) Click(button byte) error {
	if err := c.Button(button, true); err != nil {
		return err
	}
	return c.Button(button, false)
}

// Scroll scrolls by dx/dy. precise says the values are pixels rather than
// notches.
func (c *Controller) Scroll(dx, dy int32, precise bool) error {
	return c.Send(Event{Kind: KindScroll, DX: dx, DY: dy, Precise: precise})
}

// Touch sends one touch point.
func (c *Controller) Touch(id uint8, phase byte, x, y int32) error {
	return c.Send(Event{Kind: KindTouch, TouchID: id, Phase: phase, X: x, Y: y})
}

// Type sends a string as key events.
//
// This is the one place where the protocol's refusal to carry characters costs
// something: §13 sends HID usage codes and the target applies its own layout,
// so typing a character means guessing which key produces it. The guess is a US
// layout, and it is a guess — on a target with another layout, `y` on a QWERTZ
// keyboard is where `z` is here. That is the correct trade for a remote-control
// protocol (the alternative is the source deciding the target's layout, which
// is worse), and it is why Type is a convenience and Key is the primitive.
func (c *Controller) Type(ctx context.Context, text string) error {
	for _, r := range text {
		if err := ctx.Err(); err != nil {
			return err
		}
		usage, shift, ok := usageForRune(r)
		if !ok {
			return fmt.Errorf("input: no US-layout key produces %q; send it with --key", r)
		}
		var mods byte
		if shift {
			mods = ModLeftShift
			if err := c.Key(UsageLeftShift, true, mods); err != nil {
				return err
			}
		}
		if err := c.Tap(usage, mods); err != nil {
			return err
		}
		if shift {
			if err := c.Key(UsageLeftShift, false, 0); err != nil {
				return err
			}
		}
	}
	return nil
}

// usageForRune maps a character to the US-layout key that produces it.
func usageForRune(r rune) (usage uint16, shift bool, ok bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return uint16(0x04 + (r - 'a')), false, true
	case r >= 'A' && r <= 'Z':
		return uint16(0x04 + (unicode.ToLower(r) - 'a')), true, true
	case r >= '1' && r <= '9':
		return uint16(0x1e + (r - '1')), false, true
	case r == '0':
		return 0x27, false, true
	}
	if u, ok := punctuation[r]; ok {
		return u.usage, u.shift, true
	}
	return 0, false, false
}

type shifted struct {
	usage uint16
	shift bool
}

// punctuation is the rest of a US layout, unshifted and shifted.
var punctuation = map[rune]shifted{
	'\n': {UsageEnter, false},
	'\t': {UsageTab, false},
	' ':  {UsageSpace, false},
	'-':  {0x2d, false}, '_': {0x2d, true},
	'=': {0x2e, false}, '+': {0x2e, true},
	'[': {0x2f, false}, '{': {0x2f, true},
	']': {0x30, false}, '}': {0x30, true},
	'\\': {0x31, false}, '|': {0x31, true},
	';': {0x33, false}, ':': {0x33, true},
	'\'': {0x34, false}, '"': {0x34, true},
	'`': {0x35, false}, '~': {0x35, true},
	',': {0x36, false}, '<': {0x36, true},
	'.': {0x37, false}, '>': {0x37, true},
	'/': {0x38, false}, '?': {0x38, true},
	'!': {0x1e, true}, '@': {0x1f, true}, '#': {0x20, true}, '$': {0x21, true},
	'%': {0x22, true}, '^': {0x23, true}, '&': {0x24, true}, '*': {0x25, true},
	'(': {0x26, true}, ')': {0x27, true},
}

// Named keys, as HID usage codes (usage page 0x07). These are the ones a script
// or a shell actually names.
const (
	UsageEnter     uint16 = 0x28
	UsageEscape    uint16 = 0x29
	UsageBackspace uint16 = 0x2a
	UsageTab       uint16 = 0x2b
	UsageSpace     uint16 = 0x2c
	UsageCapsLock  uint16 = 0x39
	UsageF1        uint16 = 0x3a
	UsagePrintScr  uint16 = 0x46
	UsageHome      uint16 = 0x4a
	UsagePageUp    uint16 = 0x4b
	UsageDelete    uint16 = 0x4c
	UsageEnd       uint16 = 0x4d
	UsagePageDown  uint16 = 0x4e
	UsageRight     uint16 = 0x4f
	UsageLeft      uint16 = 0x50
	UsageDown      uint16 = 0x51
	UsageUp        uint16 = 0x52
	UsageLeftCtrl  uint16 = 0xe0
	UsageLeftShift uint16 = 0xe1
	UsageLeftAlt   uint16 = 0xe2
	UsageLeftGUI   uint16 = 0xe3
)

// namedKeys is what `openair input --key NAME` accepts.
var namedKeys = map[string]uint16{
	"enter": UsageEnter, "return": UsageEnter,
	"escape": UsageEscape, "esc": UsageEscape,
	"backspace": UsageBackspace,
	"tab":       UsageTab,
	"space":     UsageSpace,
	"delete":    UsageDelete, "del": UsageDelete,
	"home": UsageHome, "end": UsageEnd,
	"pageup": UsagePageUp, "pagedown": UsagePageDown,
	"up": UsageUp, "down": UsageDown, "left": UsageLeft, "right": UsageRight,
	"printscreen": UsagePrintScr,
	"capslock":    UsageCapsLock,
	"ctrl":        UsageLeftCtrl, "shift": UsageLeftShift,
	"alt": UsageLeftAlt, "super": UsageLeftGUI, "win": UsageLeftGUI, "cmd": UsageLeftGUI,
}

// Usage resolves a key name to a HID usage code. Function keys are F1 to F12,
// and a single character resolves through the US layout.
func Usage(name string) (usage uint16, shift bool, ok bool) {
	if u, found := namedKeys[name]; found {
		return u, false, true
	}
	if len(name) >= 2 && (name[0] == 'f' || name[0] == 'F') {
		var n int
		if _, err := fmt.Sscanf(name[1:], "%d", &n); err == nil && n >= 1 && n <= 12 {
			return UsageF1 + uint16(n-1), false, true
		}
	}
	runes := []rune(name)
	if len(runes) == 1 {
		return usageForRune(runes[0])
	}
	return 0, false, false
}

// Modifiers turns names into §13's modifier byte.
func Modifiers(names []string) (byte, error) {
	var mods byte
	for _, name := range names {
		switch name {
		case "ctrl", "control":
			mods |= ModLeftCtrl
		case "shift":
			mods |= ModLeftShift
		case "alt", "option":
			mods |= ModLeftAlt
		case "super", "win", "cmd", "gui":
			mods |= ModLeftGUI
		default:
			return 0, fmt.Errorf("input: unknown modifier %q", name)
		}
	}
	return mods, nil
}

// ModifierUsages is the key each modifier bit corresponds to, so a chord can be
// sent as real key-downs rather than only as a modifier byte. Platforms differ
// on which they honour, and sending both is what works everywhere.
func ModifierUsages(mods byte) []uint16 {
	var out []uint16
	if mods&ModLeftCtrl != 0 {
		out = append(out, UsageLeftCtrl)
	}
	if mods&ModLeftShift != 0 {
		out = append(out, UsageLeftShift)
	}
	if mods&ModLeftAlt != 0 {
		out = append(out, UsageLeftAlt)
	}
	if mods&ModLeftGUI != 0 {
		out = append(out, UsageLeftGUI)
	}
	return out
}

// Chord holds the modifiers down, taps the key, and lets go — in that order,
// which is the order a keyboard produces and the only one that works with
// applications that watch for key-down.
func (c *Controller) Chord(usage uint16, mods byte) error {
	held := ModifierUsages(mods)
	for _, m := range held {
		if err := c.Key(m, true, mods); err != nil {
			return err
		}
	}
	err := c.Tap(usage, mods)
	for i := len(held) - 1; i >= 0; i-- {
		if rerr := c.Key(held[i], false, 0); err == nil {
			err = rerr
		}
	}
	return err
}
