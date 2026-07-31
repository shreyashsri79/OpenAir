package remotefs

import (
	"errors"
	"fmt"
	"strings"
)

// Wire paths, §11.1.
//
// §11 never says what a path in a remotefs request looks like, which it has to
// answer somewhere: the source exposes roots rather than a filesystem, so
// "/etc/passwd" is not a thing a client can ask for even in principle. The form
// used here is `root/sub/path` — the first component names one of the
// configured roots, the rest is relative to it, always forward slashes — and
// the empty path is the list of roots themselves (D-72).
//
// The empty path matters more than it looks. Without it a client cannot
// discover what it may browse, and every UI would have to be told the share
// names out of band, for a capability whose entire purpose is browsing.

// ErrUnsafePath is a path that could escape a root, rejected on syntax before
// any filesystem call. The rules are internal/caps/files' rules (§8.1), which
// §11.1 explicitly adopts.
var ErrUnsafePath = errors.New("remotefs: unsafe path")

// isRootPath reports the "list the shares" request: empty, "/" or ".".
func isRootPath(p string) bool {
	switch strings.Trim(p, "/") {
	case "", ".":
		return true
	}
	return false
}

// cleanWirePath normalises a path for echoing back in a FileStat, so that a
// client comparing what it asked for against what it got sees the same string.
func cleanWirePath(p string) string {
	return strings.Trim(p, "/")
}

// safeWirePath validates a wire path and returns it normalised.
//
// Syntactic and run before touching the filesystem, so a symlink swapped
// between check and open cannot turn a validated path into an escaping one.
// Backslash is rejected outright for the reason files' safeRel gives: it is a
// legal filename byte on Linux and a separator on Windows, so accepting it
// would make one request mean two different things on the two first-class
// platforms.
func safeWirePath(p string) (string, error) {
	rel := cleanWirePath(p)
	if rel == "" {
		return "", fmt.Errorf("%w: empty", ErrUnsafePath)
	}
	if strings.ContainsRune(rel, '\\') {
		return "", fmt.Errorf("%w: %q contains a backslash", ErrUnsafePath, p)
	}
	if strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("%w: %q contains NUL", ErrUnsafePath, p)
	}
	for _, e := range strings.Split(rel, "/") {
		switch e {
		case "":
			return "", fmt.Errorf("%w: %q has an empty component", ErrUnsafePath, p)
		case ".", "..":
			return "", fmt.Errorf("%w: %q contains %q", ErrUnsafePath, p, e)
		}
		if strings.ContainsRune(e, ':') {
			// "C:/x" is absolute on Windows and a relative name on Linux.
			return "", fmt.Errorf("%w: %q contains a volume separator", ErrUnsafePath, p)
		}
	}
	return rel, nil
}
