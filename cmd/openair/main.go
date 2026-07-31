// Command openair is the command-line client for the local daemon.
//
// M1 has no daemon yet (that is M4) and no discovery (M3), so the two
// subcommands here drive a session directly:
//
//	openair recv --listen :9000 --dir ./inbox
//	openair send ./file 10.0.0.5:9000
//
// There is no pairing at this milestone either (M2), so neither end knows the
// other's key in advance. Both are shown the peer's fingerprint and asked to
// confirm it out of band -- which is the whole of M1's security model, and the
// reason --yes exists but is not the default.
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
  openair recv [--listen ADDR] [--dir DIR] [--keys DIR] [--yes]
  openair send [--keys DIR] [--yes] FILE... ADDR

commands:
  recv    listen for an inbound transfer and write it to --dir
  send    connect to ADDR (host:port) and offer FILE...

Both ends print the peer's device fingerprint and wait for confirmation.
Compare the two by some means other than this connection -- that comparison
is what authenticates the transfer until pairing lands.
`

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("no command given")
	}

	switch args[0] {
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
