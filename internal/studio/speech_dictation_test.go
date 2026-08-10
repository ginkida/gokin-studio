package studio

import (
	"context"
	"strings"
	"sync"
	"testing"
)

type fakeNativeSpeechController struct {
	mu    sync.Mutex
	stops []bool
}

func (c *fakeNativeSpeechController) Stop(cancel bool) error {
	c.mu.Lock()
	c.stops = append(c.stops, cancel)
	c.mu.Unlock()
	return nil
}

func (c *fakeNativeSpeechController) stopValues() []bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]bool(nil), c.stops...)
}

func availableSpeechStatus() nativeSpeechStatus {
	return nativeSpeechStatus{
		Supported:               true,
		Available:               true,
		SpeechAuthorization:     "authorized",
		MicrophoneAuthorization: "authorized",
	}
}

func TestSpeechLanguageAndSessionValidation(t *testing.T) {
	for input, want := range map[string]string{"": "", "en_US": "en-US", "ru-RU": "ru-RU", "zh-Hans-CN": "zh-Hans-CN"} {
		got, err := normalizeSpeechLanguage(input)
		if err != nil || got != want {
			t.Fatalf("normalize language %q = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"en US", "en/US", strings.Repeat("a", SpeechDictationLanguageMaxBytes+1)} {
		if got, err := normalizeSpeechLanguage(input); err == nil {
			t.Fatalf("invalid language %q normalized to %q", input, got)
		}
	}
	for _, input := range []string{"speech-123", "voice_session:1"} {
		if err := validateSpeechSessionID(input); err != nil {
			t.Fatalf("valid session ID %q: %v", input, err)
		}
	}
	for _, input := range []string{"", "speech/id", strings.Repeat("x", 129)} {
		if err := validateSpeechSessionID(input); err == nil {
			t.Fatalf("invalid session ID %q accepted", input)
		}
	}
}

func TestNativeSpeechLifecycleEmitsOrderedTranscriptEvents(t *testing.T) {
	s := newStudioForTest(t)
	s.testSpeechStatus = func(string) nativeSpeechStatus { return availableSpeechStatus() }
	controller := &fakeNativeSpeechController{}
	var callback func(nativeSpeechEvent)
	s.testSpeechStarter = func(sessionID, language string, emit func(nativeSpeechEvent)) (nativeSpeechController, error) {
		if sessionID != "speech-test" || language != "en-US" {
			t.Fatalf("starter args = %q / %q", sessionID, language)
		}
		callback = emit
		emit(nativeSpeechEvent{Type: "authorizing"})
		return controller, nil
	}
	var events []SpeechDictationEvent
	s.testSpeechEmitter = func(event SpeechDictationEvent) { events = append(events, event) }

	started, err := s.StartSpeechDictation("speech-test", "en_US")
	if err != nil || !started.Native || started.ID != "speech-test" || callback == nil {
		t.Fatalf("StartSpeechDictation = %#v, %v", started, err)
	}
	callback(nativeSpeechEvent{Type: "started"})
	callback(nativeSpeechEvent{Type: "transcript", Text: "hello wor", Final: false})
	callback(nativeSpeechEvent{Type: "transcript", Text: "Hello world.", Final: true})
	if err := s.StopSpeechDictation("speech-test"); err != nil {
		t.Fatal(err)
	}
	callback(nativeSpeechEvent{Type: "stopping"})
	callback(nativeSpeechEvent{Type: "ended"})

	wantTypes := []string{"authorizing", "started", "transcript", "transcript", "stopping", "ended"}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %#v", events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want || events[i].Sequence != uint64(i+1) {
			t.Fatalf("event[%d] = %#v, want type %q sequence %d", i, events[i], want, i+1)
		}
	}
	if events[2].Text != "hello wor" || events[2].Final || events[3].Text != "Hello world." || !events[3].Final {
		t.Fatalf("transcript events = %#v / %#v", events[2], events[3])
	}
	if stops := controller.stopValues(); len(stops) != 1 || stops[0] {
		t.Fatalf("graceful stops = %#v", stops)
	}
	if status := s.GetSpeechDictationStatus(); status.Listening {
		t.Fatalf("ended speech still listening: %#v", status)
	}
}

func TestSpeechDictationRejectsConcurrentSessionAndCancelIgnoresLateEvents(t *testing.T) {
	s := newStudioForTest(t)
	s.testSpeechStatus = func(string) nativeSpeechStatus { return availableSpeechStatus() }
	controller := &fakeNativeSpeechController{}
	var callback func(nativeSpeechEvent)
	s.testSpeechStarter = func(_, _ string, emit func(nativeSpeechEvent)) (nativeSpeechController, error) {
		callback = emit
		return controller, nil
	}
	emitted := 0
	s.testSpeechEmitter = func(SpeechDictationEvent) { emitted++ }
	if _, err := s.StartSpeechDictation("speech-one", "en-US"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartSpeechDictation("speech-two", "en-US"); err == nil {
		t.Fatal("concurrent native speech session was accepted")
	}
	if err := s.CancelSpeechDictation("speech-one"); err != nil {
		t.Fatal(err)
	}
	callback(nativeSpeechEvent{Type: "transcript", Text: "late audio"})
	if emitted != 0 {
		t.Fatalf("late event emitted after cancel: %d", emitted)
	}
	if stops := controller.stopValues(); len(stops) != 1 || !stops[0] {
		t.Fatalf("cancel stops = %#v", stops)
	}
}

func TestSpeechPermissionStatusAndShutdownCleanup(t *testing.T) {
	s := newStudioForTest(t)
	s.testSpeechStatus = func(string) nativeSpeechStatus { return availableSpeechStatus() }
	s.testSpeechPermissions = func(context.Context) (nativeSpeechStatus, error) { return availableSpeechStatus(), nil }
	status, err := s.RequestSpeechDictationPermissions()
	if err != nil || !status.Native || status.SpeechAuthorization != "authorized" || status.MicrophoneAuthorization != "authorized" {
		t.Fatalf("permission status = %#v, %v", status, err)
	}
	controller := &fakeNativeSpeechController{}
	s.testSpeechStarter = func(_, _ string, _ func(nativeSpeechEvent)) (nativeSpeechController, error) { return controller, nil }
	if _, err := s.StartSpeechDictation("speech-shutdown", "en-US"); err != nil {
		t.Fatal(err)
	}
	s.cancelSpeechDictationForShutdown()
	if stops := controller.stopValues(); len(stops) != 1 || !stops[0] {
		t.Fatalf("shutdown stops = %#v", stops)
	}
}

func TestSpeechEventTextIsBoundedAndUTF8Safe(t *testing.T) {
	s := newStudioForTest(t)
	s.testSpeechStatus = func(string) nativeSpeechStatus { return availableSpeechStatus() }
	var callback func(nativeSpeechEvent)
	s.testSpeechStarter = func(_, _ string, emit func(nativeSpeechEvent)) (nativeSpeechController, error) {
		callback = emit
		return &fakeNativeSpeechController{}, nil
	}
	var event SpeechDictationEvent
	s.testSpeechEmitter = func(value SpeechDictationEvent) { event = value }
	if _, err := s.StartSpeechDictation("speech-bounded", "ru-RU"); err != nil {
		t.Fatal(err)
	}
	callback(nativeSpeechEvent{Type: "transcript", Text: strings.Repeat("я", SpeechDictationTextMaxBytes)})
	if len(event.Text) > SpeechDictationTextMaxBytes || !strings.HasSuffix(event.Text, "я") {
		t.Fatalf("bounded transcript bytes=%d valid suffix=%t", len(event.Text), strings.HasSuffix(event.Text, "я"))
	}
}
