//go:build !darwin || !cgo

package studio

func nativeQuickEntryWindowSupported() bool { return false }

func showNativeQuickEntryWindow() error { return nil }

func hideNativeQuickEntryWindow(bool) error { return nil }
