package discovery

import (
	"net"
	"strconv"
	"strings"
)

// IsAddr reports whether what the user typed is an address rather than a device
// name.
//
// "laptop:9000" is an address; a bare DeviceID or display name never contains a
// colon followed by a number, so the distinction needs no flag from the user.
func IsAddr(s string) bool {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return false
	}
	if _, err := strconv.Atoi(port); err != nil {
		return false
	}
	return host != "" || strings.HasPrefix(s, ":")
}

// Match finds the candidates a user could have meant by target: an exact
// DeviceID, a DeviceID prefix of at least four characters, or a display name
// compared case-insensitively.
//
// A prefix counts because the fingerprint is printed in groups of four and
// nobody wants to type sixteen characters. Hyphens are ignored for the same
// reason -- what is printed should be typeable back.
//
// Returning every match rather than the best one is deliberate: two devices
// answering to the same name is a question for the user, and callers are
// expected to refuse rather than guess. The wrong device silently receiving
// your file is worse than being asked to be specific.
func Match(peers []PeerCandidate, target string) []PeerCandidate {
	want := strings.ToLower(strings.ReplaceAll(target, "-", ""))

	var out []PeerCandidate
	for _, p := range peers {
		id := strings.ToLower(string(p.DeviceID))
		switch {
		case id == want,
			len(want) >= 4 && strings.HasPrefix(id, want),
			p.DisplayName != "" && strings.EqualFold(p.DisplayName, target):
			out = append(out, p)
		}
	}
	return out
}
