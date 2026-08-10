//go:build !darwin && !windows

package studio

import "fmt"

func nativeQuickEntrySupported() bool { return false }

func startNativeQuickEntry(string, func()) (quickEntryController, error) {
	return nil, fmt.Errorf("global Quick Entry is unsupported on this platform")
}
