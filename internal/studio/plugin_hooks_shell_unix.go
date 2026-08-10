//go:build !windows

package studio

import (
	"context"
	"os/exec"
)

func pluginHookCommand(ctx context.Context, command string, args []string) *exec.Cmd {
	shellArgs := []string{"-c", command, "--"}
	shellArgs = append(shellArgs, args...)
	return exec.CommandContext(ctx, "/bin/sh", shellArgs...)
}
