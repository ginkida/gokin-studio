package studio

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestProjectSettersRejectNonFiniteAndExcessiveValues(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Validated")
	invalid := []struct {
		name string
		call func() error
	}{
		{"NaN budget", func() error { return s.SetProjectBudget(info.ID, math.NaN()) }},
		{"infinite budget", func() error { return s.SetProjectBudget(info.ID, math.Inf(1)) }},
		{"NaN temperature", func() error { return s.SetProjectModelParams(info.ID, float32(math.NaN()), 100) }},
		{"temperature range", func() error { return s.SetProjectModelParams(info.ID, 3, 100) }},
		{"token range", func() error { return s.SetProjectModelParams(info.ID, 1, 1_000_001) }},
		{"thinking range", func() error { return s.SetProjectThinking(info.ID, "enabled", 1_000_001) }},
		{"empty provider", func() error { return s.SetProjectProvider(info.ID, "", "model") }},
		{"atomic unsupported provider", func() error {
			_, err := s.ConfigureProjectModel(info.ID, "deepseek", "deepseek-v4-pro", 0, 0, "", 0)
			return err
		}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestSetProjectSystemPromptTruncatesAtValidUTF8Boundary(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Prompt")
	if err := s.SetProjectSystemPrompt(info.ID, strings.Repeat("🙂", 20_000)); err != nil {
		t.Fatal(err)
	}
	project, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.SystemPrompt) > 64<<10 || !utf8.ValidString(project.SystemPrompt) {
		t.Fatalf("system prompt remains oversized: %d", len(project.SystemPrompt))
	}
}

func TestUpdateSettingsPersistenceFailureRollsBackMemory(t *testing.T) {
	s := newStudioForTest(t)
	before := s.GetSettings().Settings
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := os.Getenv("GOKIN_CONFIG_DIR")
	if err := os.Setenv("GOKIN_CONFIG_DIR", blocked); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", previous) })

	candidate := StudioConfig{Settings: before}
	candidate.Settings.Theme = "light"
	if err := s.UpdateSettings(candidate); err == nil {
		t.Fatal("expected persistence failure")
	}
	after := s.GetSettings().Settings
	if after.Theme != before.Theme {
		t.Fatalf("settings changed despite persistence failure: before=%q after=%q", before.Theme, after.Theme)
	}
}

func TestProjectSettersPersistenceFailureLeaveMemoryAndClientUntouched(t *testing.T) {
	tests := []struct {
		name string
		call func(*Studio, string) error
	}{
		{"atomic model configuration", func(s *Studio, id string) error {
			_, err := s.ConfigureProjectModel(id, "kimi", "k3", 0.4, 65536, "enabled", 8192)
			return err
		}},
		{"provider", func(s *Studio, id string) error { return s.SetProjectProvider(id, "kimi", "kimi-for-coding") }},
		{"system prompt", func(s *Studio, id string) error { return s.SetProjectSystemPrompt(id, "replacement") }},
		{"model params", func(s *Studio, id string) error { return s.SetProjectModelParams(id, 0.9, 8192) }},
		{"thinking", func(s *Studio, id string) error { return s.SetProjectThinking(id, "enabled", 4096) }},
		{"permission", func(s *Studio, id string) error { return s.SetProjectPermissionMode(id, "ask") }},
		{"budget", func(s *Studio, id string) error { return s.SetProjectBudget(id, 25) }},
		{"budget enforcement", func(s *Studio, id string) error { return s.SetProjectEnforceBudget(id, true) }},
		{"pin", func(s *Studio, id string) error { return s.SetProjectPinned(id, true) }},
		{"rename", func(s *Studio, id string) error { return s.RenameProject(id, "Replacement") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStudioForTest(t)
			info := addTestProject(t, s, "Original")
			before, err := s.GetProject(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			provider := &mockClient{}
			p := s.projects[info.ID]
			p.mu.Lock()
			p.client = provider
			p.mu.Unlock()

			blocked := filepath.Join(t.TempDir(), "not-a-directory")
			if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GOKIN_CONFIG_DIR", blocked)
			if err := tc.call(s, info.ID); err == nil {
				t.Fatal("expected persistence failure")
			}
			after, err := s.GetProject(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("project changed after failed commit:\nbefore=%+v\nafter=%+v", before, after)
			}
			provider.mu.Lock()
			closeCalls := provider.closeCalls
			provider.mu.Unlock()
			p.mu.RLock()
			retained := p.client == provider
			p.mu.RUnlock()
			if closeCalls != 0 || !retained {
				t.Fatalf("cached client changed after failed commit: closes=%d retained=%v", closeCalls, retained)
			}
		})
	}
}

func TestRemoveProjectPersistenceFailurePreservesProjectAndHistory(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Keep Me")
	configRoot := configDir()
	key := projectSessionStorageKey(info.ID, "default")
	if err := SaveHistoryWithName(key, "Important", nil); err != nil {
		t.Fatal(err)
	}
	historyFile := historyPath(key)
	provider := &mockClient{}
	p := s.projects[info.ID]
	p.mu.Lock()
	p.client = provider
	p.mu.Unlock()

	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOKIN_CONFIG_DIR", blocked)
	if err := s.RemoveProject(info.ID); err == nil {
		t.Fatal("expected persistence failure")
	}
	if _, err := s.GetProject(info.ID); err != nil {
		t.Fatalf("project disappeared from memory: %v", err)
	}
	provider.mu.Lock()
	closeCalls := provider.closeCalls
	provider.mu.Unlock()
	if closeCalls != 0 {
		t.Fatalf("provider closed after failed removal: %d", closeCalls)
	}
	if _, err := os.Stat(historyFile); err != nil {
		t.Fatalf("history deleted after failed removal: %v", err)
	}
	if err := os.Setenv("GOKIN_CONFIG_DIR", configRoot); err != nil {
		t.Fatal(err)
	}
	reloaded := LoadConfig()
	found := false
	for _, pc := range reloaded.Projects {
		found = found || pc.ID == info.ID
	}
	if !found {
		t.Fatal("project missing from durable config after failed removal")
	}
}
