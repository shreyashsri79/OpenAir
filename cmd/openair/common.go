package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shreyashsri79/openair/internal/identity"
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
