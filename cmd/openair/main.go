// Command openair is the command-line client for the local daemon.
//
// There is no daemon yet (that is M4) and no discovery (M3), so these
// subcommands drive a session directly:
//
//	openair pair --listen :9000            # on one device
//	openair pair openair://pair/...        # on the other
//	openair recv --listen :9000 --dir ./inbox
//	openair send ./file 10.0.0.5:9000
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
  openair pair --listen ADDR [--keys DIR]
  openair pair [--addr ADDR] [--keys DIR] OFFER
  openair recv [--listen ADDR] [--dir DIR] [--keys DIR] [--yes]
  openair send [--keys DIR] FILE... ADDR

commands:
  pair    exchange keys with another device, once, and pin them
  recv    listen for an inbound transfer and write it to --dir
  send    connect to ADDR (host:port) and offer FILE...

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
	case "pair":
		return runPair(args[1:], stdin, stdout)
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
