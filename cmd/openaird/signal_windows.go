package main

import "os"

// sigterm has no Windows equivalent -- a service stop arrives as a control
// event, not a signal. Interrupt is already registered, and repeating it here
// keeps NotifyContext's argument list valid without pretending SIGTERM exists.
func sigterm() os.Signal { return os.Interrupt }
