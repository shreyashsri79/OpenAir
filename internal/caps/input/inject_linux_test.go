//go:build linux

package input

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

// The uinput backend writes struct input_event to a file descriptor, and the
// kernel is not available in a test -- /dev/uinput is root-only, deliberately.
// What can be checked without it is the part that would be wrong silently: the
// byte layout, the key code translation, and the SYN_REPORT that tells the
// kernel one logical event is finished. A missing SYN is the classic uinput
// bug: everything looks right and nothing moves until the next event flushes
// it.

// readEvents parses what the injector wrote.
func readEvents(t *testing.T, path string) []inputEvent {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	size := int(unsafe.Sizeof(inputEvent{}))
	if len(raw)%size != 0 {
		t.Fatalf("%d bytes is not a whole number of %d-byte events", len(raw), size)
	}
	var out []inputEvent
	for off := 0; off+size <= len(raw); off += size {
		b := raw[off : off+size]
		out = append(out, inputEvent{
			Type:  binary.LittleEndian.Uint16(b[16:18]),
			Code:  binary.LittleEndian.Uint16(b[18:20]),
			Value: int32(binary.LittleEndian.Uint32(b[20:24])),
		})
	}
	return out
}

func fileInjector(t *testing.T) (*uinputInjector, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "uinput")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return &uinputInjector{f: f}, path
}

// TestAKeyBecomesAKernelKeyCodeAndASync.
func TestAKeyBecomesAKernelKeyCodeAndASync(t *testing.T) {
	inj, path := fileInjector(t)

	if err := inj.Key(0x04, true, 0); err != nil { // HID 'a'
		t.Fatal(err)
	}
	events := readEvents(t, path)
	if len(events) != 2 {
		t.Fatalf("a key press wrote %d events, want a key and a sync", len(events))
	}
	if events[0].Type != evKey || events[0].Code != 30 || events[0].Value != 1 {
		t.Fatalf("the key event is %+v; HID 0x04 is KEY_A (30) pressed", events[0])
	}
	if events[1].Type != evSyn || events[1].Code != synReport {
		t.Fatalf("no SYN_REPORT after the key: %+v -- the kernel would hold it until the next event", events[1])
	}
}

// TestARelativeMoveIsTwoAxesAndASync, and an absolute one uses the absolute
// axes -- sending a relative event for an absolute move puts the pointer
// somewhere else entirely.
func TestARelativeMoveIsTwoAxesAndASync(t *testing.T) {
	inj, path := fileInjector(t)

	if err := inj.PointerMove(-5, 7, false); err != nil {
		t.Fatal(err)
	}
	events := readEvents(t, path)
	if len(events) != 3 {
		t.Fatalf("a move wrote %d events, want x, y and a sync", len(events))
	}
	if events[0].Type != evRel || events[0].Code != relX || events[0].Value != -5 {
		t.Fatalf("the x axis is %+v", events[0])
	}
	if events[1].Type != evRel || events[1].Code != relY || events[1].Value != 7 {
		t.Fatalf("the y axis is %+v", events[1])
	}

	inj2, path2 := fileInjector(t)
	if err := inj2.PointerMove(100, 200, true); err != nil {
		t.Fatal(err)
	}
	abs := readEvents(t, path2)
	if abs[0].Type != evAbs || abs[1].Type != evAbs {
		t.Fatalf("an absolute move used relative axes: %+v", abs)
	}
}

// TestAnUnmappedUsageIsRefusedRatherThanGuessed. Writing code 0 to uinput is a
// keystroke nobody asked for.
func TestAnUnmappedUsageIsRefusedRatherThanGuessed(t *testing.T) {
	inj, path := fileInjector(t)

	if err := inj.Key(0xfffe, true, 0); err == nil {
		t.Fatal("an unmapped HID usage was injected as something")
	}
	if raw, err := os.ReadFile(path); err != nil || len(raw) != 0 {
		t.Fatalf("%d bytes were written for an unmapped usage", len(raw))
	}
}

// TestEveryMappedUsageHasADistinctKeyCode, within the ranges where the kernel
// gives each key its own code. A duplicate here means two different keys
// arriving as one.
func TestEveryMappedUsageHasADistinctKeyCode(t *testing.T) {
	seen := map[uint16]uint16{}
	for usage, code := range hidToLinux {
		// 0x31 and 0x32 are the same physical key on most layouts, which is
		// why the table maps both to KEY_BACKSLASH.
		if usage == 0x32 {
			continue
		}
		if prev, dup := seen[code]; dup {
			t.Fatalf("HID 0x%02x and 0x%02x both map to kernel key %d", prev, usage, code)
		}
		seen[code] = usage
	}
	if len(seen) < 100 {
		t.Fatalf("only %d keys are mapped; a keyboard has more than that", len(seen))
	}
}

// TestScrollSendsNotchesAndHighResolution: applications that understand
// high-resolution scrolling use the 1/120 axis, and the ones that do not need
// the notch axis, so precise scrolling sends both.
func TestScrollSendsNotchesAndHighResolution(t *testing.T) {
	inj, path := fileInjector(t)

	if err := inj.Scroll(0, 240, true); err != nil {
		t.Fatal(err)
	}
	var sawHiRes, sawNotch bool
	for _, e := range readEvents(t, path) {
		if e.Type == evRel && e.Code == relWheelHiRes {
			sawHiRes = true
		}
		if e.Type == evRel && e.Code == relWheel {
			sawNotch = true
		}
	}
	if !sawHiRes || !sawNotch {
		t.Fatalf("precise scrolling sent hi-res=%v notch=%v; it needs both", sawHiRes, sawNotch)
	}
}
