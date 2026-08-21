package tools

import (
	"context"
	"github.com/ginkida/gokin-studio/internal/engine/wsl"
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
	// Inert for every non-WSL directory, which is every directory off Windows.
	wsl.ApplyGit(cmd, workDir, append([]string{"git"}, safeArgs...))
	return cmd
}
