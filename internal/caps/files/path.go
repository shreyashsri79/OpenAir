package files

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrUnsafePath is returned for any offered path that could escape the
// destination root. PROTOCOL.md §8.1 makes this the receiver's job, not the
// UI's -- path traversal is the obvious attack on a file transfer protocol.
var ErrUnsafePath = errors.New("files: unsafe path")

// safeRel validates a wire path: relative, forward slashes, no traversal.
//
// The checks are deliberately syntactic and run before touching the
// filesystem, so a symlink race cannot turn a validated path into an escaping
// one between check and open. Backslash is rejected outright: it is a legal
// filename byte on Linux and a separator on Windows, so accepting it would make
// the same offer mean two different things on the two first-class platforms.
func safeRel(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("%w: empty", ErrUnsafePath)
	}
	if strings.ContainsRune(p, '\\') {
		return "", fmt.Errorf("%w: %q contains a backslash", ErrUnsafePath, p)
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: %q contains NUL", ErrUnsafePath, p)
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: %q is absolute", ErrUnsafePath, p)
	}
	// "C:/x" and "C:x" are absolute on Windows and would be a relative name on
	// Linux. Reject a drive letter anywhere in the first element.
	parts := strings.Split(p, "/")
	for _, e := range parts {
		switch e {
		case "":
			return "", fmt.Errorf("%w: %q has an empty component", ErrUnsafePath, p)
		case ".", "..":
			return "", fmt.Errorf("%w: %q contains %q", ErrUnsafePath, p, e)
		}
		if strings.ContainsRune(e, ':') {
			return "", fmt.Errorf("%w: %q contains a volume separator", ErrUnsafePath, p)
		}
	}
	return strings.Join(parts, "/"), nil
}

// resolve joins a validated wire path onto the destination root and confirms
// the result is still inside it. Both checks run: safeRel catches the syntax,
// this catches anything filepath.Join's Clean might still fold.
func resolve(root, p string) (string, error) {
	rel, err := safeRel(p)
	if err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full := filepath.Join(absRoot, filepath.FromSlash(rel))
	if full != absRoot && !strings.HasPrefix(full, absRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q escapes the destination root", ErrUnsafePath, p)
	}
	return full, nil
}
