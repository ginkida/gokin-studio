package tools

import (
	"strings"
	"testing"
)

// `pwd -P` inside a distro reports a Linux path. On Windows filepath.Clean turns
// /home/me/api into \home\me\api, os.Stat fails, and the session working
// directory silently never advances after a cd — no error anywhere.
func TestHostPathForDetectedPWDRebuildsUNC(t *testing.T) {
	cases := []struct {
		root, pwd, want string
	}{
		{`\\wsl.localhost\Ubuntu\home\me\p`, "/home/me/p/sub", `\\wsl.localhost\Ubuntu\home\me\p\sub`},
		// The legacy spelling must round-trip as itself; rewriting it would make
		// the path straddle two VolumeNames in later comparisons.
		{`\\wsl$\Ubuntu\home\me\p`, "/home/me/p/sub", `\\wsl$\Ubuntu\home\me\p\sub`},
		{`\\wsl.localhost\Ubuntu\home\me\p`, "/", `\\wsl.localhost\Ubuntu`},
		// A path outside the workspace is still translated; the boundary check
		// is a separate concern and must see a comparable spelling.
		{`\\wsl.localhost\Ubuntu\home\me\p`, "/etc", `\\wsl.localhost\Ubuntu\etc`},
	}
	for _, tc := range cases {
		got, rebuilt := hostPathForDetectedPWD(tc.root, tc.pwd)
		if !rebuilt {
			t.Fatalf("hostPathForDetectedPWD(%q, %q) did not rebuild", tc.root, tc.pwd)
		}
		if got != tc.want {
			t.Fatalf("hostPathForDetectedPWD(%q, %q) = %q, want %q", tc.root, tc.pwd, got, tc.want)
		}
	}
}

// The regression that matters: a local project must take exactly today's path.
func TestHostPathForDetectedPWDLeavesLocalRootUntouched(t *testing.T) {
	for _, tc := range []struct{ root, pwd string }{
		{"/home/me/project", "/home/me/project/sub"},
		{`C:\Users\me\project`, `C:\Users\me\project\sub`},
		{`C:\Users\me\project`, "/home/me/elsewhere"},
		{"", "/home/me/x"},
	} {
		got, rebuilt := hostPathForDetectedPWD(tc.root, tc.pwd)
		if rebuilt {
			t.Fatalf("hostPathForDetectedPWD(%q, %q) claimed a rebuild", tc.root, tc.pwd)
		}
		if got != tc.pwd {
			t.Fatalf("hostPathForDetectedPWD(%q, %q) = %q, want the input unchanged", tc.root, tc.pwd, got)
		}
	}
}

func TestHostPathForDetectedPWDIgnoresNonAbsolutePWD(t *testing.T) {
	for _, pwd := range []string{"", "relative/dir", `C:\Users\me`} {
		got, rebuilt := hostPathForDetectedPWD(`\\wsl.localhost\Ubuntu\home\me`, pwd)
		if rebuilt || got != pwd {
			t.Fatalf("hostPathForDetectedPWD(.., %q) = %q, %v", pwd, got, rebuilt)
		}
	}
}

// The in-shell assertion compares against `pwd -P`, which inside a distro is a
// Linux path. Interpolating the Windows spelling would make every cd read as a
// boundary violation.
func TestWorkspaceRootForShellSpeaksLinuxOnlyForWSL(t *testing.T) {
	if got := workspaceRootForShell(`\\wsl.localhost\Ubuntu\home\me\p`); got != "/home/me/p" {
		t.Fatalf("workspaceRootForShell(WSL) = %q", got)
	}
	if got := workspaceRootForShell(`\\wsl$\Ubuntu\srv`); got != "/srv" {
		t.Fatalf("workspaceRootForShell(legacy WSL) = %q", got)
	}
	for _, root := range []string{"/home/me/project", `C:\Users\me\project`, ""} {
		if got := workspaceRootForShell(root); got != root {
			t.Fatalf("workspaceRootForShell(%q) = %q, want it unchanged", root, got)
		}
	}
}

// Byte-for-byte: a local workspace root must produce exactly the script it
// produced before this change.
func TestWrapManagedWorkspaceCommandUnchangedForLocalRoot(t *testing.T) {
	tool := &BashTool{workspaceRoot: "/home/me/project"}
	script := tool.wrapManagedWorkspaceCommand("echo hi")
	if !strings.HasPrefix(script, "workspace_root='/home/me/project'\n") {
		t.Fatalf("local root was rewritten:\n%s", script[:120])
	}
	if strings.Contains(script, "wsl") {
		t.Fatalf("WSL vocabulary leaked into a local script:\n%s", script[:200])
	}
}

func TestWrapManagedWorkspaceCommandUsesLinuxRootForWSL(t *testing.T) {
	tool := &BashTool{workspaceRoot: `\\wsl.localhost\Ubuntu\home\me\project`}
	script := tool.wrapManagedWorkspaceCommand("echo hi")
	if !strings.HasPrefix(script, "workspace_root='/home/me/project'\n") {
		t.Fatalf("the shell assertion did not get the Linux root:\n%s", script[:160])
	}
	// A Windows spelling inside the script would never match `pwd -P`.
	if strings.Contains(script, `wsl.localhost`) {
		t.Fatalf("a UNC path reached the in-shell comparison:\n%s", script[:200])
	}
}
