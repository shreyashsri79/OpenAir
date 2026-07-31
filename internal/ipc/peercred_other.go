//go:build !linux

package ipc

import "net"

// CheckPeer is the credential check where the platform does not give one in a
// form worth relying on.
//
// Windows needs none: the pipe ACL in transport_windows.go names the owning
// user, so a connection from anyone else never reaches here. On the BSDs and
// macOS the equivalent (LOCAL_PEERCRED / getpeereid) exists and is not wired up
// yet, so those platforms rest on the 0700 directory and 0600 socket alone.
// That is the same protection the key file itself has, which bounds the gap:
// an attacker who can defeat it can read the identity key directly.
func CheckPeer(net.Conn) error { return nil }
