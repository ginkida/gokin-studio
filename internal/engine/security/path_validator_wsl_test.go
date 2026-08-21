package security

import (
	"path/filepath"
	"runtime"
	"testing"
)

// Once commands run inside a distro, every compiler error, stack trace and
// `git status` line the model reads names /home/me/... while the file tools
// still take the UNC spelling. Without translation the model's very next
// read/edit of a path it just saw would fail.
func TestTranslatePrefixPathRewritesLinuxPathsToUNC(t *testing.T) {
	from, to := "/home/me/api", `\\wsl.localhost\Ubuntu\home\me\api`
	cases := map[string]string{
		"/home/me/api":             to,
		"/home/me/api/cmd/main.go": filepath.Join(to, "cmd", "main.go"),
		"/home/me/api/":            filepath.Join(to, ""),
	}
	for in, want := range cases {
		got, ok := TranslatePrefixPath(in, from, to)
		if !ok || got != want {
			t.Fatalf("TranslatePrefixPath(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

// The boundary check is the point: a sibling directory whose name merely starts
// with the workspace name must not be rewritten into the workspace.
func TestTranslatePrefixPathRequiresASegmentBoundary(t *testing.T) {
	from, to := "/home/me/api", `\\wsl.localhost\Ubuntu\home\me\api`
	for _, in := range []string{"/home/me/apiary/x", "/home/me/api2", "/home/me/ap"} {
		got, ok := TranslatePrefixPath(in, from, to)
		if ok || got != in {
			t.Fatalf("TranslatePrefixPath(%q) = %q, %v; it must be left alone", in, got, ok)
		}
	}
}

// A path outside the workspace is left untouched so the existing allowedDirs
// check still rejects it — translation must not become a new hole.
func TestTranslatePrefixPathLeavesOutsidePathsAlone(t *testing.T) {
	from, to := "/home/me/api", `\\wsl.localhost\Ubuntu\home\me\api`
	for _, in := range []string{"/etc/passwd", "/", "relative/path", "", `C:\Users\me`} {
		got, ok := TranslatePrefixPath(in, from, to)
		if ok || got != in {
			t.Fatalf("TranslatePrefixPath(%q) = %q, %v; it must be left alone", in, got, ok)
		}
	}
}

// Disabled translation is the default and must be a pure pass-through.
func TestTranslatePrefixPathIsInertWhenDisabled(t *testing.T) {
	for _, in := range []string{"/home/me/api/x", "/etc/passwd", ""} {
		if got, ok := TranslatePrefixPath(in, "", `\\wsl.localhost\Ubuntu`); ok || got != in {
			t.Fatalf("disabled translation changed %q to %q", in, got)
		}
	}
}

func TestWSLPathTranslationDerivesFromAllowedDirs(t *testing.T) {
	from, to, ok := WSLPathTranslation([]string{
		`C:\Users\me\other`,
		`\\wsl.localhost\Ubuntu\home\me\api`,
	})
	if !ok || from != "/home/me/api" || to != `\\wsl.localhost\Ubuntu\home\me\api` {
		t.Fatalf("WSLPathTranslation = %q, %q, %v", from, to, ok)
	}
	if _, _, ok := WSLPathTranslation([]string{`C:\Users\me`, "/home/me/api"}); ok {
		t.Fatal("a non-WSL allow list produced a translation")
	}
	if _, _, ok := WSLPathTranslation(nil); ok {
		t.Fatal("an empty allow list produced a translation")
	}
}

// wsl.Available() is a constant false off Windows, so a validator built here
// must never carry a translation — this is the byte-identity guarantee for the
// 16 tools that share this validator.
func TestNewPathValidatorHasNoTranslationOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this asserts the off-Windows default")
	}
	validator := NewPathValidator([]string{`\\wsl.localhost\Ubuntu\home\me\api`}, false)
	if validator.fromPrefix != "" {
		t.Fatalf("fromPrefix = %q; translation must be off outside Windows", validator.fromPrefix)
	}
}

// With translation configured, Validate accepts a Linux path that names a file
// inside the workspace, and still rejects one that does not.
func TestValidateAcceptsTranslatedLinuxPathAndStillRejectsOutside(t *testing.T) {
	// macOS puts t.TempDir() under /var, which is a symlink to /private/var, and
	// this validator rejects symlinked components.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	validator := NewPathValidator([]string{root}, false)
	validator.SetPathTranslation("/home/me/api", root)

	got, err := validator.Validate("/home/me/api/cmd/main.go")
	if err != nil {
		t.Fatalf("Validate(translated) = %v", err)
	}
	if want := filepath.Join(root, "cmd", "main.go"); got != want {
		t.Fatalf("Validate = %q, want %q", got, want)
	}
	// Fail-closed: an untranslated absolute path is not inside the workspace.
	if _, err := validator.Validate("/etc/passwd"); err == nil {
		t.Fatal("a path outside the workspace was accepted")
	}
}
