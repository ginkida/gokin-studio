//go:build darwin && cgo

package studio

/*
#cgo CFLAGS: -fblocks
#cgo LDFLAGS: -framework Foundation -framework Speech -framework AVFoundation -framework AVFAudio
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>

bool gokinSpeechSupported(void);
int gokinSpeechAuthorizationStatus(void);
int gokinSpeechMicrophoneAuthorizationStatus(void);
bool gokinSpeechRecognizerAvailable(const char *locale);
bool gokinSpeechStart(uint32_t token, const char *locale);
void gokinSpeechStop(uint32_t token, bool cancel);
void gokinSpeechRequestPermissions(uint32_t token);
*/
import "C"

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

var (
	darwinSpeechNextToken           atomic.Uint32
	darwinSpeechCallbacks           sync.Map
	darwinSpeechPermissionCallbacks sync.Map
)

type darwinSpeechController struct {
	token         uint32
	mu            sync.Mutex
	stopRequested bool
	cancelled     bool
}

func nextDarwinSpeechToken() uint32 {
	token := darwinSpeechNextToken.Add(1)
	if token == 0 {
		token = darwinSpeechNextToken.Add(1)
	}
	return token
}

func speechAuthorizationLabel(value int) string {
	switch value {
	case 0:
		return "not-determined"
	case 1:
		return "denied"
	case 2:
		return "restricted"
	case 3:
		return "authorized"
	default:
		return "unsupported"
	}
}

func microphoneAuthorizationLabel(value int) string {
	switch value {
	case 0:
		return "not-determined"
	case 1:
		return "restricted"
	case 2:
		return "denied"
	case 3:
		return "authorized"
	default:
		return "unsupported"
	}
}

func getNativeSpeechStatus(language string) nativeSpeechStatus {
	if C.gokinSpeechSupported() == C.bool(false) {
		return nativeSpeechStatus{
			Supported:               false,
			Available:               false,
			SpeechAuthorization:     "unsupported",
			MicrophoneAuthorization: "unsupported",
			Error:                   "Native dictation requires macOS 14 or later.",
		}
	}
	locale := C.CString(language)
	defer C.free(unsafe.Pointer(locale))
	return nativeSpeechStatus{
		Supported:               true,
		Available:               C.gokinSpeechRecognizerAvailable(locale) != C.bool(false),
		SpeechAuthorization:     speechAuthorizationLabel(int(C.gokinSpeechAuthorizationStatus())),
		MicrophoneAuthorization: microphoneAuthorizationLabel(int(C.gokinSpeechMicrophoneAuthorizationStatus())),
	}
}

func requestNativeSpeechPermissions(ctx context.Context) (nativeSpeechStatus, error) {
	if C.gokinSpeechSupported() == C.bool(false) {
		return getNativeSpeechStatus(""), fmt.Errorf("native dictation requires macOS 14 or later")
	}
	token := nextDarwinSpeechToken()
	result := make(chan nativeSpeechStatus, 1)
	darwinSpeechPermissionCallbacks.Store(token, result)
	defer darwinSpeechPermissionCallbacks.Delete(token)
	C.gokinSpeechRequestPermissions(C.uint32_t(token))
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	select {
	case status := <-result:
		if status.SpeechAuthorization != "authorized" || status.MicrophoneAuthorization != "authorized" {
			status.Error = "Speech Recognition and Microphone access are both required. Review them in System Settings > Privacy & Security."
		}
		return status, nil
	case <-ctx.Done():
		return nativeSpeechStatus{}, ctx.Err()
	case <-timer.C:
		return nativeSpeechStatus{}, fmt.Errorf("speech permission request timed out")
	}
}

func startNativeSpeechDictation(_ string, language string, callback func(nativeSpeechEvent)) (nativeSpeechController, error) {
	if callback == nil {
		return nil, fmt.Errorf("native speech callback is required")
	}
	if C.gokinSpeechSupported() == C.bool(false) {
		return nil, fmt.Errorf("native dictation requires macOS 14 or later")
	}
	token := nextDarwinSpeechToken()
	darwinSpeechCallbacks.Store(token, callback)
	locale := C.CString(language)
	started := C.gokinSpeechStart(C.uint32_t(token), locale) != C.bool(false)
	C.free(unsafe.Pointer(locale))
	if !started {
		darwinSpeechCallbacks.Delete(token)
		return nil, fmt.Errorf("could not create the native macOS speech session")
	}
	return &darwinSpeechController{token: token}, nil
}

func (c *darwinSpeechController) Stop(cancel bool) error {
	c.mu.Lock()
	if c.cancelled || (!cancel && c.stopRequested) {
		c.mu.Unlock()
		return nil
	}
	if cancel {
		c.cancelled = true
		darwinSpeechCallbacks.Delete(c.token)
	} else {
		c.stopRequested = true
	}
	c.mu.Unlock()
	C.gokinSpeechStop(C.uint32_t(c.token), C.bool(cancel))
	return nil
}

func nativeSpeechEventType(value int) string {
	switch value {
	case 0:
		return "authorizing"
	case 1:
		return "started"
	case 2:
		return "transcript"
	case 3:
		return "stopping"
	case 4:
		return "ended"
	case 5:
		return "error"
	default:
		return "error"
	}
}

//export goNativeSpeechDarwinEvent
func goNativeSpeechDarwinEvent(token C.uint32_t, eventType C.int, textValue *C.char, errorValue *C.char, final C.bool) {
	key := uint32(token)
	value, ok := darwinSpeechCallbacks.Load(key)
	if !ok {
		return
	}
	event := nativeSpeechEvent{Type: nativeSpeechEventType(int(eventType)), Final: final != C.bool(false)}
	if textValue != nil {
		event.Text = C.GoString(textValue)
	}
	if errorValue != nil {
		event.Error = C.GoString(errorValue)
	}
	if callback, ok := value.(func(nativeSpeechEvent)); ok {
		callback(event)
	}
	if event.Type == "ended" {
		darwinSpeechCallbacks.Delete(key)
	}
}

//export goNativeSpeechDarwinPermissionResult
func goNativeSpeechDarwinPermissionResult(token C.uint32_t, speechStatus C.int, microphoneStatus C.int) {
	value, ok := darwinSpeechPermissionCallbacks.Load(uint32(token))
	if !ok {
		return
	}
	if result, ok := value.(chan nativeSpeechStatus); ok {
		result <- nativeSpeechStatus{
			Supported:               true,
			Available:               true,
			SpeechAuthorization:     speechAuthorizationLabel(int(speechStatus)),
			MicrophoneAuthorization: microphoneAuthorizationLabel(int(microphoneStatus)),
		}
	}
}
