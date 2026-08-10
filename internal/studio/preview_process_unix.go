//go:build !windows

package studio

import (
	"os/exec"
	"syscall"
)

func preparePreviewProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killPreviewProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// Kill the complete process group so package-manager wrappers cannot leave
	// Vite/Next/etc. children listening after the preview pane is closed.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
