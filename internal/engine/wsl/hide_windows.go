//go:build windows

package wsl

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW. Wails links the GUI binary with
// -H windowsgui, so it owns no console; every child process would allocate one
// and flash a black window on screen. Since bash, git, tests and formatters all
// run through here, that would be constant.
const createNoWindow = 0x08000000

func hideConsoleWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
