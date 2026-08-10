//go:build darwin && !cgo

package studio

import "fmt"

func nativeQuickEntrySupported() bool { return false }

func startNativeQuickEntry(string, func()) (quickEntryController, error) {
	return nil, fmt.Errorf("global Quick Entry on macOS requires a cgo-enabled desktop build")
}
