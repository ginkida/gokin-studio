package wsl

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// The probe runs inside a shell, so a command name has to be quoted rather than
// pasted. Nothing user-supplied reaches it today, but the function takes a
// string and the next caller may not be so careful.
func TestLookPathScriptQuotesTheName(t *testing.T) {
	if got, want := lookPathScript("gh"), "command -v -- 'gh' >/dev/null 2>&1"; got != want {
		t.Fatalf("lookPathScript = %q, want %q", got, want)
	}
	script := lookPathScript("gh; rm -rf /")
	if strings.Contains(script, "; rm -rf /'") == false && strings.Contains(script, "rm -rf") {
		// The dangerous text must survive only INSIDE the quotes.
		if !strings.HasPrefix(script, "command -v -- '") {
			t.Fatalf("the name escaped its quoting: %q", script)
		}
	}
	if strings.Count(script, "'") < 2 {
		t.Fatalf("the name was not quoted at all: %q", script)
	}
}

// For a host target this must be exactly exec.LookPath — that is what keeps
// every non-Windows build behaving as it did before the check became
// target-aware.
func TestLookPathForMatchesExecLookPathOnAHostTarget(t *testing.T) {
	for _, name := range []string{"go", "definitely-not-a-real-binary-xyzzy"} {
		_, want := exec.LookPath(name)
		got := LookPathFor(context.Background(), Target{}, name)
		if (got == nil) != (want == nil) {
			t.Fatalf("LookPathFor(%q) = %v, exec.LookPath = %v", name, got, want)
		}
	}
}

// A host-only probe reports "gh is not installed" for exactly the projects whose
// gh sits next to the repo inside the distro — WSL interop pushes the Windows
// PATH in, never the distro's PATH out. The message has to say which side was
// searched, or the user reinstalls gh on the wrong one.
func TestMissingCommandHintNamesTheDistro(t *testing.T) {
	const hostAdvice = "gh CLI is not installed. Install it from https://cli.github.com/"

	if got := MissingCommandHint(Target{}, "gh", hostAdvice); got != hostAdvice {
		t.Fatalf("a host target must keep the original advice, got %q", got)
	}

	target := Detect("windows", `C:\Windows\System32\wsl.exe`,
		`\\wsl.localhost\Ubuntu\home\me\p`, Caps{SupportsCD: true}, []string{"Ubuntu"})
	if !target.IsWSL() {
		t.Fatal("fixture did not produce a WSL target")
	}
	got := MissingCommandHint(target, "gh", hostAdvice)
	if !strings.Contains(got, "Ubuntu") {
		t.Fatalf("the message does not name the distribution: %q", got)
	}
	if strings.Contains(got, "cli.github.com") {
		t.Fatalf("the message still sends the user to the Windows installer: %q", got)
	}
}
