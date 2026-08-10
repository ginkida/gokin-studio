package studio

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	SpeechDictationLanguageMaxBytes = 64
	SpeechDictationTextMaxBytes     = 100_000
)

type SpeechDictationStatus struct {
	Supported               bool   `json:"supported"`
	Native                  bool   `json:"native"`
	Available               bool   `json:"available"`
	Listening               bool   `json:"listening"`
	SpeechAuthorization     string `json:"speechAuthorization"`
	MicrophoneAuthorization string `json:"microphoneAuthorization"`
	Error                   string `json:"error,omitempty"`
}

type SpeechDictationSession struct {
	ID     string `json:"id"`
	Native bool   `json:"native"`
}

type nativeSpeechStatus struct {
	Supported               bool
	Available               bool
	SpeechAuthorization     string
	MicrophoneAuthorization string
	Error                   string
}

type nativeSpeechEvent struct {
	Type  string
	Text  string
	Final bool
	Error string
}

type nativeSpeechController interface {
	Stop(cancel bool) error
}

type studioSpeechSession struct {
	id         string
	controller nativeSpeechController
	sequence   atomic.Uint64
	state      string
}

func normalizeSpeechLanguage(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if !utf8.ValidString(value) || len(value) > SpeechDictationLanguageMaxBytes {
		return "", fmt.Errorf("speech language must be valid UTF-8 and at most %d bytes", SpeechDictationLanguageMaxBytes)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return "", fmt.Errorf("speech language contains unsupported character %q", r)
	}
	return strings.ReplaceAll(value, "_", "-"), nil
}

func validateSpeechSessionID(value string) error {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return fmt.Errorf("invalid speech session ID")
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == ':' {
			continue
		}
		return fmt.Errorf("invalid speech session ID")
	}
	return nil
}

func (s *Studio) speechStatus(language string) nativeSpeechStatus {
	if s.testSpeechStatus != nil {
		return s.testSpeechStatus(language)
	}
	return getNativeSpeechStatus(language)
}

func (s *Studio) GetSpeechDictationStatus() SpeechDictationStatus {
	status := s.speechStatus("")
	s.speechMu.Lock()
	listening := s.speechSession != nil && s.speechSession.state != "ended"
	s.speechMu.Unlock()
	return SpeechDictationStatus{
		Supported:               status.Supported,
		Native:                  status.Supported,
		Available:               status.Available,
		Listening:               listening,
		SpeechAuthorization:     status.SpeechAuthorization,
		MicrophoneAuthorization: status.MicrophoneAuthorization,
		Error:                   status.Error,
	}
}

// RequestSpeechDictationPermissions is called only from an explicit Settings
// action. Merely opening Settings or Quick Entry never prompts for microphone
// or speech-recognition access.
func (s *Studio) RequestSpeechDictationPermissions() (SpeechDictationStatus, error) {
	requester := s.testSpeechPermissions
	if requester == nil {
		requester = requestNativeSpeechPermissions
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	status, err := requester(ctx)
	if err != nil {
		return SpeechDictationStatus{}, err
	}
	return SpeechDictationStatus{
		Supported:               status.Supported,
		Native:                  status.Supported,
		Available:               status.Available,
		SpeechAuthorization:     status.SpeechAuthorization,
		MicrophoneAuthorization: status.MicrophoneAuthorization,
		Error:                   status.Error,
	}, nil
}

func (s *Studio) StartSpeechDictation(sessionID, language string) (SpeechDictationSession, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.shuttingDown {
		return SpeechDictationSession{}, fmt.Errorf("studio is shutting down")
	}
	if err := validateSpeechSessionID(sessionID); err != nil {
		return SpeechDictationSession{}, err
	}
	language, err := normalizeSpeechLanguage(language)
	if err != nil {
		return SpeechDictationSession{}, err
	}
	status := s.speechStatus(language)
	if !status.Supported {
		return SpeechDictationSession{}, fmt.Errorf("native speech dictation is unavailable on this platform")
	}
	if !status.Available {
		return SpeechDictationSession{}, fmt.Errorf("native speech recognition is currently unavailable for this language")
	}

	s.speechMu.Lock()
	if s.speechSession != nil {
		activeID := s.speechSession.id
		s.speechMu.Unlock()
		return SpeechDictationSession{}, fmt.Errorf("speech dictation session %s is already active", activeID)
	}
	session := &studioSpeechSession{id: sessionID, state: "starting"}
	s.speechSession = session
	s.speechMu.Unlock()

	starter := s.testSpeechStarter
	if starter == nil {
		starter = startNativeSpeechDictation
	}
	controller, err := starter(sessionID, language, func(event nativeSpeechEvent) {
		s.handleNativeSpeechEvent(sessionID, event)
	})
	if err != nil || controller == nil {
		s.speechMu.Lock()
		if s.speechSession == session {
			s.speechSession = nil
		}
		s.speechMu.Unlock()
		if err == nil {
			err = fmt.Errorf("native speech controller was not created")
		}
		return SpeechDictationSession{}, err
	}

	s.speechMu.Lock()
	if s.speechSession == session {
		session.controller = controller
		stopRequested := session.state == "stopping"
		s.speechMu.Unlock()
		if stopRequested {
			_ = controller.Stop(false)
		}
	} else {
		s.speechMu.Unlock()
		_ = controller.Stop(true)
	}
	return SpeechDictationSession{ID: sessionID, Native: true}, nil
}

func (s *Studio) StopSpeechDictation(sessionID string) error {
	return s.stopSpeechDictation(sessionID, false)
}

func (s *Studio) CancelSpeechDictation(sessionID string) error {
	return s.stopSpeechDictation(sessionID, true)
}

func (s *Studio) stopSpeechDictation(sessionID string, cancel bool) error {
	if err := validateSpeechSessionID(sessionID); err != nil {
		return err
	}
	s.speechMu.Lock()
	session := s.speechSession
	if session == nil || session.id != sessionID {
		s.speechMu.Unlock()
		return nil
	}
	controller := session.controller
	if cancel {
		s.speechSession = nil
		session.state = "ended"
	} else {
		session.state = "stopping"
	}
	s.speechMu.Unlock()
	if controller == nil {
		return nil
	}
	return controller.Stop(cancel)
}

func (s *Studio) cancelSpeechDictationForShutdown() {
	s.speechMu.Lock()
	session := s.speechSession
	s.speechSession = nil
	if session != nil {
		session.state = "ended"
	}
	s.speechMu.Unlock()
	if session != nil && session.controller != nil {
		_ = session.controller.Stop(true)
	}
}

func (s *Studio) handleNativeSpeechEvent(sessionID string, native nativeSpeechEvent) {
	s.speechMu.Lock()
	session := s.speechSession
	if session == nil || session.id != sessionID {
		s.speechMu.Unlock()
		return
	}
	if native.Type == "started" || native.Type == "authorizing" || native.Type == "stopping" {
		session.state = native.Type
	}
	sequence := session.sequence.Add(1)
	if native.Type == "ended" {
		session.state = "ended"
		s.speechSession = nil
	}
	s.speechMu.Unlock()

	event := SpeechDictationEvent{
		SessionID: sessionID,
		Type:      native.Type,
		Text:      truncateUTF8(native.Text, SpeechDictationTextMaxBytes),
		Final:     native.Final,
		Error:     truncateUTF8(native.Error, 2048),
		Sequence:  sequence,
	}
	if s.testSpeechEmitter != nil {
		s.testSpeechEmitter(event)
		return
	}
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, EventSpeechDictation, event)
	}
}
