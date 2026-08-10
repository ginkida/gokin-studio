package tools

import (
	"context"
	"os/exec"
)

// newGitCommand disables repository-controlled executable hooks for all Git
// operations initiated by the agent. User-authored hooks are code execution,
// not part of the requested Git operation, and must never run implicitly.
// core.fsmonitor is disabled for the same reason: it may name an arbitrary
// executable in repository-local configuration.
func newGitCommand(ctx context.Context, workDir string, args ...string) *exec.Cmd {
	safeArgs := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
	}
	safeArgs = append(safeArgs, args...)
	cmd := exec.CommandContext(ctx, "git", safeArgs...)
	cmd.Dir = workDir
	return cmd
}
