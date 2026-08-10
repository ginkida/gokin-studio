//go:build darwin && !cgo

package studio

import (
	"context"
	"fmt"
)

func getNativeSpeechStatus(string) nativeSpeechStatus {
	return nativeSpeechStatus{
		Supported:               false,
		Available:               false,
		SpeechAuthorization:     "unsupported",
		MicrophoneAuthorization: "unsupported",
		Error:                   "Native macOS speech dictation requires a cgo-enabled desktop build.",
	}
}

func requestNativeSpeechPermissions(context.Context) (nativeSpeechStatus, error) {
	return getNativeSpeechStatus(""), fmt.Errorf("native macOS speech dictation requires a cgo-enabled desktop build")
}

func startNativeSpeechDictation(string, string, func(nativeSpeechEvent)) (nativeSpeechController, error) {
	return nil, fmt.Errorf("native macOS speech dictation requires a cgo-enabled desktop build")
}
