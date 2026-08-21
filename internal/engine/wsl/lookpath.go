package wsl

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Availability has to be asked where the command will actually run.
//
// WSL interop appends the WINDOWS PATH into the distro, never the reverse, so a
// tool the user installed with apt inside their distro is invisible to
// exec.LookPath on the host. A host-side check therefore reports "gh is not
// installed" for exactly the projects whose gh is sitting right next to the
// repo — and it reports it before any routing can happen, which makes the
// routing itself unreachable.

// lookPathProbeTimeout bounds the probe. Starting a stopped distro is the slow
// case; everything after that is a shell builtin.
const lookPathProbeTimeout = 20 * time.Second

// lookPathScript is the probe body, kept separate so the quoting is testable
// without a distro.
func lookPathScript(name string) string {
	return "command -v -- " + ShellQuote(name) + " >/dev/null 2>&1"
}

// LookPathFor reports nil when name can be executed for this target.
//
// For a host target it is exactly exec.LookPath, so every non-Windows build
// behaves as before — Detect never yields a WSL target off Windows.
func LookPathFor(ctx context.Context, t Target, name string) error {
	if !t.IsWSL() {
		_, err := exec.LookPath(name)
		return err
	}

	probeCtx, cancel := context.WithTimeout(ctx, lookPathProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, t.Exe)
	if !ApplyShell(cmd, t, lookPathScript(name), nil) {
		// Unreachable while IsWSL is true, but failing closed beats running a
		// bare wsl.exe with no arguments.
		return fmt.Errorf("could not build a %s probe for distro %q", name, t.Distro)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s not found in WSL distribution %q: %w", name, t.Distro, err)
	}
	return nil
}

// MissingCommandHint explains where the command was looked for, so the user is
// not told to install something they already have on the other side of the
// boundary.
func MissingCommandHint(t Target, name, hostAdvice string) string {
	if !t.IsWSL() {
		return hostAdvice
	}
	return fmt.Sprintf("%s is not installed in WSL distribution %q. This project's files live inside "+
		"that distribution, so %s has to be installed there — a copy on Windows is not reachable from it.",
		name, t.Distro, name)
}
