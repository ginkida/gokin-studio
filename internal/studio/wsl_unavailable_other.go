//go:build !windows

package studio

// wslUnavailableDetail states plainly why WSL projects are not offered here.
func wslUnavailableDetail() string {
	return "WSL is a Windows feature; this build runs on a different operating system."
}
