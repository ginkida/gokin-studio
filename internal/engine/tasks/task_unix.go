//go:build unix

package tasks

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcAttr sets Unix-specific process attributes for proper cleanup
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// killProcessGroup kills the entire process group on Unix
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	// Kill process group (negative PID). Match os.Process.Kill's completed-process
	// contract so exec.Cmd can preserve a successful exit during a cancellation
	// boundary race.
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
