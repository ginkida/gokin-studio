package wsl

import (
	"os"
	"os/exec"
)

// Apply rewrites an already-constructed *exec.Cmd so it runs inside a distro.
//
// Mutating an existing command rather than returning a new one is deliberate:
// call sites have already attached stdout/stderr writers, contexts, WaitDelay
// and process attributes, and rebuilding the command would drop all of that.
//
// Both functions return false for a host target WITHOUT touching the command,
// which is what keeps macOS and Linux on byte-identical code paths.

// ApplyExec routes an exact argument vector into the distro.
func ApplyExec(cmd *exec.Cmd, t Target, argv []string, inject map[string]string) bool {
	plan, ok := RetargetExec(t, argv, inject)
	if !ok {
		return false
	}
	apply(cmd, plan)
	return true
}

// ApplyShell routes a POSIX script into the distro's login shell.
func ApplyShell(cmd *exec.Cmd, t Target, script string, inject map[string]string) bool {
	plan, ok := RetargetShell(t, script, inject)
	if !ok {
		return false
	}
	apply(cmd, plan)
	return true
}

func apply(cmd *exec.Cmd, plan Plan) {
	cmd.Path = plan.Name
	cmd.Args = append([]string{plan.Name}, plan.Args...)
	cmd.Dir = plan.Dir
	// nil Env means "inherit", which wsl.exe needs to start. The overlay then
	// pins WSLENV and blanks the app's own credentials, so the inheritance
	// cannot undo the allowlist the host path relies on.
	cmd.Env = MergeEnv(os.Environ(), plan.EnvOverlay)
	// exec.Command records a lookup failure in cmd.Err, and Start() returns it
	// even after Path is repointed. On Windows exec.Command(ctx, "bash", …)
	// usually fails that lookup, so without this every retargeted command
	// would fail with "bash: executable file not found" while pointing at
	// wsl.exe.
	cmd.Err = nil
	// A Wails GUI binary has no console, so each child process would otherwise
	// allocate a visible one — a black window flashing on every command.
	hideConsoleWindow(cmd)
}

// ApplyGit is the single entry point every git call site uses. It detects the
// target from the working directory, so a caller needs no WSL awareness beyond
// this one line, and it is inert for every non-WSL directory.
func ApplyGit(cmd *exec.Cmd, dir string, argv []string) bool {
	plan, ok := RetargetGit(DetectFor(dir), dir, argv)
	if !ok {
		return false
	}
	apply(cmd, plan)
	return true
}
