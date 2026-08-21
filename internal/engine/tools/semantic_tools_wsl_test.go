package tools

import (
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/wsl"
)

// gopls runs inside the distro for a WSL project, and a UNC path names nothing
// there — `\\wsl.localhost\Ubuntu\home\me\p\main.go:12:3` would simply not
// resolve, so go_to_definition and find_references would fail on every symbol
// in every WSL project while looking like an ordinary "not found".
func TestGoplsPositionUsesTheDistroSpelling(t *testing.T) {
	target := wsl.Detect("windows", `C:\Windows\System32\wsl.exe`,
		`\\wsl.localhost\Ubuntu\home\me\p`, wsl.Caps{SupportsCD: true}, []string{"Ubuntu"})
	if !target.IsWSL() {
		t.Fatal("fixture did not produce a WSL target")
	}

	got := goplsPosition(target, `\\wsl.localhost\Ubuntu\home\me\p\main.go`, 12, 3)
	if want := "/home/me/p/main.go:12:3"; got != want {
		t.Fatalf("goplsPosition = %q, want %q", got, want)
	}

	// A path in a different distro is not reachable from this one, so it must be
	// left alone rather than silently rewritten into this distro's namespace.
	other := `\\wsl.localhost\Debian\srv\x.go`
	if got := goplsPosition(target, other, 1, 1); got != other+":1:1" {
		t.Fatalf("a path from another distro was rewritten: %q", got)
	}

	// A relative path resolves against the translated working directory.
	if got := goplsPosition(target, "pkg/x.go", 4, 0); got != "pkg/x.go:4:1" {
		t.Fatalf("relative path = %q; also check the column clamp", got)
	}
}

// The host target is what every non-Windows build produces, and it must leave
// the position exactly as the caller built it.
func TestGoplsPositionIsUnchangedForAHostTarget(t *testing.T) {
	for _, file := range []string{"/home/me/p/main.go", `C:\Users\me\p\main.go`, "pkg/x.go"} {
		if got, want := goplsPosition(wsl.Target{}, file, 7, 2), file+":7:2"; got != want {
			t.Fatalf("goplsPosition(%q) = %q, want %q", file, got, want)
		}
	}
	// The column clamp is pre-existing behaviour and must survive.
	if got := goplsPosition(wsl.Target{}, "a.go", 1, 0); got != "a.go:1:1" {
		t.Fatalf("column clamp lost: %q", got)
	}
}
