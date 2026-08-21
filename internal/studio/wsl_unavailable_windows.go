//go:build windows

package studio

// wslUnavailableDetail states plainly why WSL projects are not offered, which on
// Windows means wsl.exe itself was not found.
func wslUnavailableDetail() string {
	return "wsl.exe was not found. Install the Windows Subsystem for Linux, then restart Gokin Studio."
}
