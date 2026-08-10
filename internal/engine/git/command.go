package git

import (
	"context"
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
	return cmd
}
