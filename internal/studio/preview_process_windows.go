//go:build windows

package studio

import (
	"fmt"
	"os/exec"
)

func preparePreviewProcess(_ *exec.Cmd) {}

func killPreviewProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// Package-manager commands commonly spawn the actual dev server as a
	// child. taskkill /T mirrors Unix process-group cleanup on Windows.
	if err := exec.Command("taskkill", "/PID", fmt.Sprintf("%d", cmd.Process.Pid), "/T", "/F").Run(); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
