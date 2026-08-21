//go:build windows

package wsl

import (
	"context"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// The only part of WSL support that actually touches the machine. Everything
// interesting lives in wsl.go as pure functions; this file is deliberately thin
// because it is the one piece that cannot be unit-tested off Windows.

const distroListTimeout = 5 * time.Second

var (
	availabilityOnce sync.Once
	availabilityPath string
	capsOnce         sync.Once
	cachedCaps       Caps
	knownOnce        sync.Once
	cachedKnown      []string
)

// Executable returns the path to wsl.exe, or "" when WSL is not installed.
// The lookup is cached: it cannot change while the app is running, and it sits
// on the path of every command a WSL project runs.
// probeCommand builds a wsl.exe probe with its console suppressed.
//
// Wails links the GUI binary with -H windowsgui, so the app owns no console and
// every child allocates one. apply() hides the commands it routes, but these
// probes build their own and would each flash a black window — Detect calls
// HostCaps and KnownDistros, and GetWSLStatus calls States three times, on a
// path that runs whenever the add-project dialog opens.
func probeCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	hideConsoleWindow(cmd)
	return cmd
}

func Executable() string {
	availabilityOnce.Do(func() {
		if path, err := exec.LookPath("wsl.exe"); err == nil {
			availabilityPath = path
		}
	})
	return availabilityPath
}

// Available reports whether wsl.exe can be found at all.
func Available() bool { return Executable() != "" }

// ListDistros runs `wsl.exe --list --verbose` and parses the result.
//
// A non-zero exit is normal when no distro is installed, so the error is only
// returned when wsl.exe itself is missing; otherwise an empty list is the
// honest answer.
func ListDistros(ctx context.Context) ([]DistroState, error) {
	binary := Executable()
	if binary == "" {
		return nil, ErrUnavailable
	}
	runCtx, cancel := context.WithTimeout(ctx, distroListTimeout)
	defer cancel()
	// Output is UTF-16LE, which DecodeConsoleOutput handles.
	raw, err := probeCommand(runCtx, binary, "--list", "--verbose").Output()
	if err != nil && len(raw) == 0 {
		return nil, nil
	}
	return ParseDistroList(DecodeConsoleOutput(raw)), nil
}

// Caps probes what this wsl.exe supports. Cached: it cannot change while the
// app runs, and it sits on the path of every command a WSL project executes.
//
// A probe failure yields SupportsCD=false, which selects the shell-cd fallback
// — the form that works on every version.
func HostCaps() Caps {
	capsOnce.Do(func() {
		binary := Executable()
		if binary == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), distroListTimeout)
		defer cancel()
		raw, err := probeCommand(ctx, binary, "--help").CombinedOutput()
		if err != nil && len(raw) == 0 {
			return
		}
		cachedCaps.SupportsCD = ParseSupportsCD(DecodeConsoleOutput(raw))
	})
	return cachedCaps
}

// KnownDistros lists registered distro names, used to reconcile the spelling in
// a UNC path against the spelling wsl.exe expects.
func KnownDistros() []string {
	knownOnce.Do(func() {
		binary := Executable()
		if binary == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), distroListTimeout)
		defer cancel()
		raw, err := probeCommand(ctx, binary, "--list", "--quiet").Output()
		if err != nil && len(raw) == 0 {
			return
		}
		cachedKnown = ParseDistroNames(DecodeConsoleOutput(raw))
	})
	return cachedKnown
}

// States merges the three listings into one view for the UI.
func States(ctx context.Context) []DistroState {
	binary := Executable()
	if binary == "" {
		return nil
	}
	run := func(args ...string) string {
		runCtx, cancel := context.WithTimeout(ctx, distroListTimeout)
		defer cancel()
		raw, err := probeCommand(runCtx, binary, args...).Output()
		if err != nil && len(raw) == 0 {
			return ""
		}
		return DecodeConsoleOutput(raw)
	}
	all := ParseDistroNames(run("--list", "--quiet"))
	running := ParseDistroNames(run("--list", "--quiet", "--running"))
	verbose := ParseDistroList(run("--list", "--verbose"))
	return MergeDistroStates(all, running, verbose)
}

// DetectFor is the one call feature code makes. Everything it depends on is
// cached, so it is cheap enough to sit on every command.
func DetectFor(dir string) Target {
	return Detect(runtime.GOOS, Executable(), dir, HostCaps(), KnownDistros())
}
