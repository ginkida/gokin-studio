//go:build !darwin || !cgo

package studio

func externalBrowserActiveScriptsSupported() bool { return false }
