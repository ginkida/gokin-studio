//go:build !windows

package wsl

import "os/exec"

// Console windows are a Windows concern; nothing to hide elsewhere.
func hideConsoleWindow(*exec.Cmd) {}
