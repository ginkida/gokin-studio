package studio

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeQuickEntryController struct {
	once    sync.Once
	stopped atomic.Int32
}

type fakeRegisteredShortcutController struct {
	once   sync.Once
	remove func()
}

func (c *fakeRegisteredShortcutController) Stop() error {
	c.once.Do(c.remove)
	return nil
}

type fakeShortcutRegistry struct {
	mu     sync.Mutex
	active map[string]bool
}

func newFakeShortcutRegistry() *fakeShortcutRegistry {
	return &fakeShortcutRegistry{active: make(map[string]bool)}
}

func (r *fakeShortcutRegistry) start(shortcut string, _ func()) (quickEntryController, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[shortcut] {
		return nil, errors.New("shortcut already registered")
	}
	r.active[shortcut] = true
	return &fakeRegisteredShortcutController{remove: func() {
		r.mu.Lock()
		delete(r.active, shortcut)
		r.mu.Unlock()
	}}, nil
}

func (r *fakeShortcutRegistry) has(shortcut string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[shortcut]
}

func (c *fakeQuickEntryController) Stop() error {
	c.once.Do(func() { c.stopped.Add(1) })
	return nil
}

func TestQuickEntrySettingsLifecycleAndActivation(t *testing.T) {
	s := newStudioForTest(t)
	controller := &fakeQuickEntryController{}
	var trigger func()
	var registeredShortcut string
	s.testQuickEntryStarter = func(shortcut string, callback func()) (quickEntryController, error) {
		registeredShortcut = shortcut
		trigger = callback
		return controller, nil
	}
	activated := make(chan struct{}, 1)
	s.testQuickEntryActivation = func(mode string) {
		if mode != "text" {
			t.Errorf("activation mode = %q, want text", mode)
		}
		activated <- struct{}{}
	}

	cfg := *s.GetSettings()
	cfg.Settings.QuickEntryEnabled = true
	cfg.Settings.QuickEntryShortcut = "shift + alt + g"
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatal(err)
	}
	status := s.GetQuickEntryStatus()
	if !status.Supported || !status.Enabled || !status.Active || status.Shortcut == "" || registeredShortcut != status.Shortcut || trigger == nil {
		t.Fatalf("enabled Quick Entry status = %#v", status)
	}

	trigger()
	select {
	case <-activated:
	case <-time.After(time.Second):
		t.Fatal("native shortcut callback did not activate Quick Entry")
	}
	s.wg.Wait()

	cfg = *s.GetSettings()
	cfg.Settings.QuickEntryEnabled = false
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatal(err)
	}
	status = s.GetQuickEntryStatus()
	if status.Enabled || status.Active || controller.stopped.Load() != 1 {
		t.Fatalf("disabled Quick Entry status = %#v, stops=%d", status, controller.stopped.Load())
	}
}

func TestQuickEntryRegistrationFailureDoesNotPersistPreference(t *testing.T) {
	s := newStudioForTest(t)
	s.testQuickEntryStarter = func(string, func()) (quickEntryController, error) {
		return nil, errors.New("shortcut already registered")
	}
	cfg := *s.GetSettings()
	cfg.Settings.QuickEntryEnabled = true
	if err := s.UpdateSettings(cfg); err == nil {
		t.Fatal("shortcut registration failure was ignored")
	}
	if s.GetSettings().Settings.QuickEntryEnabled {
		t.Fatal("failed Quick Entry preference was persisted")
	}
	if status := s.GetQuickEntryStatus(); status.Active {
		t.Fatalf("failed Quick Entry is active: %#v", status)
	}
}

func TestQuickEntryShortcutValidationAndNormalization(t *testing.T) {
	valid := []string{"alt+space", "Ctrl + Shift + K", "command+option+f12", "Ctrl+Left", "double option", "Double-tap Option"}
	for _, value := range valid {
		normalized, err := normalizeQuickEntryShortcut(value)
		if err != nil || normalized == "" {
			t.Fatalf("normalize %q = %q, %v", value, normalized, err)
		}
	}
	invalid := []string{"", "Space", "Alt", "Alt+Shift", "Alt+A+B", "Alt+VolumeUp", "Alt++A", "Ctrl+Control+A"}
	for _, value := range invalid {
		if normalized, err := normalizeQuickEntryShortcut(value); err == nil {
			t.Fatalf("normalize invalid %q = %q", value, normalized)
		}
	}
}

func TestVoiceShortcutValidationAndNormalization(t *testing.T) {
	valid := map[string]string{
		"capslock":        quickEntryCapsLock,
		"Caps Lock":       quickEntryCapsLock,
		"control+alt+d":   formatQuickEntryShortcut(quickEntryShortcutSpec{Key: "D", Control: true, Alt: true}),
		"Command+Shift+V": formatQuickEntryShortcut(quickEntryShortcutSpec{Key: "V", Meta: true, Shift: true}),
	}
	for value, want := range valid {
		if got, err := normalizeVoiceShortcut(value); err != nil || got != want {
			t.Fatalf("normalize voice %q = %q, %v; want %q", value, got, err, want)
		}
	}
	for _, value := range []string{"", "Space", "Double-tap Option", "Caps Lock+Shift"} {
		if got, err := normalizeVoiceShortcut(value); err == nil {
			t.Fatalf("normalize invalid voice %q = %q", value, got)
		}
	}
}

func TestVoiceShortcutLifecycleAndActivation(t *testing.T) {
	s := newStudioForTest(t)
	controller := &fakeQuickEntryController{}
	var trigger func()
	s.testVoiceShortcutStarter = func(shortcut string, callback func()) (quickEntryController, error) {
		if shortcut != quickEntryCapsLock {
			t.Fatalf("registered voice shortcut = %q", shortcut)
		}
		trigger = callback
		return controller, nil
	}
	activated := make(chan string, 1)
	s.testQuickEntryActivation = func(mode string) { activated <- mode }

	cfg := *s.GetSettings()
	cfg.Settings.VoiceShortcutEnabled = true
	cfg.Settings.VoiceShortcut = "capslock"
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatal(err)
	}
	status := s.GetQuickEntryStatus()
	if !status.VoiceEnabled || !status.VoiceActive || status.VoiceShortcut != quickEntryCapsLock || trigger == nil {
		t.Fatalf("enabled voice status = %#v", status)
	}
	trigger()
	select {
	case mode := <-activated:
		if mode != "voice" {
			t.Fatalf("activation mode = %q", mode)
		}
	case <-time.After(time.Second):
		t.Fatal("native voice callback did not activate Quick Entry")
	}
	s.wg.Wait()

	cfg = *s.GetSettings()
	cfg.Settings.VoiceShortcutEnabled = false
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatal(err)
	}
	if status := s.GetQuickEntryStatus(); status.VoiceEnabled || status.VoiceActive || controller.stopped.Load() != 1 {
		t.Fatalf("disabled voice status = %#v, stops=%d", status, controller.stopped.Load())
	}
}

func TestVoiceRegistrationFailureRollsBackNewQuickEntry(t *testing.T) {
	s := newStudioForTest(t)
	quick := &fakeQuickEntryController{}
	s.testQuickEntryStarter = func(string, func()) (quickEntryController, error) { return quick, nil }
	s.testVoiceShortcutStarter = func(string, func()) (quickEntryController, error) {
		return nil, errors.New("voice shortcut already registered")
	}
	cfg := *s.GetSettings()
	cfg.Settings.QuickEntryEnabled = true
	cfg.Settings.VoiceShortcutEnabled = true
	if err := s.UpdateSettings(cfg); err == nil {
		t.Fatal("voice registration failure was ignored")
	}
	settings := s.GetSettings().Settings
	if settings.QuickEntryEnabled || settings.VoiceShortcutEnabled {
		t.Fatalf("failed combined preferences persisted: %#v", settings)
	}
	status := s.GetQuickEntryStatus()
	if status.Active || status.VoiceActive || quick.stopped.Load() != 1 {
		t.Fatalf("combined rollback status=%#v quickStops=%d", status, quick.stopped.Load())
	}
}

func TestQuickEntryAndVoiceShortcutsCanSwapAtomically(t *testing.T) {
	s := newStudioForTest(t)
	registry := newFakeShortcutRegistry()
	s.testQuickEntryStarter = registry.start
	s.testVoiceShortcutStarter = registry.start

	cfg := *s.GetSettings()
	cfg.Settings.QuickEntryEnabled = true
	cfg.Settings.QuickEntryShortcut = "Alt+Space"
	cfg.Settings.VoiceShortcutEnabled = true
	cfg.Settings.VoiceShortcut = "Ctrl+Shift+K"
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatal(err)
	}
	before := s.GetSettings().Settings
	if !registry.has(before.QuickEntryShortcut) || !registry.has(before.VoiceShortcut) {
		t.Fatalf("initial registrations missing: %#v", registry.active)
	}

	cfg = *s.GetSettings()
	cfg.Settings.QuickEntryShortcut, cfg.Settings.VoiceShortcut = cfg.Settings.VoiceShortcut, cfg.Settings.QuickEntryShortcut
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatalf("swap shortcuts: %v", err)
	}
	after := s.GetSettings().Settings
	if after.QuickEntryShortcut != before.VoiceShortcut || after.VoiceShortcut != before.QuickEntryShortcut {
		t.Fatalf("swapped settings = %#v", after)
	}
	if !registry.has(after.QuickEntryShortcut) || !registry.has(after.VoiceShortcut) {
		t.Fatalf("swapped registrations missing: %#v", registry.active)
	}
}

func TestVoiceShortcutConflictRestoresPreviousRegistration(t *testing.T) {
	s := newStudioForTest(t)
	registry := newFakeShortcutRegistry()
	s.testQuickEntryStarter = registry.start
	s.testVoiceShortcutStarter = registry.start
	cfg := *s.GetSettings()
	cfg.Settings.QuickEntryEnabled = true
	cfg.Settings.QuickEntryShortcut = "Alt+Space"
	cfg.Settings.VoiceShortcutEnabled = true
	cfg.Settings.VoiceShortcut = "Ctrl+Shift+K"
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatal(err)
	}
	before := s.GetSettings().Settings
	cfg = *s.GetSettings()
	cfg.Settings.VoiceShortcut = before.QuickEntryShortcut
	if err := s.UpdateSettings(cfg); err == nil {
		t.Fatal("duplicate global shortcut was accepted")
	}
	after := s.GetSettings().Settings
	if after.VoiceShortcut != before.VoiceShortcut || !registry.has(before.QuickEntryShortcut) || !registry.has(before.VoiceShortcut) {
		t.Fatalf("conflict rollback settings=%#v registrations=%#v", after, registry.active)
	}
}

func TestQuickEntryShortcutChangeRollsBackOnRegistrationFailure(t *testing.T) {
	s := newStudioForTest(t)
	first := &fakeQuickEntryController{}
	rollback := &fakeQuickEntryController{}
	var registrations []string
	s.testQuickEntryStarter = func(shortcut string, _ func()) (quickEntryController, error) {
		registrations = append(registrations, shortcut)
		switch len(registrations) {
		case 1:
			return first, nil
		case 2:
			return nil, errors.New("shortcut already registered")
		default:
			return rollback, nil
		}
	}
	cfg := *s.GetSettings()
	cfg.Settings.QuickEntryEnabled = true
	cfg.Settings.QuickEntryShortcut = "Alt+Space"
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatal(err)
	}
	cfg = *s.GetSettings()
	previous := cfg.Settings.QuickEntryShortcut
	cfg.Settings.QuickEntryShortcut = "Ctrl+Shift+K"
	if err := s.UpdateSettings(cfg); err == nil {
		t.Fatal("shortcut replacement failure was ignored")
	}
	if got := s.GetSettings().Settings.QuickEntryShortcut; got != previous {
		t.Fatalf("persisted shortcut = %q, want rollback %q", got, previous)
	}
	status := s.GetQuickEntryStatus()
	if !status.Active || status.Shortcut != previous || first.stopped.Load() != 1 || rollback.stopped.Load() != 0 {
		t.Fatalf("rollback status=%#v firstStops=%d rollbackStops=%d", status, first.stopped.Load(), rollback.stopped.Load())
	}
}

func TestDiffSettingsQuickEntryToggle(t *testing.T) {
	diff := diffSettings(Settings{}, Settings{QuickEntryEnabled: true})
	if len(diff) != 1 || diff[0].Field != "quickEntryEnabled" {
		t.Fatalf("Quick Entry audit diff = %#v", diff)
	}
}

func TestDiffSettingsVoiceShortcut(t *testing.T) {
	diff := diffSettings(Settings{}, Settings{VoiceShortcutEnabled: true, VoiceShortcut: quickEntryCapsLock})
	if len(diff) != 2 || diff[0].Field != "voiceShortcutEnabled" || diff[1].Field != "voiceShortcut" {
		t.Fatalf("voice shortcut audit diff = %#v", diff)
	}
}
