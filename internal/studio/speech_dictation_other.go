//go:build !darwin

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
	}
}

func requestNativeSpeechPermissions(context.Context) (nativeSpeechStatus, error) {
	return getNativeSpeechStatus(""), fmt.Errorf("native speech dictation is unavailable on this platform")
}

func startNativeSpeechDictation(string, string, func(nativeSpeechEvent)) (nativeSpeechController, error) {
	return nil, fmt.Errorf("native speech dictation is unavailable on this platform")
}
