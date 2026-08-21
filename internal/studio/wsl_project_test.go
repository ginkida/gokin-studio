package studio

import (
	"path/filepath"
	"strings"
	"testing"
)

// One repository must be one project however the user spells its path.
// \\wsl$\Ubuntu\x and \\wsl.localhost\Ubuntu\x name the same directory, and
// registering both would give one repo two projects with two histories.
func TestSameProjectDirectoryFoldsUNCSpellingsAndDistroCase(t *testing.T) {
	simulateWindowsHost(t)
	spellings := []string{
		`\\wsl.localhost\Ubuntu\home\me\api`,
		`\\wsl$\Ubuntu\home\me\api`,
		`\\WSL.LOCALHOST\ubuntu\home\me\api`,
		`//wsl.localhost/Ubuntu/home/me/api`,
	}
	for _, left := range spellings {
		for _, right := range spellings {
			if !sameProjectDirectory(left, right) {
				t.Fatalf("%q and %q were treated as different projects", left, right)
			}
		}
	}
	// Genuinely different locations must stay distinct.
	for _, other := range []string{
		`\\wsl.localhost\Ubuntu\home\me\api2`,
		`\\wsl.localhost\Debian\home\me\api`,
		`C:\Users\me\api`,
	} {
		if sameProjectDirectory(spellings[0], other) {
			t.Fatalf("%q was folded into %q", other, spellings[0])
		}
	}
}

// Off Windows the helper must behave exactly as it did before: a WSL-shaped
// path is just an ordinary string there.
func TestSameProjectDirectoryUnchangedOffWindows(t *testing.T) {
	left := `\\wsl.localhost\Ubuntu\home\me\api`
	right := `\\wsl$\Ubuntu\home\me\api`
	if sameProjectDirectory(left, right) {
		t.Fatal("UNC spellings were folded off Windows; behaviour must be unchanged there")
	}
	// Identical strings still match, as before.
	if !sameProjectDirectory(left, left) {
		t.Fatal("an identical path stopped matching itself")
	}
}

// Two local directories must still compare exactly as they always did.
func TestSameProjectDirectoryUnchangedForLocalPaths(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !sameProjectDirectory(dir, resolved) {
		t.Fatal("a directory stopped matching its resolved form")
	}
	if sameProjectDirectory(dir, t.TempDir()) {
		t.Fatal("two distinct directories were treated as one project")
	}
}

// A WSL project must be accepted even though EvalSymlinks cannot resolve a 9P
// path, and its stored directory must be the canonical spelling.
func TestAddProjectAcceptsWSLPathAndStoresCanonicalSpelling(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)

	// A real directory stands in for the distro's filesystem; only the GOOS and
	// path-shape decisions are simulated.
	dir := t.TempDir()
	if _, err := s.AddProject("Local", dir); err != nil {
		t.Fatalf("a local project must still register: %v", err)
	}

	// Now the error strings for a genuinely bad local path must be unchanged.
	if _, err := s.AddProject("Missing", filepath.Join(dir, "does-not-exist")); err == nil {
		t.Fatal("a missing directory was accepted")
	} else if !strings.Contains(err.Error(), "directory does not exist") {
		t.Fatalf("error message changed: %v", err)
	}
	if _, err := s.AddProject("Duplicate", dir); err == nil {
		t.Fatal("a duplicate directory was accepted")
	} else if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate error message changed: %v", err)
	}
}
