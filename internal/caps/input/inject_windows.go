//go:build windows

package input

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Injection on Windows is SendInput, which is the documented way in and needs
// no privilege beyond running as the interactive user. There is no cgo here:
// user32.dll is called through x/sys/windows, which keeps the cross-build gate
// (`GOOS=windows go build ./...`) honest from a Linux machine.
//
// Keys are sent as **scan codes** rather than virtual keys. A virtual key is
// post-layout — VK_Y means "the key that produces y on the current layout" —
// and §13's whole position is that the target applies its own layout to a
// physical key. Sending scan codes keeps that true: the same usage lands on the
// same physical key whatever layout the target has loaded.
//
// One caveat this cannot fix: a process running at a higher integrity level
// than the daemon does not receive injected input (UIPI). An elevated window
// focused on the target ignores everything sent here, silently, and that is
// Windows policy rather than something to work around.

var (
	user32       = windows.NewLazySystemDLL("user32.dll")
	procSendIn   = user32.NewProc("SendInput")
	procGetMetrs = user32.NewProc("GetSystemMetrics")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	keyEventKeyUp       = 0x0002
	keyEventScanCode    = 0x0008
	keyEventExtendedKey = 0x0001

	mouseEventMove        = 0x0001
	mouseEventLeftDown    = 0x0002
	mouseEventLeftUp      = 0x0004
	mouseEventRightDown   = 0x0008
	mouseEventRightUp     = 0x0010
	mouseEventMiddleDown  = 0x0020
	mouseEventMiddleUp    = 0x0040
	mouseEventXDown       = 0x0080
	mouseEventXUp         = 0x0100
	mouseEventWheel       = 0x0800
	mouseEventHWheel      = 0x1000
	mouseEventAbsolute    = 0x8000
	mouseEventVirtualDesk = 0x4000

	xButton1 = 0x0001
	xButton2 = 0x0002

	wheelDelta = 120

	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

// mouseInput and keyboardInput are the two arms of the INPUT union. Go has no
// unions, so this is the largest arm laid out by hand; SendInput reads the type
// tag and then the bytes.
type mouseInput struct {
	Type      uint32
	_         uint32 // padding to the union's 8-byte alignment on amd64
	DX        int32
	DY        int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
	_         [8]byte // pad to the size of the keyboard arm
}

type keyboardInput struct {
	Type      uint32
	_         uint32
	VK        uint16
	Scan      uint16
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
	_         [16]byte
}

type winInjector struct {
	mu sync.Mutex
}

// NewOSInjector returns the Windows injector. Nothing is opened: SendInput is
// stateless, and the only failure it has is being refused by UIPI, which it
// reports per call.
func NewOSInjector() (Injector, error) { return &winInjector{}, nil }

func (w *winInjector) send(p unsafe.Pointer, size uintptr) error {
	sent, _, err := procSendIn.Call(1, uintptr(p), size)
	if sent != 1 {
		return fmt.Errorf("input: SendInput was refused: %w", err)
	}
	return nil
}

func (w *winInjector) Key(usage uint16, down bool, _ byte) error {
	scan, extended, ok := windowsScanCode(usage)
	if !ok {
		return fmt.Errorf("input: no Windows scan code for HID usage 0x%02x", usage)
	}
	flags := uint32(keyEventScanCode)
	if extended {
		flags |= keyEventExtendedKey
	}
	if !down {
		flags |= keyEventKeyUp
	}
	in := keyboardInput{Type: inputKeyboard, Scan: scan, Flags: flags}

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.send(unsafe.Pointer(&in), unsafe.Sizeof(in))
}

func (w *winInjector) PointerMove(x, y int32, absolute bool) error {
	in := mouseInput{Type: inputMouse, DX: x, DY: y, Flags: mouseEventMove}
	if absolute {
		// Absolute coordinates are 0..65535 across the virtual desktop, so the
		// caller's screen-space pixels are scaled here rather than by them:
		// only this side knows how large the desktop is.
		left, _, _ := procGetMetrs.Call(smXVirtualScreen)
		top, _, _ := procGetMetrs.Call(smYVirtualScreen)
		width, _, _ := procGetMetrs.Call(smCXVirtualScreen)
		height, _, _ := procGetMetrs.Call(smCYVirtualScreen)
		if width > 0 && height > 0 {
			in.DX = int32((int64(x-int32(left)) * 65535) / int64(width))
			in.DY = int32((int64(y-int32(top)) * 65535) / int64(height))
		}
		in.Flags |= mouseEventAbsolute | mouseEventVirtualDesk
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.send(unsafe.Pointer(&in), unsafe.Sizeof(in))
}

func (w *winInjector) PointerButton(button byte, down bool) error {
	var flags, data uint32
	switch button {
	case ButtonLeft:
		flags = pick(down, mouseEventLeftDown, mouseEventLeftUp)
	case ButtonRight:
		flags = pick(down, mouseEventRightDown, mouseEventRightUp)
	case ButtonMiddle:
		flags = pick(down, mouseEventMiddleDown, mouseEventMiddleUp)
	case ButtonBack:
		flags, data = pick(down, mouseEventXDown, mouseEventXUp), xButton1
	case ButtonForward:
		flags, data = pick(down, mouseEventXDown, mouseEventXUp), xButton2
	default:
		return fmt.Errorf("input: unknown pointer button %d", button)
	}
	in := mouseInput{Type: inputMouse, Flags: flags, MouseData: data}

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.send(unsafe.Pointer(&in), unsafe.Sizeof(in))
}

func (w *winInjector) Scroll(dx, dy int32, precise bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	scale := func(v int32) uint32 {
		if precise {
			// Precise values are already in 1/120 units, which is what
			// mouseData wants.
			return uint32(v)
		}
		return uint32(v * wheelDelta)
	}
	if dy != 0 {
		in := mouseInput{Type: inputMouse, Flags: mouseEventWheel, MouseData: scale(dy)}
		if err := w.send(unsafe.Pointer(&in), unsafe.Sizeof(in)); err != nil {
			return err
		}
	}
	if dx != 0 {
		in := mouseInput{Type: inputMouse, Flags: mouseEventHWheel, MouseData: scale(dx)}
		if err := w.send(unsafe.Pointer(&in), unsafe.Sizeof(in)); err != nil {
			return err
		}
	}
	return nil
}

// Touch becomes an absolute pointer move plus a left button, which is what a
// desktop can honestly do with a touch event. Real injected touch needs
// InitializeTouchInjection and a device declaration, and a Windows machine
// being controlled is a mouse target.
func (w *winInjector) Touch(_ uint8, phase byte, x, y int32) error {
	if err := w.PointerMove(x, y, true); err != nil {
		return err
	}
	switch phase {
	case TouchDown:
		return w.PointerButton(ButtonLeft, true)
	case TouchUp:
		return w.PointerButton(ButtonLeft, false)
	}
	return nil
}

func (w *winInjector) Close() error { return nil }

func pick(down bool, whenDown, whenUp uint32) uint32 {
	if down {
		return whenDown
	}
	return whenUp
}
