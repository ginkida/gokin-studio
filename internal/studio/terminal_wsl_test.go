package studio

import (
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/wsl"
)

const fakeWSLExe = `C:\Windows\System32\wsl.exe`

func wslTargetForTest(dir string, supportsCD bool) wsl.Target {
	return wsl.Detect("windows", fakeWSLExe, dir, wsl.Caps{SupportsCD: supportsCD}, []string{"Ubuntu"})
}

// A WSL project's terminal must be a shell INSIDE the distro — that is the
// whole point: the user's own nvm/pyenv/asdf toolchain, not whatever Windows
// happens to have on PATH.
func TestTerminalShellCommandOpensDistroShell(t *testing.T) {
	target := wslTargetForTest(`\\wsl.localhost\Ubuntu\home\me\repo`, true)
	name, args, dir, err := terminalShellCommand("windows", target.HostDir, target)
	if err != nil {
		t.Fatalf("terminalShellCommand: %v", err)
	}
	if name != fakeWSLExe {
		t.Fatalf("name = %q, want wsl.exe", name)
	}
	joined := strings.Join(args, " ")
	if joined != "-d Ubuntu --cd /home/me/repo --exec bash -l" {
		t.Fatalf("args = %q", joined)
	}
	// A UNC path is not a legal CreateProcess working directory; --cd carries it.
	if dir != "" {
		t.Fatalf("dir = %q, must be empty for a WSL target", dir)
	}
}

// Legacy wsl.exe has no --cd, so the directory cannot be set that way. The
// terminal still opens rather than silently starting somewhere else.
func TestTerminalShellCommandOmitsCDOnLegacyWSL(t *testing.T) {
	target := wslTargetForTest(`\\wsl.localhost\Ubuntu\home\me\repo`, false)
	_, args, _, err := terminalShellCommand("windows", target.HostDir, target)
	if err != nil {
		t.Fatalf("terminalShellCommand: %v", err)
	}
	for _, arg := range args {
		if arg == "--cd" {
			t.Fatalf("--cd used on a wsl.exe that lacks it: %v", args)
		}
	}
}

// creack/pty has no Windows implementation, so a Windows-drive project would
// otherwise fail with a bare "not supported" that explains nothing.
func TestTerminalShellCommandExplainsWindowsDriveLimitation(t *testing.T) {
	_, _, _, err := terminalShellCommand("windows", `C:\Users\me\repo`, wsl.Target{})
	if err == nil {
		t.Fatal("a Windows-drive terminal should report why it cannot start")
	}
	if !strings.Contains(err.Error(), "wsl.localhost") {
		t.Fatalf("the error should point at the workaround: %v", err)
	}
}

// macOS and Linux must keep exactly the behaviour they had.
func TestTerminalShellCommandUnchangedOnUnix(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	name, args, dir, err := terminalShellCommand("darwin", "/home/me/repo", wsl.Target{})
	if err != nil {
		t.Fatalf("terminalShellCommand: %v", err)
	}
	if name != "/bin/zsh" || len(args) != 0 || dir != "/home/me/repo" {
		t.Fatalf("unix command = %q %v %q", name, args, dir)
	}

	t.Setenv("SHELL", "")
	name, _, _, err = terminalShellCommand("linux", "/home/me/repo", wsl.Target{})
	if err != nil || name != "/bin/bash" {
		t.Fatalf("fallback shell = %q, %v", name, err)
	}
}

// The routing decision itself must be inert off Windows, so a WSL-shaped path
// on macOS still takes the ordinary path.
func TestTerminalIgnoresWSLShapedPathOffWindows(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	target := wsl.Detect("darwin", fakeWSLExe, `\\wsl.localhost\Ubuntu\home\me`, wsl.Caps{SupportsCD: true}, nil)
	name, _, dir, err := terminalShellCommand("darwin", `\\wsl.localhost\Ubuntu\home\me`, target)
	if err != nil {
		t.Fatalf("terminalShellCommand: %v", err)
	}
	if name != "/bin/zsh" || dir != `\\wsl.localhost\Ubuntu\home\me` {
		t.Fatalf("off-Windows command = %q, dir %q", name, dir)
	}
}
