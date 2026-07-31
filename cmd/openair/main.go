// Command openair is the command-line client for the local daemon.
//
// M4 added the daemon, so pair, send and discover prefer it when one is
// running and fall back to driving a session from this process when it is not:
//
//	openaird &                             # the daemon, once per device
//	openair status                         # what it is doing
//	openair watch                          # approve inbound transfers here
//	openair pair --listen :9000            # on one device
//	openair pair openair://pair/...        # on the other
//	openair send ./file laptop                # M3: no address typed
//	openair send ./file 10.0.0.5:9000         # still works
//	openair recv --listen :9000 --dir ./inbox # no daemon needed
//
// M3 added LAN discovery, so `send` takes a device name or fingerprint prefix
// as well as a host:port, and `recv` advertises itself while it listens.
//
// M2 replaced M1's fingerprint prompt with pairing: the two devices exchange
// keys once, both users compare six digits, and the keys are pinned. After
// that, transfers go to paired devices and are refused for everyone else --
// on both ends, independently.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "openair: %v\n", err)
		os.Exit(1)
	}
}

const usage = `openair -- direct file transfer over QUIC

usage:
  openair status [--socket PATH]
  openair devices [--paired] [--socket PATH]
  openair watch [--yes] [--socket PATH]
  openair pair [--socket PATH]                      (with a daemon running)
  openair pair --listen ADDR [--keys DIR]           (without one)
  openair pair [--addr ADDR] [--keys DIR] OFFER
  openair discover [--for DURATION] [--watch]
  openair recv [--listen ADDR] [--dir DIR] [--keys DIR] [--yes] [--no-announce]
  openair send [--keys DIR] FILE... DEVICE|ADDR

commands:
  status    what the daemon is doing
  devices   paired devices, and whatever is visible on the network
  watch     follow daemon events and approve inbound transfers
  pair      exchange keys with another device, once, and pin them
  discover  list the OpenAir devices on this network
  recv      listen for an inbound transfer and write it to --dir
  send      offer FILE... to a device, named or at an explicit host:port

With openaird running, pair and send go through it -- one listener, one
identity, and inbound transfers arrive whether or not a terminal is open. Pass
--no-daemon to drive a session from this process instead, which is what happens
anyway when no daemon is reachable. discover and recv are always direct:
"devices" is the daemon's own view, and openaird replaces recv entirely.

Pair before transferring: one device runs "pair --listen" and shows a code,
the other is given that code. Both then display six digits, and pairing
completes only if they match on both screens. Transfers to a device that was
never paired are refused at both ends.
`

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("no command given")
	}

	switch args[0] {
	case "status":
		return runStatus(args[1:], stdout)
	case "devices":
		return runDevices(args[1:], stdout)
	case "watch":
		return runWatch(args[1:], stdin, stdout)
	case "pair":
		return runPair(args[1:], stdin, stdout)
	case "discover":
		return runDiscover(args[1:], stdout)
	case "recv":
		return runRecv(args[1:], stdin, stdout)
	case "send":
		return runSend(args[1:], stdin, stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}
