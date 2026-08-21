//go:build !windows

package wsl

import "context"

// WSL is a Windows feature. On macOS and Linux the path helpers in wsl.go stay
// available — they are pure, and their tests run everywhere — but nothing can
// be executed, so the runtime surface reports honestly rather than pretending.

// Executable returns "" because wsl.exe cannot exist on this platform.
func Executable() string { return "" }

// Available is always false off Windows.
func Available() bool { return false }

// ListDistros always reports that WSL is unavailable here.
func ListDistros(context.Context) ([]DistroState, error) { return nil, ErrUnavailable }

// HostCaps reports nothing supported; no command is ever retargeted here.
func HostCaps() Caps { return Caps{} }

// KnownDistros is empty off Windows.
func KnownDistros() []string { return nil }

// States is empty off Windows.
func States(context.Context) []DistroState { return nil }

// DetectFor always returns the host target, so every ApplyShell/ApplyExec call
// site keeps byte-identical behaviour on macOS and Linux. This one-line return
// is what makes the whole routing layer inert outside Windows.
func DetectFor(string) Target { return Target{} }
