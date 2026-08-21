package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realDir resolves the macOS /var -> /private/var symlink so the fixtures below
// compare against the same spelling the validator produces.
func realDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// Go maps only IO_REPARSE_TAG_SYMLINK to ModeSymlink, so an ordinary directory
// reached over the WSL 9P share makes EvalSymlinks fail with ENOTDIR rather
// than ENOENT. AddProject already tolerates that for the project root. Before
// this tolerance existed in the validator, the translation added for WSL
// projects rewrote /home/me/api/main.go into its UNC spelling and the very next
// step rejected it — read, write, edit, glob, grep and list_dir would have
// failed on every file in every WSL project.
func TestUnresolvablePathIsToleratedOnlyForWSL(t *testing.T) {
	tolerated := []string{
		`\\wsl.localhost\Ubuntu\home\me\api`,
		`\\wsl$\Debian\srv`,
		`\\WSL.LOCALHOST\Ubuntu-24.04\home`,
	}
	for _, path := range tolerated {
		if !tolerateUnresolvedPath(path) {
			t.Errorf("%q must be tolerated; every file tool fails on WSL projects otherwise", path)
		}
	}
	rejected := []string{
		`\\fileserver\share\repo`, // an ordinary SMB share is not WSL
		`C:\Users\me\repo`,
		"/home/me/api",
		"",
	}
	for _, path := range rejected {
		if tolerateUnresolvedPath(path) {
			t.Errorf("%q must NOT be tolerated; the tolerance is scoped to the 9P quirk", path)
		}
	}
}

// The hatch hands the UNRESOLVED path to a lexical containment check, so it
// must never fire when the path contains a link. That is not a hypothetical
// pairing: Go maps a WSL Linux symlink to ModeIrregular (only
// IO_REPARSE_TAG_SYMLINK becomes ModeSymlink) and withholds ModeDir from name
// surrogates, which is precisely what makes EvalSymlinks return ENOTDIR. So the
// ordinary-directory failure the hatch exists for and a `escape -> /` symlink
// escape arrive as the SAME error, and only the component scan separates them.
func TestToleranceRefusesAnyPathThatContainsALink(t *testing.T) {
	for _, tc := range []struct {
		name string
		scan linkScan
		err  error
		want bool
	}{
		{"clean path", linkScan{}, nil, true},
		{"a real symlink component", linkScan{symlink: `\\wsl.localhost\Ubuntu\p\escape`}, nil, false},
		{"an LX symlink seen as an unclassified reparse point",
			linkScan{reparse: `\\wsl.localhost\Ubuntu\p\escape`}, nil, false},
		{"both", linkScan{symlink: "a", reparse: "b"}, nil, false},
		{"the scan itself failed", linkScan{}, os.ErrPermission, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unresolvedPathIsTolerable(tc.scan, tc.err); got != tc.want {
				t.Fatalf("unresolvedPathIsTolerable(%+v, %v) = %v, want %v", tc.scan, tc.err, got, tc.want)
			}
		})
	}
}

// The scan is what the decision above rests on, so prove it actually reports a
// link rather than always returning an empty result.
func TestScanPathLinksReportsTheOutermostLink(t *testing.T) {
	root := realDir(t)
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	scan, err := scanPathLinks(filepath.Join(root, "link", "deep", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if scan.symlink != filepath.Join(root, "link") {
		t.Fatalf("symlink = %q, want the link component", scan.symlink)
	}
	if !scan.found() {
		t.Fatal("found() disagrees with the recorded symlink")
	}

	clean, err := scanPathLinks(filepath.Join(real, "nothing-here.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if clean.found() {
		t.Fatalf("a link-free path reported %+v", clean)
	}
}

// The other half of that scoping: a genuine resolution failure on a local path
// must still be refused. A file used as a directory component is the cheapest
// way to produce a non-ENOENT EvalSymlinks error on any platform.
func TestValidateStillRejectsAnUnresolvableLocalPath(t *testing.T) {
	root := realDir(t)
	blocker := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	v := NewPathValidator([]string{root}, true)
	if _, err := v.Validate(filepath.Join(blocker, "child.txt")); err == nil {
		t.Fatal("a path through a regular file must not validate")
	} else if !strings.Contains(err.Error(), "resolve symlinks") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// The symlink scan walks upward with filepath.Dir. These lock the behaviour at
// every depth, including the boundary cases the previous split-and-rejoin
// implementation had to special-case: the leaf itself, an ancestor, and a path
// that does not exist yet.
//
// NOTE: the defect this rewrite fixes was Windows-only — the volume segments
// were joined onto the volume root a second time, so no Lstat ever hit a real
// path and the scan found nothing. That cannot be exercised from macOS, where
// VolumeName is always empty; these tests prove the rewrite did not regress the
// platform I can actually run.
func TestSymlinkScanFindsLinksAtEveryDepth(t *testing.T) {
	root := realDir(t)
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(real, "pkg", "file.txt")
	if err := os.WriteFile(leaf, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "ancestor-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(leaf, filepath.Join(real, "pkg", "leaf-link")); err != nil {
		t.Fatal(err)
	}

	v := NewPathValidator([]string{root}, false)
	for _, tc := range []struct {
		name    string
		path    string
		blocked bool
	}{
		{"link is the leaf", filepath.Join(real, "pkg", "leaf-link"), true},
		{"link is an ancestor", filepath.Join(root, "ancestor-link", "pkg", "file.txt"), true},
		{"link is the parent", filepath.Join(root, "ancestor-link", "pkg"), true},
		{"no link anywhere", leaf, false},
		{"file not created yet", filepath.Join(real, "pkg", "new.txt"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := v.checkSymlink(tc.path)
			if tc.blocked && err == nil {
				t.Fatalf("checkSymlink(%q) allowed a symlinked component", tc.path)
			}
			if !tc.blocked && err != nil {
				t.Fatalf("checkSymlink(%q) = %v, want nil", tc.path, err)
			}
		})
	}
}

// The upward walk must stop at the filesystem root rather than spinning, and it
// must not report a link for a path that has none above it.
func TestSymlinkScanTerminatesAtTheRoot(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- NewPathValidator([]string{"/"}, false).checkSymlink(filepath.Clean("/a/b/c/d/e"))
	}()
	select {
	case err := <-done:
		if err != nil && !strings.Contains(err.Error(), "symlinks not allowed") {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-t.Context().Done():
		t.Fatal("checkSymlink did not terminate")
	}
}

// A project registered at the distro root produces fromPrefix "/", which the
// segment-boundary rule would otherwise reject for every path but "/" itself.
func TestTranslationHandlesADistroRootProject(t *testing.T) {
	got, ok := TranslatePrefixPath("/etc/hosts", "/", `\\wsl.localhost\Ubuntu`)
	if !ok {
		t.Fatal("a distro-root project must still translate its paths")
	}
	if want := filepath.Join(`\\wsl.localhost\Ubuntu`, filepath.FromSlash("etc/hosts")); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
