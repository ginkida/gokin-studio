//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework UniformTypeIdentifiers
*/
import "C"

// Wails 2.12 uses UTType in its macOS file-dialog implementation. Some Wails
// CLI / Command Line Tools combinations omit the corresponding framework from
// the final application link even though the SDK header is available. Keeping
// the framework on the main package makes production builds deterministic.
