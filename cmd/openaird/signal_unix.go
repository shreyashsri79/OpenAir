//go:build !windows

package main

import (
	"os"
	"syscall"
)

// sigterm is the signal a service manager stops this daemon with. Windows has
// no SIGTERM; see signal_windows.go.
func sigterm() os.Signal { return syscall.SIGTERM }
