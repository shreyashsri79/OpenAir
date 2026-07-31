package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/pairing"
)

// defaultKeyDir is where this device's keys live when --keys is not given.
func defaultKeyDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "openair")
	}
	return ".openair"
}

// platform reports this device's platform string for Hello (PROTOCOL.md §4,
// which enumerates exactly these four values).
func platform() string {
	switch runtime.GOOS {
	case "linux", "windows", "android", "darwin":
		return runtime.GOOS
	default:
		// §4 does not define a value for anything else. Reporting the GOOS
		// would put an unlisted string on the wire; "linux" is the closest
		// truthful answer for the BSDs, which is where this lands in practice.
		return "linux"
	}
}

// loadIdentity opens or creates this device's keys.
//
// Tier is TierNone: M1 has no unlock flow (M6) and therefore nothing that could
// ever use a privilege key, and generating one we cannot protect would put a
// tier on the wire that overstates what this device actually offers (D-21).
func loadIdentity(dir string) (*identity.FileIdentity, error) {
	if dir == "" {
		dir = defaultKeyDir()
	}
	return identity.LoadOrCreate(identity.Options{Dir: dir, Tier: identity.TierNone})
}

// trustStoreName is the trust store's filename inside the key directory. The
// two live together because they are the same secret in two halves: the keys
// this device holds, and the keys it has decided to believe.
const trustStoreName = "trust.json"

// loadTrustStore opens the trust store beside this device's keys, creating an
// empty one on first run.
func loadTrustStore(dir string) (*identity.FileTrustStore, error) {
	if dir == "" {
		dir = defaultKeyDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}
	return identity.OpenTrustStore(filepath.Join(dir, trustStoreName))
}

// newPairingHandler builds the capID 0 handler that owns pairing, revocation
// and grants, with a Confirm that shows the short authentication string on this
// terminal.
//
// There is no --yes here and there must not be: PROTOCOL.md §5.2 forbids a
// skip-verification path, because the digits are the only thing that detects a
// man in the middle during pairing. --yes elsewhere in this CLI answers "is
// this the device I meant", which the trust store answers afterwards; this
// question has no safe default.
func newPairingHandler(local identity.Identity, store identity.TrustStore, in io.Reader, out io.Writer) (*pairing.Handler, error) {
	return pairing.NewHandler(pairing.Config{
		Local:       local,
		Store:       store,
		DisplayName: hostname(),
		Platform:    platform(),
		Confirm: func(_ context.Context, sas string, peer pairing.PeerInfo) (bool, error) {
			fmt.Fprintf(out, "\npairing with %s\n", fingerprint(peer.DeviceID))
			if peer.DisplayName != "" {
				fmt.Fprintf(out, "  name: %s  platform: %s\n", peer.DisplayName, peer.Platform)
			}
			fmt.Fprintf(out, "\n  %s\n\n", pairing.FormatSAS(sas))
			return confirm(in, out, "do both devices show exactly these six digits?"), nil
		},
	})
}

// requirePaired is the sending side of M2's rule: a transfer only goes to a
// device this one has paired with. The receiving side enforces the same thing
// in session.Config.Authorize before any capability message is dispatched; this
// is the check the dialler makes for itself, after the handshake has told it
// whose key answered.
func requirePaired(h *pairing.Handler, peer identity.Peer) error {
	if err := h.Authorize(peer); err != nil {
		if errors.Is(err, identity.ErrKeyMismatch) {
			return fmt.Errorf("%w\nthe device at that address is not the one you paired; re-pair with `openair pair`", err)
		}
		return fmt.Errorf("%s is not paired with this device; run `openair pair` on both ends first",
			fingerprint(peer.DeviceID))
	}
	return nil
}

// fingerprint formats a DeviceID for a human to read aloud or compare on a
// screen. The DeviceID is already the truncated hash of the identity key
// (PROTOCOL.md §2); grouping only makes it checkable without losing your place.
func fingerprint(id identity.DeviceID) string {
	s := string(id)
	var b strings.Builder
	for i := 0; i < len(s); i += 4 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(s[i:min(i+4, len(s))])
	}
	return b.String()
}

// confirm prints a prompt and reads a yes/no answer. Anything other than an
// explicit yes is no: at this milestone the fingerprint comparison is the only
// thing standing between the user and an arbitrary peer, so a bare Enter must
// not mean "go ahead".
func confirm(in io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprintf(out, "%s [y/N]: ", prompt)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// humanBytes formats a byte count for progress output.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
