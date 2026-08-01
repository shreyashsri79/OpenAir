//go:build !linux && !windows

package input

import "errors"

// Every platform that is neither Linux nor Windows. macOS would use
// CGEventPost and Android InputManager; both need a host to build on, and a
// build that pretended otherwise would fail at run time on the target's machine
// rather than here.
//
// A device with no injector still *sends* input perfectly well — Controller is
// platform-independent — so this is a machine that can drive another and cannot
// be driven.

// ErrNoInjector reports that this build cannot inject input locally.
var ErrNoInjector = errors.New("input: this platform has no injection backend; this device can control others but cannot be controlled")

// NewOSInjector reports that there is none.
func NewOSInjector() (Injector, error) { return nil, ErrNoInjector }
