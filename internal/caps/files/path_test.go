package files

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// Path traversal is the obvious attack on a file transfer protocol, and §8.1
// puts the check on the receiver rather than in the UI. These are the offers
// that check exists to refuse.
func TestSafeRelRejectsTraversal(t *testing.T) {
	bad := []string{
		"",
		"/etc/passwd",
		"..",
		"../etc/passwd",
		"a/../../etc/passwd",
		"a/b/../../../x",
		"./a",
		"a/./b",
		"a//b",
		"a/",
		`..\..\windows\system32\x`,
		`a\b`,
		"C:/windows/x",
		"C:x",
		"a/C:/b",
		"a\x00b",
	}
	for _, p := range bad {
		if got, err := safeRel(p); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("safeRel(%q) = %q, %v; want ErrUnsafePath", p, got, err)
		}
	}
}

func TestSafeRelAcceptsOrdinaryPaths(t *testing.T) {
	good := []string{
		"a",
		"a/b/c.txt",
		"dir with spaces/file.name.ext",
		"..hidden",
		"a..b/c",
		"unicode/ファイル.txt",
	}
	for _, p := range good {
		got, err := safeRel(p)
		if err != nil {
			t.Errorf("safeRel(%q): %v", p, err)
			continue
		}
		if got != p {
			t.Errorf("safeRel(%q) = %q, want it unchanged", p, got)
		}
	}
}

func TestResolveStaysInsideRoot(t *testing.T) {
	root := t.TempDir()

	got, err := resolve(root, "sub/dir/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "sub", "dir", "file.bin")
	if got != want {
		t.Errorf("resolve = %q, want %q", got, want)
	}

	for _, p := range []string{"../escape", "a/../../escape", "/etc/passwd"} {
		if got, err := resolve(root, p); err == nil {
			t.Errorf("resolve(%q) = %q, want rejection", p, got)
		}
	}
}

// A sibling directory whose name merely starts with the root's name must not
// count as inside it: /tmp/rootevil is not under /tmp/root.
func TestResolvePrefixIsNotContainment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	got, err := resolve(root, "x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, root+string(filepath.Separator)) {
		t.Fatalf("resolve produced %q, which is not under %q", got, root)
	}
}
