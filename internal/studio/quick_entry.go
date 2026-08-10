package studio

import (
	"fmt"
	"runtime"
	"sort"
	"strings"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type quickEntryController interface {
	Stop() error
}

type QuickEntryStatus struct {
	Supported     bool   `json:"supported"`
	Enabled       bool   `json:"enabled"`
	Active        bool   `json:"active"`
	Shortcut      string `json:"shortcut"`
	Error         string `json:"error,omitempty"`
	VoiceEnabled  bool   `json:"voiceEnabled"`
	VoiceActive   bool   `json:"voiceActive"`
	VoiceShortcut string `json:"voiceShortcut"`
	VoiceError    string `json:"voiceError,omitempty"`
}

const (
	quickEntryDoubleOption = "Double-tap Option"
	quickEntryCapsLock     = "Caps Lock"
)

type quickEntryShortcutSpec struct {
	Key     string
	Control bool
	Alt     bool
	Shift   bool
	Meta    bool
}

func defaultQuickEntryShortcut() string {
	if runtime.GOOS == "windows" {
		return "Ctrl+Alt+Space"
	}
	if runtime.GOOS == "darwin" {
		return quickEntryDoubleOption
	}
	return "Alt+Space"
}

func defaultVoiceShortcut() string {
	if runtime.GOOS == "darwin" {
		return quickEntryCapsLock
	}
	if runtime.GOOS == "windows" {
		return "Ctrl+Alt+D"
	}
	return "Alt+Shift+D"
}

func parseQuickEntryShortcut(value string) (quickEntryShortcutSpec, error) {
	var spec quickEntryShortcutSpec
	value = strings.TrimSpace(value)
	if value == "" {
		return spec, fmt.Errorf("shortcut cannot be empty")
	}
	parts := strings.Split(value, "+")
	for _, rawPart := range parts {
		part := strings.ToLower(strings.TrimSpace(rawPart))
		if part == "" {
			return spec, fmt.Errorf("shortcut contains an empty key")
		}
		switch part {
		case "ctrl", "control":
			if spec.Control {
				return spec, fmt.Errorf("Control is repeated")
			}
			spec.Control = true
		case "alt", "option":
			if spec.Alt {
				return spec, fmt.Errorf("Alt/Option is repeated")
			}
			spec.Alt = true
		case "shift":
			if spec.Shift {
				return spec, fmt.Errorf("Shift is repeated")
			}
			spec.Shift = true
		case "cmd", "command", "meta", "win", "super":
			if spec.Meta {
				return spec, fmt.Errorf("Command/Meta is repeated")
			}
			spec.Meta = true
		default:
			if spec.Key != "" {
				return spec, fmt.Errorf("shortcut must contain exactly one non-modifier key")
			}
			spec.Key = normalizeQuickEntryKey(part)
			if spec.Key == "" {
				return spec, fmt.Errorf("unsupported key %q; use A-Z, 0-9, Space, Enter, Tab, Escape, an arrow, or F1-F12", rawPart)
			}
		}
	}
	if spec.Key == "" {
		return spec, fmt.Errorf("shortcut needs a non-modifier key")
	}
	if !spec.Control && !spec.Alt && !spec.Shift && !spec.Meta {
		return spec, fmt.Errorf("shortcut needs at least one modifier")
	}
	return spec, nil
}

func normalizeQuickEntryKey(key string) string {
	if len(key) == 1 && ((key[0] >= 'a' && key[0] <= 'z') || (key[0] >= '0' && key[0] <= '9')) {
		return strings.ToUpper(key)
	}
	switch key {
	case "space", "spacebar":
		return "Space"
	case "enter", "return":
		return "Enter"
	case "tab":
		return "Tab"
	case "escape", "esc":
		return "Escape"
	case "left", "arrowleft":
		return "Left"
	case "right", "arrowright":
		return "Right"
	case "up", "arrowup":
		return "Up"
	case "down", "arrowdown":
		return "Down"
	}
	if len(key) >= 2 && key[0] == 'f' {
		for i := 1; i <= 12; i++ {
			if key == fmt.Sprintf("f%d", i) {
				return strings.ToUpper(key)
			}
		}
	}
	return ""
}

func formatQuickEntryShortcut(spec quickEntryShortcutSpec) string {
	modifiers := make([]string, 0, 4)
	if runtime.GOOS == "darwin" {
		if spec.Meta {
			modifiers = append(modifiers, "Command")
		}
		if spec.Control {
			modifiers = append(modifiers, "Control")
		}
		if spec.Alt {
			modifiers = append(modifiers, "Option")
		}
		if spec.Shift {
			modifiers = append(modifiers, "Shift")
		}
	} else {
		if spec.Control {
			modifiers = append(modifiers, "Ctrl")
		}
		if spec.Alt {
			modifiers = append(modifiers, "Alt")
		}
		if spec.Shift {
			modifiers = append(modifiers, "Shift")
		}
		if spec.Meta {
			modifiers = append(modifiers, "Meta")
		}
	}
	return strings.Join(append(modifiers, spec.Key), "+")
}

func normalizeQuickEntryShortcut(value string) (string, error) {
	special := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	switch special {
	case "double option", "double-option", "doubleoption", "double tap option", "double-tap option", "option option":
		return quickEntryDoubleOption, nil
	}
	spec, err := parseQuickEntryShortcut(value)
	if err != nil {
		return "", err
	}
	return formatQuickEntryShortcut(spec), nil
}

func normalizeVoiceShortcut(value string) (string, error) {
	special := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	switch special {
	case "caps lock", "caps-lock", "capslock":
		return quickEntryCapsLock, nil
	}
	spec, err := parseQuickEntryShortcut(value)
	if err != nil {
		return "", err
	}
	return formatQuickEntryShortcut(spec), nil
}

// quickEntryKeyNames is used only by platform tests to ensure every accepted
// key has a native mapping. Sorting keeps failures deterministic.
func quickEntryKeyNames() []string {
	keys := []string{"Space", "Enter", "Tab", "Escape", "Left", "Right", "Up", "Down"}
	for key := 'A'; key <= 'Z'; key++ {
		keys = append(keys, string(key))
	}
	for key := '0'; key <= '9'; key++ {
		keys = append(keys, string(key))
	}
	for key := 1; key <= 12; key++ {
		keys = append(keys, fmt.Sprintf("F%d", key))
	}
	sort.Strings(keys)
	return keys
}

func (s *Studio) setQuickEntryEnabled(enabled bool, shortcut string) error {
	s.quickEntryMu.Lock()
	defer s.quickEntryMu.Unlock()
	if !enabled {
		controller := s.quickEntry
		if controller == nil {
			return nil
		}
		if err := controller.Stop(); err != nil {
			return err
		}
		s.quickEntry = nil
		return nil
	}
	if s.quickEntry != nil {
		return nil
	}
	starter := s.testQuickEntryStarter
	if starter == nil {
		if !nativeQuickEntrySupported() {
			return fmt.Errorf("global Quick Entry is supported only on macOS and Windows")
		}
		starter = startNativeQuickEntry
	}
	controller, err := starter(shortcut, func() {
		s.startBackground("quick-entry", func() { s.activateQuickEntry("text") })
	})
	if err != nil {
		return err
	}
	if controller == nil {
		return fmt.Errorf("native shortcut registration returned no controller")
	}
	s.quickEntry = controller
	return nil
}

func (s *Studio) replaceQuickEntryShortcut(previous, next string) error {
	s.quickEntryMu.Lock()
	defer s.quickEntryMu.Unlock()
	oldController := s.quickEntry
	if oldController == nil {
		return fmt.Errorf("current Quick Entry shortcut is not active")
	}
	if err := oldController.Stop(); err != nil {
		return err
	}
	s.quickEntry = nil
	starter := s.testQuickEntryStarter
	if starter == nil {
		starter = startNativeQuickEntry
	}
	start := func(shortcut string) (quickEntryController, error) {
		return starter(shortcut, func() { s.startBackground("quick-entry", func() { s.activateQuickEntry("text") }) })
	}
	controller, err := start(next)
	if err == nil && controller == nil {
		err = fmt.Errorf("native shortcut registration returned no controller")
	}
	if err == nil {
		s.quickEntry = controller
		return nil
	}
	rollback, rollbackErr := start(previous)
	if rollbackErr == nil && rollback == nil {
		rollbackErr = fmt.Errorf("native shortcut registration returned no controller")
	}
	if rollbackErr == nil {
		s.quickEntry = rollback
		return err
	}
	return fmt.Errorf("%v; restoring %s also failed: %v", err, previous, rollbackErr)
}

func (s *Studio) setVoiceShortcutEnabled(enabled bool, shortcut string) error {
	s.voiceShortcutMu.Lock()
	defer s.voiceShortcutMu.Unlock()
	if !enabled {
		controller := s.voiceShortcut
		if controller == nil {
			return nil
		}
		if err := controller.Stop(); err != nil {
			return err
		}
		s.voiceShortcut = nil
		return nil
	}
	if s.voiceShortcut != nil {
		return nil
	}
	starter := s.testVoiceShortcutStarter
	if starter == nil {
		starter = s.testQuickEntryStarter
	}
	if starter == nil {
		if !nativeQuickEntrySupported() {
			return fmt.Errorf("global voice dictation is supported only on macOS and Windows")
		}
		starter = startNativeQuickEntry
	}
	controller, err := starter(shortcut, func() {
		s.startBackground("voice-shortcut", func() { s.activateQuickEntry("voice") })
	})
	if err != nil {
		return err
	}
	if controller == nil {
		return fmt.Errorf("native voice shortcut registration returned no controller")
	}
	s.voiceShortcut = controller
	return nil
}

func (s *Studio) replaceVoiceShortcut(previous, next string) error {
	s.voiceShortcutMu.Lock()
	defer s.voiceShortcutMu.Unlock()
	oldController := s.voiceShortcut
	if oldController == nil {
		return fmt.Errorf("current voice shortcut is not active")
	}
	if err := oldController.Stop(); err != nil {
		return err
	}
	s.voiceShortcut = nil
	starter := s.testVoiceShortcutStarter
	if starter == nil {
		starter = s.testQuickEntryStarter
	}
	if starter == nil {
		starter = startNativeQuickEntry
	}
	start := func(shortcut string) (quickEntryController, error) {
		return starter(shortcut, func() { s.startBackground("voice-shortcut", func() { s.activateQuickEntry("voice") }) })
	}
	controller, err := start(next)
	if err == nil && controller == nil {
		err = fmt.Errorf("native voice shortcut registration returned no controller")
	}
	if err == nil {
		s.voiceShortcut = controller
		return nil
	}
	rollback, rollbackErr := start(previous)
	if rollbackErr == nil && rollback == nil {
		rollbackErr = fmt.Errorf("native voice shortcut registration returned no controller")
	}
	if rollbackErr == nil {
		s.voiceShortcut = rollback
		return err
	}
	return fmt.Errorf("%v; restoring %s also failed: %v", err, previous, rollbackErr)
}

func (s *Studio) activateQuickEntry(mode string) {
	if mode != "voice" {
		mode = "text"
	}
	if s.testQuickEntryActivation != nil {
		s.testQuickEntryActivation(mode)
		return
	}
	if s.ctx == nil {
		return
	}
	windowStatus, windowErr := s.ShowQuickEntryWindow()
	if windowErr != nil {
		s.LogEvent("warn", "quick-entry", windowErr.Error())
	}
	if !windowStatus.Open {
		wailsRuntime.WindowUnminimise(s.ctx)
		wailsRuntime.WindowShow(s.ctx)
	}
	shortcut := s.currentQuickEntryShortcut()
	if mode == "voice" {
		shortcut = s.currentVoiceShortcut()
	}
	wailsRuntime.EventsEmit(s.ctx, EventQuickEntry, map[string]any{
		"shortcut": shortcut,
		"mode":     mode,
	})
}

func (s *Studio) currentVoiceShortcut() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.config == nil || s.config.Settings.VoiceShortcut == "" {
		return defaultVoiceShortcut()
	}
	return s.config.Settings.VoiceShortcut
}

func (s *Studio) currentQuickEntryShortcut() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.config == nil || s.config.Settings.QuickEntryShortcut == "" {
		return defaultQuickEntryShortcut()
	}
	return s.config.Settings.QuickEntryShortcut
}

// GetQuickEntryStatus distinguishes the persisted preference from the live OS
// registration, which can fail when another application owns the shortcut.
func (s *Studio) GetQuickEntryStatus() QuickEntryStatus {
	s.mu.RLock()
	enabled := s.config != nil && s.config.Settings.QuickEntryEnabled
	voiceEnabled := s.config != nil && s.config.Settings.VoiceShortcutEnabled
	shortcut := defaultQuickEntryShortcut()
	voiceShortcut := defaultVoiceShortcut()
	if s.config != nil && s.config.Settings.QuickEntryShortcut != "" {
		shortcut = s.config.Settings.QuickEntryShortcut
	}
	if s.config != nil && s.config.Settings.VoiceShortcut != "" {
		voiceShortcut = s.config.Settings.VoiceShortcut
	}
	s.mu.RUnlock()
	s.quickEntryMu.Lock()
	active := s.quickEntry != nil
	s.quickEntryMu.Unlock()
	s.voiceShortcutMu.Lock()
	voiceActive := s.voiceShortcut != nil
	s.voiceShortcutMu.Unlock()
	supported := nativeQuickEntrySupported() || s.testQuickEntryStarter != nil || s.testVoiceShortcutStarter != nil
	status := QuickEntryStatus{
		Supported:     supported,
		Enabled:       enabled,
		Active:        active,
		Shortcut:      shortcut,
		VoiceEnabled:  voiceEnabled,
		VoiceActive:   voiceActive,
		VoiceShortcut: voiceShortcut,
	}
	if enabled && supported && !active {
		status.Error = "The global shortcut is not registered. Another app may already be using it."
	}
	if enabled && !supported {
		status.Error = "Global Quick Entry is supported only on macOS and Windows."
	}
	if voiceEnabled && supported && !voiceActive {
		status.VoiceError = "The global voice shortcut is not registered. Another app may already be using it, or macOS Accessibility access is missing."
	}
	if voiceEnabled && !supported {
		status.VoiceError = "Global voice dictation is supported only on macOS and Windows."
	}
	return status
}
