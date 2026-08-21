package git

import (
	"context"
	"github.com/ginkida/gokin-studio/internal/engine/wsl"
	"os/exec"
)

func newCommand(workDir string, args ...string) *exec.Cmd {
	return newCommandContext(context.Background(), workDir, args...)
}

func newCommandContext(ctx context.Context, workDir string, args ...string) *exec.Cmd {
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
