//go:build windows

package tasks

import (
	"fmt"
	"os"
	"os/exec"
)

// setProcAttr sets Windows-specific process attributes
func setProcAttr(cmd *exec.Cmd) {
	// No special process attributes needed on Windows
}

// killProcessGroup kills the complete process tree on Windows.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	// Shells and package managers commonly leave the actual worker one or more
	// levels below the wrapper. taskkill /T is the Windows counterpart to the
	// Unix process-group signal. Fall back to the process handle when taskkill
	// is unavailable or the tree has already changed concurrently.
	if err := exec.Command("taskkill", "/PID", fmt.Sprintf("%d", cmd.Process.Pid), "/T", "/F").Run(); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
