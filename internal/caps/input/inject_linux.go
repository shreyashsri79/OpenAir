//go:build linux

package input

import (
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Injection on Linux is /dev/uinput: this process creates a virtual keyboard
// and pointer in the kernel, and writes events to it.
//
// Not XTest, and not the exec-a-helper arrangement D-54 chose for the
// clipboard. A clipboard write is one event a minute and a subprocess is fine;
// input is hundreds of events a second, so a process per event is not a design
// but a denial of service against oneself. XTest was the other candidate and it
// is X11-only — on Wayland it does nothing at all, which would ship a feature
// that silently fails on the desktop most of this project's users are running.
// uinput is below the display server, so the same code drives X11, Wayland and
// a bare console.
//
// The cost is a permission: /dev/uinput is root-only by default. That is the
// honest form of the requirement — injecting keystrokes into a machine is
// exactly as privileged as it sounds — and the error says how to grant it
// rather than failing with EACCES and nothing else.

const (
	uinputPath = "/dev/uinput"

	uiDevCreate  = 0x5501
	uiDevDestroy = 0x5502
	uiDevSetup   = 0x405c5503
	uiSetEvBit   = 0x40045564
	uiSetKeyBit  = 0x40045565
	uiSetRelBit  = 0x40045566
	uiSetAbsBit  = 0x40045567

	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03

	synReport = 0x00

	relX      = 0x00
	relY      = 0x01
	relWheel  = 0x08
	relHWheel = 0x06
	// High-resolution wheel, in 1/120 of a notch. Used for precise scrolling,
	// which is what a trackpad produces and what makes remote scrolling feel
	// like scrolling rather than like stepping.
	relWheelHiRes  = 0x0b
	relHWheelHiRes = 0x0c

	absX = 0x00
	absY = 0x01

	btnLeft    = 0x110
	btnRight   = 0x111
	btnMiddle  = 0x112
	btnSide    = 0x113
	btnExtra   = 0x114
	btnTouch   = 0x14a
	btnToolPen = 0x140

	// absMax is the coordinate space the virtual absolute device declares.
	// Absolute events arrive in the target's screen space (§13), and the
	// kernel scales from this range, so a device declaring 0..absMax and a
	// caller sending screen pixels agree only if the caller says how wide the
	// screen is. Until M15 declares dimensions, absolute events are scaled by
	// the caller and this is a plain 0..65535 space.
	absMax = 65535
)

// inputEvent is struct input_event on a 64-bit kernel.
type inputEvent struct {
	Sec   int64
	Usec  int64
	Type  uint16
	Code  uint16
	Value int32
}

// uinputSetup is struct uinput_setup.
type uinputSetup struct {
	ID struct {
		Bustype uint16
		Vendor  uint16
		Product uint16
		Version uint16
	}
	Name         [80]byte
	FFEffectsMax uint32
	_            [4]byte
}

// uinputInjector is an Injector backed by a virtual kernel device.
type uinputInjector struct {
	mu sync.Mutex
	f  *os.File
}

// NewOSInjector opens /dev/uinput and creates the virtual device.
func NewOSInjector() (Injector, error) {
	f, err := os.OpenFile(uinputPath, os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("input: %s does not exist; load the uinput module (`sudo modprobe uinput`)", uinputPath)
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("input: cannot open %s: %w\n"+
				"injecting input needs write access to it. Add a udev rule granting your user, "+
				"or run the daemon as a user that has it — this is a real privilege and it is "+
				"meant to be granted deliberately", uinputPath, err)
		}
		return nil, err
	}

	inj := &uinputInjector{f: f}
	if err := inj.setup(); err != nil {
		f.Close()
		return nil, err
	}
	return inj, nil
}

func (u *uinputInjector) setup() error {
	fd := u.f.Fd()

	for _, ev := range []uintptr{evKey, evRel, evAbs, evSyn} {
		if err := ioctl(fd, uiSetEvBit, ev); err != nil {
			return fmt.Errorf("input: enabling event type %d: %w", ev, err)
		}
	}
	// Every key code the HID table can produce, plus the buttons.
	for _, code := range allKeyCodes() {
		if err := ioctl(fd, uiSetKeyBit, uintptr(code)); err != nil {
			return fmt.Errorf("input: enabling key %d: %w", code, err)
		}
	}
	for _, rel := range []uintptr{relX, relY, relWheel, relHWheel, relWheelHiRes, relHWheelHiRes} {
		if err := ioctl(fd, uiSetRelBit, rel); err != nil {
			return fmt.Errorf("input: enabling relative axis %d: %w", rel, err)
		}
	}
	for _, abs := range []uintptr{absX, absY} {
		if err := ioctl(fd, uiSetAbsBit, abs); err != nil {
			return fmt.Errorf("input: enabling absolute axis %d: %w", abs, err)
		}
	}

	var setup uinputSetup
	setup.ID.Bustype = 0x03 // BUS_USB, which is what every desktop expects
	setup.ID.Vendor = 0x1d6b
	setup.ID.Product = 0x0104
	setup.ID.Version = 1
	copy(setup.Name[:], "OpenAir remote input")

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, uiDevSetup, uintptr(unsafe.Pointer(&setup))); errno != 0 {
		return fmt.Errorf("input: uinput setup: %w", errno)
	}
	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		return fmt.Errorf("input: creating the virtual device: %w", err)
	}
	// The kernel announces the device to userspace asynchronously; writing
	// before a compositor has noticed it drops the first events, which looks
	// like a keystroke going missing.
	time.Sleep(200 * time.Millisecond)
	return nil
}

func ioctl(fd, req, arg uintptr) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, req, arg); errno != 0 {
		return errno
	}
	return nil
}

func (u *uinputInjector) emit(evType, code uint16, value int32) error {
	ev := inputEvent{Type: evType, Code: code, Value: value}
	buf := (*[unsafe.Sizeof(ev)]byte)(unsafe.Pointer(&ev))[:]
	_, err := u.f.Write(buf)
	return err
}

// sync tells the kernel one logical event is complete.
func (u *uinputInjector) sync() error { return u.emit(evSyn, synReport, 0) }

func (u *uinputInjector) Key(usage uint16, down bool, _ byte) error {
	code, ok := linuxKeyCode(usage)
	if !ok {
		return fmt.Errorf("input: no Linux key code for HID usage 0x%02x", usage)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.emit(evKey, code, boolValue(down)); err != nil {
		return err
	}
	return u.sync()
}

func (u *uinputInjector) PointerMove(x, y int32, absolute bool) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	evType, codeX, codeY := uint16(evRel), uint16(relX), uint16(relY)
	if absolute {
		evType, codeX, codeY = evAbs, absX, absY
	}
	if err := u.emit(evType, codeX, x); err != nil {
		return err
	}
	if err := u.emit(evType, codeY, y); err != nil {
		return err
	}
	return u.sync()
}

func (u *uinputInjector) PointerButton(button byte, down bool) error {
	code, ok := linuxButton(button)
	if !ok {
		return fmt.Errorf("input: unknown pointer button %d", button)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.emit(evKey, code, boolValue(down)); err != nil {
		return err
	}
	return u.sync()
}

func (u *uinputInjector) Scroll(dx, dy int32, precise bool) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if precise {
		// 1/120 notch units. Applications that understand high-resolution
		// scrolling use these; the others need the notch events too, so both
		// are sent and the kernel's own accumulation is not relied upon.
		if dy != 0 {
			if err := u.emit(evRel, relWheelHiRes, dy); err != nil {
				return err
			}
			if err := u.emit(evRel, relWheel, dy/120); err != nil {
				return err
			}
		}
		if dx != 0 {
			if err := u.emit(evRel, relHWheelHiRes, dx); err != nil {
				return err
			}
			if err := u.emit(evRel, relHWheel, dx/120); err != nil {
				return err
			}
		}
		return u.sync()
	}
	if dy != 0 {
		if err := u.emit(evRel, relWheel, dy); err != nil {
			return err
		}
	}
	if dx != 0 {
		if err := u.emit(evRel, relHWheel, dx); err != nil {
			return err
		}
	}
	return u.sync()
}

// Touch is emitted as an absolute pointer with BTN_TOUCH, which is what a
// single-touch digitiser looks like. Multi-touch needs the MT protocol and a
// device declared for it; §13 allows several IDs and this collapses them,
// which is honest for a desktop target and is revisited when M15 gives a
// phone something to touch.
func (u *uinputInjector) Touch(_ uint8, phase byte, x, y int32) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if err := u.emit(evAbs, absX, x); err != nil {
		return err
	}
	if err := u.emit(evAbs, absY, y); err != nil {
		return err
	}
	switch phase {
	case TouchDown:
		if err := u.emit(evKey, btnTouch, 1); err != nil {
			return err
		}
	case TouchUp:
		if err := u.emit(evKey, btnTouch, 0); err != nil {
			return err
		}
	}
	return u.sync()
}

func (u *uinputInjector) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.f == nil {
		return nil
	}
	_ = ioctl(u.f.Fd(), uiDevDestroy, 0)
	err := u.f.Close()
	u.f = nil
	return err
}

func boolValue(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func linuxButton(button byte) (uint16, bool) {
	switch button {
	case ButtonLeft:
		return btnLeft, true
	case ButtonRight:
		return btnRight, true
	case ButtonMiddle:
		return btnMiddle, true
	case ButtonBack:
		return btnSide, true
	case ButtonForward:
		return btnExtra, true
	}
	return 0, false
}

// allKeyCodes is every code the HID table maps to, plus the pointer buttons,
// so the device declares exactly what it can produce.
func allKeyCodes() []uint16 {
	seen := map[uint16]struct{}{}
	var out []uint16
	for _, code := range hidToLinux {
		if code == 0 {
			continue
		}
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	out = append(out, btnLeft, btnRight, btnMiddle, btnSide, btnExtra, btnTouch, btnToolPen)
	return out
}
