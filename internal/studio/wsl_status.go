package studio

import (
	"context"
	"fmt"

	"github.com/ginkida/gokin-studio/internal/engine/wsl"
)

// What the UI and diagnostics need to know about WSL: whether it exists, what
// distros are registered, and what to type to reach one.
//
// The honesty rule here is the same as everywhere else in this app: when a
// capability is missing, say which one and why, rather than showing an empty
// list that reads like "no distros installed".

// WSLDistroInfo describes one registered distro.
type WSLDistroInfo struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Version int    `json:"version,omitempty"` // 1 or 2; 0 when wsl.exe did not report it
	Default bool   `json:"default,omitempty"`
	// UNCRoot is the path to paste into the project picker.
	UNCRoot string `json:"uncRoot"`
}

// WSLStatus is the whole capability answer in one value.
type WSLStatus struct {
	Available  bool            `json:"available"`
	Detail     string          `json:"detail,omitempty"`
	SupportsCD bool            `json:"supportsCD"`
	Distros    []WSLDistroInfo `json:"distros"`
}

// GetWSLStatus reports whether WSL projects are usable on this machine.
//
// Off Windows it returns immediately with no exec at all — wsl.Executable() is
// a compile-time empty string there.
func (s *Studio) GetWSLStatus() *WSLStatus {
	if !wsl.Available() {
		return &WSLStatus{
			Available: false,
			Detail:    wslUnavailableDetail(),
			Distros:   []WSLDistroInfo{},
		}
	}
	states := wsl.States(context.Background())
	status := &WSLStatus{
		Available:  true,
		SupportsCD: wsl.HostCaps().SupportsCD,
		Distros:    make([]WSLDistroInfo, 0, len(states)),
	}
	for _, state := range states {
		status.Distros = append(status.Distros, WSLDistroInfo{
			Name:    state.Name,
			Running: state.Running,
			Version: state.Version,
			Default: state.Default,
			UNCRoot: wsl.Location{Distro: state.Name, LinuxPath: "/"}.WindowsPath(),
		})
	}
	if len(status.Distros) == 0 {
		status.Detail = "WSL is installed but no distributions are registered. Install one from the Microsoft Store, then reopen this dialog."
	} else if !status.SupportsCD {
		// The shell-cd fallback covers this, but the user should know why their
		// wsl.exe is being driven differently.
		status.Detail = "This wsl.exe predates the --cd flag, so the working directory is set by the shell instead. Updating WSL removes the extra step."
	}
	return status
}

// wslDistroNotice explains a project directory whose distro is not usable right
// now, so a stopped distro is not reported as a missing folder.
func wslDistroNotice(dir string, available bool, distros []wsl.DistroState) string {
	location, ok := wsl.ParseWindowsPath(dir)
	if !ok {
		return ""
	}
	if !available {
		return "This project lives in a WSL distribution, but wsl.exe was not found, so its commands cannot run."
	}
	for _, state := range distros {
		if !equalFoldASCII(state.Name, location.Distro) {
			continue
		}
		if !state.Running {
			return fmt.Sprintf(
				"Distribution %q is registered but not running. It starts automatically on the first command.",
				state.Name)
		}
		return ""
	}
	return fmt.Sprintf(
		"Distribution %q is not registered on this machine, so this project's files and commands are unavailable.",
		location.Distro)
}

// equalFoldASCII compares distro names the way Windows does.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
