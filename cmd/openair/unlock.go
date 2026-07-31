package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/shreyashsri79/openair/internal/daemon"
	"github.com/shreyashsri79/openair/internal/identity"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// Unattended access, from the outside: `protect` once, `unlock` per device per
// six hours, `trust --owned` to say which devices it applies to (M6).
//
// The split is D-20's. Pairing gets you Trusted, which is clipboard and file
// offers on the always-warm identity key. Owned is the unattended level, it
// rides a second key that is sealed at rest, and it is deliberately three
// separate deliberate acts to reach.

// runProtect is `openair protect`: create this device's privilege key.
//
// It runs against the key directory rather than the daemon, because it must
// work before anything is unlocked and because the passphrase it takes seals a
// file rather than opening a session. The daemon detects the result on its next
// start (identity.DetectTier).
func runProtect(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("protect", flag.ContinueOnError)
	fs.SetOutput(stdout)
	keys := fs.String("keys", "", "directory holding this device's keys")
	fromStdin := fs.Bool("passphrase-stdin", false, "read the passphrase from standard input instead of prompting")
	if _, err := parseInterleaved(fs, args); err != nil {
		return err
	}

	dir := *keys
	if dir == "" {
		dir = defaultKeyDir()
	}
	if identity.HasPrivilegeKey(dir) {
		tier, err := identity.DetectTier(dir)
		if err != nil {
			return err
		}
		return fmt.Errorf("this device already has a privilege key (%s); "+
			"delete privilege.key and privilege.pub in %s to start again, "+
			"which un-pairs Owned access on every device that pinned it", tierName(tier), dir)
	}

	pass, err := readPassphrase(stdin, stdout, *fromStdin,
		"Passphrase to protect this device's privilege key: ", true)
	if err != nil {
		return err
	}
	// D-21 tier 2 is a passphrase, not a PIN: roughly four words. Short ones
	// are refused here rather than in an error a user sees six hours later.
	if len(pass) < 12 {
		return fmt.Errorf("that passphrase is %d characters; use four words or more -- "+
			"an attacker who copies the sealed file can grind a short one offline (D-21)", len(pass))
	}

	id, err := identity.LoadOrCreate(identity.Options{
		Dir:        dir,
		Tier:       identity.TierPassphrase,
		Passphrase: pass,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "privilege key created, sealed with your passphrase (tier 2)\n")
	fmt.Fprintf(stdout, "device %s\n", fingerprint(id.DeviceID()))
	fmt.Fprintf(stdout, "\nTwo things follow from this:\n")
	fmt.Fprintf(stdout, "  - Devices paired before now did not receive this key. Pair them again\n")
	fmt.Fprintf(stdout, "    before promoting any of them to owned.\n")
	fmt.Fprintf(stdout, "  - Forgetting the passphrase means re-pairing everything: the key cannot\n")
	fmt.Fprintf(stdout, "    be recovered, by design.\n")
	if err := daemonRestartHint(stdout); err != nil {
		return err
	}
	return nil
}

// runUnlock is `openair unlock DEVICE`: D-18's challenge, D-30's scope.
func runUnlock(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("unlock", flag.ContinueOnError)
	fs.SetOutput(stdout)
	socket := fs.String("socket", "", "daemon IPC socket path")
	fromStdin := fs.Bool("passphrase-stdin", false, "read the passphrase from standard input instead of prompting")
	never := fs.Bool("never-expire", false, "keep this device unlocked until locked by hand (D-20's always-on)")
	lifetime := fs.Duration("for", 0, "how long the unlock lasts; default six hours")
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: openair unlock DEVICE [--for DURATION] [--never-expire]")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	c, err := daemon.Connect(ctx, *socket, nil, nil)
	if err != nil {
		return err
	}
	defer c.Close()

	target, err := resolveDevice(ctx, c, rest[0])
	if err != nil {
		return err
	}

	// The prompt names the device, which is the argument D-30 rests on: "unlock
	// to reach desktop-home" states what is being authorised, where "unlock
	// OpenAir" states nothing and trains people to approve reflexively.
	pass, err := readPassphrase(stdin, stdout, *fromStdin,
		fmt.Sprintf("Passphrase to unlock owned access to %s: ", deviceLabel(target)), false)
	if err != nil {
		return err
	}

	resp, err := c.Unlock(ctx, target.GetDeviceId(), pass, nil, *never, *lifetime)
	if err != nil {
		return err
	}

	if resp.GetExpiresUnixMs() == 0 {
		fmt.Fprintf(stdout, "%s unlocked until you lock it\n", deviceLabel(target))
	} else {
		at := time.UnixMilli(resp.GetExpiresUnixMs())
		fmt.Fprintf(stdout, "%s unlocked until %s (%s)\n",
			deviceLabel(target), at.Format(time.Kitchen), time.Until(at).Round(time.Minute))
	}
	if resp.GetKeySwappable() {
		fmt.Fprintf(stdout, "warning: this machine refused to lock the key into RAM, so it may reach swap\n")
	}
	return nil
}

// runLock is `openair lock [DEVICE]`. With no device it ends every session,
// which is the "I am walking away from this machine" action.
func runLock(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("lock", flag.ContinueOnError)
	fs.SetOutput(stdout)
	socket := fs.String("socket", "", "daemon IPC socket path")
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := daemon.Connect(ctx, *socket, nil, nil)
	if err != nil {
		return err
	}
	defer c.Close()

	if len(rest) == 0 {
		if err := c.Lock(ctx, ""); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "every unlock session ended; the privilege key is sealed again")
		return nil
	}

	target, err := resolveDevice(ctx, c, rest[0])
	if err != nil {
		return err
	}
	if err := c.Lock(ctx, target.GetDeviceId()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s locked\n", deviceLabel(target))
	return nil
}

// runTrust is `openair trust DEVICE --owned|--trusted`, §6.4's local act.
func runTrust(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("trust", flag.ContinueOnError)
	fs.SetOutput(stdout)
	socket := fs.String("socket", "", "daemon IPC socket path")
	owned := fs.Bool("owned", false, "grant unattended access (needs an unlock per six hours)")
	trusted := fs.Bool("trusted", false, "take unattended access away, keeping the pairing")
	never := fs.Bool("never-expire", false, "record this device as always-on (D-20)")
	timed := fs.Bool("timed", false, "record this device as expiring on the usual six-hour timer")
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: openair trust DEVICE --owned|--trusted [--never-expire]")
	}
	if *owned == *trusted {
		return errors.New("say which: --owned grants unattended access, --trusted takes it away")
	}

	policy := ""
	switch {
	case *never && *timed:
		return errors.New("--never-expire and --timed are opposites")
	case *never:
		policy = identity.PolicyNever
	case *timed:
		policy = identity.PolicyTimed
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := daemon.Connect(ctx, *socket, nil, nil)
	if err != nil {
		return err
	}
	defer c.Close()

	target, err := resolveDevice(ctx, c, rest[0])
	if err != nil {
		return err
	}

	level := openairv1.TrustLevel_TRUST_LEVEL_TRUSTED
	if *owned {
		level = openairv1.TrustLevel_TRUST_LEVEL_OWNED
	}
	got, err := c.Trust(ctx, target.GetDeviceId(), level, policy)
	if err != nil {
		return err
	}

	switch got {
	case openairv1.TrustLevel_TRUST_LEVEL_OWNED:
		fmt.Fprintf(stdout, "%s is now owned: it can act unattended on this device\n", deviceLabel(target))
		// The unlock happens on the other machine and names *this* one, because
		// scope is per peer (D-30). Saying it the other way round sends a user
		// to the wrong terminal to type the wrong thing.
		fmt.Fprintf(stdout, "on %s, run `openair unlock %s` -- once per six hours\n",
			deviceLabel(target), shortID(string(selfID(ctx, c))))
	default:
		fmt.Fprintf(stdout, "%s is trusted: paired, but not able to act unattended\n", deviceLabel(target))
	}
	if policy == identity.PolicyNever {
		fmt.Fprintln(stdout, "recorded as always-on; its unlock will not expire on a timer")
	}
	return nil
}

// parseInterleaved parses flags that appear before *and* after the positional
// argument.
//
// Go's flag package stops at the first non-flag word, so `openair trust laptop
// --owned` would otherwise see no flags at all and fail with a usage error --
// while `openair trust --owned laptop` worked. Both orders are things people
// type, and one of them silently meaning something else is worse than either.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
}

// --- helpers -----------------------------------------------------------------

// resolveDevice finds one paired device by name, fingerprint or DeviceID
// prefix. Unlock and promotion both name a single device, and getting the wrong
// one is not a mistake a user should be able to make silently.
func resolveDevice(ctx context.Context, c *daemon.Client, want string) (*openairv1.DaemonDevice, error) {
	devices, err := c.Devices(ctx, true)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.ReplaceAll(want, "-", ""))

	var hits []*openairv1.DaemonDevice
	for _, dev := range devices {
		id := strings.ToLower(dev.GetDeviceId())
		name := strings.ToLower(dev.GetDisplayName())
		if id == needle || name == strings.ToLower(want) || strings.HasPrefix(id, needle) {
			hits = append(hits, dev)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return nil, fmt.Errorf("no paired device matches %q; `openair devices` lists them", want)
	default:
		names := make([]string, 0, len(hits))
		for _, h := range hits {
			names = append(names, deviceLabel(h))
		}
		return nil, fmt.Errorf("%q matches %s", want, strings.Join(names, ", "))
	}
}

// selfID asks the daemon which device this is, for a message that has to name
// the local machine rather than the one being changed.
func selfID(ctx context.Context, c *daemon.Client) identity.DeviceID {
	st, err := c.Status(ctx)
	if err != nil {
		return ""
	}
	return identity.DeviceID(st.GetDeviceId())
}

func deviceLabel(d *openairv1.DaemonDevice) string {
	if n := d.GetDisplayName(); n != "" {
		return fmt.Sprintf("%s (%s)", n, shortID(d.GetDeviceId()))
	}
	return shortID(d.GetDeviceId())
}

func shortID(id string) string { return fingerprint(identity.DeviceID(id)) }

// readPassphrase reads without echoing when standard input is a terminal.
//
// The fallback matters as much as the prompt: a passphrase typed into a pipe or
// a CI job has to work, and silently requiring a TTY would make this command
// unusable from a script for no security gain -- the credential is on the local
// socket a moment later either way.
func readPassphrase(stdin io.Reader, stdout io.Writer, fromStdin bool, prompt string, confirm bool) ([]byte, error) {
	if fromStdin {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		return []byte(strings.TrimRight(line, "\r\n")), nil
	}

	f, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return nil, errors.New("standard input is not a terminal; pass --passphrase-stdin to read it from a pipe")
	}

	fmt.Fprint(stdout, prompt)
	pass, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(stdout)
	if err != nil {
		return nil, err
	}
	if !confirm {
		return pass, nil
	}

	fmt.Fprint(stdout, "Again: ")
	again, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(stdout)
	if err != nil {
		return nil, err
	}
	if string(pass) != string(again) {
		return nil, errors.New("the two passphrases differ")
	}
	return pass, nil
}

func tierName(t identity.ProtectionTier) string {
	switch t {
	case identity.TierKeystore:
		return "tier 1, platform keystore"
	case identity.TierPassphrase:
		return "tier 2, passphrase"
	default:
		return "tier 3, unprotected"
	}
}

// daemonRestartHint says the one thing a user would otherwise discover by
// failing: a running daemon read the old state at startup.
func daemonRestartHint(stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := daemon.Connect(ctx, "", nil, nil)
	if err != nil {
		return nil // no daemon running; nothing to restart
	}
	defer c.Close()
	fmt.Fprintf(stdout, "\nRestart openaird so it picks the key up.\n")
	return nil
}
