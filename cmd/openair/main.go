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
  openair clip push DEVICE|ADDR [TEXT]
  openair ls DEVICE [PATH] [-l] [--all] [--socket PATH]
  openair get DEVICE PATH [--out FILE] [--offset N] [--length N]
  openair stream DEVICE PATH [--open|--with PLAYER] [--stop]
  openair input DEVICE [--text TEXT|--stdin] [--key KEY [--mods ctrl,alt]] [--click BUTTON]
  openair mirror DEVICE [--with PLAYER|--open] [--fps N] [--bitrate 8Mb] [--stop]
  openair notify [DEVICE] --title TEXT [--body TEXT|--stdin] [--app ID]
  openair dismiss KEY [--device DEVICE] [--action ID [--reply TEXT]]
  openair protect [--keys DIR]
  openair unlock DEVICE [--for DURATION] [--never-expire] [--socket PATH]
  openair lock [DEVICE] [--socket PATH]
  openair trust DEVICE --owned|--trusted [--never-expire]

commands:
  status    what the daemon is doing
  devices   paired devices, and whatever is visible on the network
  watch     follow daemon events and approve inbound transfers
  pair      exchange keys with another device, once, and pin them
  discover  list the OpenAir devices on this network
  recv      listen for an inbound transfer and write it to --dir
  send      offer FILE... to a device, named or at an explicit host:port
  clip      push this machine's clipboard, or the text you give, to a device
  ls        list what another device shares, without transferring anything
  get       copy a remote file, or a byte range of one, to this machine
  input     type, click and scroll on another device you own
  mirror    watch another device's screen in a media player
  notify    mirror a notification to a device, or to every connected one
  dismiss   clear a mirrored notification everywhere, or press one of its buttons
  protect   create this device's privilege key, sealed with a passphrase
  unlock    start a six-hour owned session for one device
  lock      end an owned session now, or all of them
  trust     grant or take away a paired device's unattended access

With openaird running, pair and send go through it -- one listener, one
identity, and inbound transfers arrive whether or not a terminal is open. Pass
--no-daemon to drive a session from this process instead, which is what happens
anyway when no daemon is reachable. discover and recv are always direct:
"devices" is the daemon's own view, and openaird replaces recv entirely.

Unattended access is separate from pairing, and takes three steps: "protect"
once per device to create the privilege key, "trust DEVICE --owned" on the
machine being reached, and "unlock DEVICE" on the machine reaching it, once per
six hours. Until all three are done a device is Trusted: files and clipboard
work, and someone has to approve inbound transfers.

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
	case "protect":
		return runProtect(args[1:], stdin, stdout)
	case "unlock":
		return runUnlock(args[1:], stdin, stdout)
	case "lock":
		return runLock(args[1:], stdout)
	case "trust":
		return runTrust(args[1:], stdout)
	case "discover":
		return runDiscover(args[1:], stdout)
	case "recv":
		return runRecv(args[1:], stdin, stdout)
	case "send":
		return runSend(args[1:], stdin, stdout)
	case "clip":
		return runClip(args[1:], stdin, stdout)
	case "ls":
		return runLs(args[1:], stdout)
	case "get":
		return runGet(args[1:], stdout)
	case "stream":
		return runStream(args[1:], stdout)
	case "input":
		return runInput(args[1:], stdin, stdout)
	case "mirror":
		return runMirror(args[1:], stdout)
	case "notify":
		return runNotify(args[1:], stdin, stdout)
	case "dismiss":
		return runDismiss(args[1:], stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}
