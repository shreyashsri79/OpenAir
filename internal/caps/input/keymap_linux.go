//go:build linux

package input

// HID usage codes (usage page 0x07) to Linux evdev key codes.
//
// §13 puts HID usages on the wire and lets the target apply its own layout,
// which is what this table is: the physical key, not the character. A US
// keyboard's `y` and a German one's `z` are the same usage and the same evdev
// code, and the difference appears when the desktop's own layout is applied on
// top — which is the only place that knows what it is.
//
// The values are the kernel's, from input-event-codes.h. They are written out
// rather than derived because the sequence is not arithmetic: the alphabet is
// scattered across the number space in typewriter order, and F11 and F12 are
// nowhere near F1 to F10.
var hidToLinux = map[uint16]uint16{
	0x04: 30, 0x05: 48, 0x06: 46, 0x07: 32, 0x08: 18, 0x09: 33, // a b c d e f
	0x0a: 34, 0x0b: 35, 0x0c: 23, 0x0d: 36, 0x0e: 37, 0x0f: 38, // g h i j k l
	0x10: 50, 0x11: 49, 0x12: 24, 0x13: 25, 0x14: 16, 0x15: 19, // m n o p q r
	0x16: 31, 0x17: 20, 0x18: 22, 0x19: 47, 0x1a: 17, 0x1b: 45, // s t u v w x
	0x1c: 21, 0x1d: 44, // y z

	0x1e: 2, 0x1f: 3, 0x20: 4, 0x21: 5, 0x22: 6, // 1-5
	0x23: 7, 0x24: 8, 0x25: 9, 0x26: 10, 0x27: 11, // 6-9, 0

	0x28: 28, // enter
	0x29: 1,  // escape
	0x2a: 14, // backspace
	0x2b: 15, // tab
	0x2c: 57, // space
	0x2d: 12, // minus
	0x2e: 13, // equal
	0x2f: 26, // left brace
	0x30: 27, // right brace
	0x31: 43, // backslash
	0x32: 43, // non-US hash, same key on most layouts
	0x33: 39, // semicolon
	0x34: 40, // apostrophe
	0x35: 41, // grave
	0x36: 51, // comma
	0x37: 52, // dot
	0x38: 53, // slash
	0x39: 58, // caps lock

	0x3a: 59, 0x3b: 60, 0x3c: 61, 0x3d: 62, 0x3e: 63, 0x3f: 64, // F1-F6
	0x40: 65, 0x41: 66, 0x42: 67, 0x43: 68, 0x44: 87, 0x45: 88, // F7-F12

	0x46: 99,  // print screen
	0x47: 70,  // scroll lock
	0x48: 119, // pause
	0x49: 110, // insert
	0x4a: 102, // home
	0x4b: 104, // page up
	0x4c: 111, // delete
	0x4d: 107, // end
	0x4e: 109, // page down
	0x4f: 106, // right
	0x50: 105, // left
	0x51: 108, // down
	0x52: 103, // up

	0x53: 69,                                         // num lock
	0x54: 98, 0x55: 55, 0x56: 74, 0x57: 78, 0x58: 96, // keypad / * - + enter
	0x59: 79, 0x5a: 80, 0x5b: 81, 0x5c: 75, 0x5d: 76, // keypad 1-5
	0x5e: 77, 0x5f: 71, 0x60: 72, 0x61: 73, // keypad 6-9
	0x62: 82, 0x63: 83, // keypad 0 and dot
	0x64: 86,  // 102nd key
	0x65: 127, // compose

	0xe0: 29, 0xe1: 42, 0xe2: 56, 0xe3: 125, // left ctrl shift alt meta
	0xe4: 97, 0xe5: 54, 0xe6: 100, 0xe7: 126, // right ctrl shift alt meta
}

func linuxKeyCode(usage uint16) (uint16, bool) {
	code, ok := hidToLinux[usage]
	return code, ok
}
