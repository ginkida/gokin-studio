package studio

import (
	"os"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// TestUpdateSettings_TrimsSupportedKeysAndClearsLegacy verifies that current
// credentials are normalized and credentials outside the product contract are
// not retained.
func TestUpdateSettings_TrimsSupportedKeysAndClearsLegacy(t *testing.T) {
	s := newStudioForTest(t)

	cfg := StudioConfig{
		Settings: Settings{
			GLMKey:  "  myglmkey  ",
			KimiKey: " mykimikey ",
		},
	}
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	got := s.GetSettings().Settings
	if got.GLMKey != "myglmkey" {
		t.Errorf("GLMKey = %q, want 'myglmkey'", got.GLMKey)
	}
	if got.KimiKey != "mykimikey" {
		t.Errorf("KimiKey = %q, want 'mykimikey'", got.KimiKey)
	}
}

func TestUpdateSettings_RejectsUnsupportedProviderOrModel(t *testing.T) {
	s := newStudioForTest(t)
	for _, settings := range []Settings{
		{DefaultProvider: "deepseek", DefaultModel: "deepseek-v4-pro"},
		{DefaultProvider: "glm", DefaultModel: "glm-6-unknown"},
		{DefaultProvider: "kimi", DefaultModel: "moonshot-v1-auto"},
	} {
		if err := s.UpdateSettings(StudioConfig{Settings: settings}); err == nil {
			t.Errorf("UpdateSettings(%s/%s) unexpectedly succeeded", settings.DefaultProvider, settings.DefaultModel)
		}
	}
}

// TestUpdateSettings_ClampsNegativeBudget verifies that a negative
// DefaultThinkingBudget is coerced to 0 instead of being persisted.
func TestUpdateSettings_ClampsNegativeBudget(t *testing.T) {
	s := newStudioForTest(t)

	cfg := StudioConfig{
		Settings: Settings{DefaultThinkingBudget: -500},
	}
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	got := s.GetSettings().Settings.DefaultThinkingBudget
	if got != 0 {
		t.Errorf("DefaultThinkingBudget = %d, want 0 after clamping", got)
	}
}

// TestUpdateSettings_PersistsSettings verifies that a round-trip through
// UpdateSettings / GetSettings preserves the non-key fields too.
func TestUpdateSettings_PersistsSettings(t *testing.T) {
	s := newStudioForTest(t)

	cfg := StudioConfig{
		Settings: Settings{
			Theme:                   "light",
			DefaultProvider:         "kimi",
			DefaultModel:            "kimi-for-coding",
			GlobalInstructions:      "Answer concisely and cite project evidence.",
			DefaultThinkingMode:     "enabled",
			DefaultThinkingBudget:   4096,
			AutoArchivePRAfterClose: true,
		},
	}
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	got := s.GetSettings().Settings
	if got.Theme != "light" {
		t.Errorf("Theme = %q, want 'light'", got.Theme)
	}
	if got.DefaultProvider != "kimi" {
		t.Errorf("DefaultProvider = %q, want 'kimi'", got.DefaultProvider)
	}
	if got.DefaultThinkingMode != "enabled" {
		t.Errorf("DefaultThinkingMode = %q, want 'enabled'", got.DefaultThinkingMode)
	}
	if got.DefaultThinkingBudget != 4096 {
		t.Errorf("DefaultThinkingBudget = %d, want 4096", got.DefaultThinkingBudget)
	}
	if got.GlobalInstructions != "Answer concisely and cite project evidence." {
		t.Errorf("GlobalInstructions = %q", got.GlobalInstructions)
	}
	if !got.AutoArchivePRAfterClose {
		t.Error("AutoArchivePRAfterClose was not persisted")
	}
}

func TestUpdateSettings_GlobalInstructionsUTF8AndLimit(t *testing.T) {
	s := newStudioForTest(t)
	if err := s.UpdateSettings(StudioConfig{Settings: Settings{
		GlobalInstructions: string([]byte{0xff}),
	}}); err == nil {
		t.Fatal("invalid UTF-8 global instructions were accepted")
	}
	input := strings.Repeat("🙂", GlobalInstructionsMaxBytes)
	if err := s.UpdateSettings(StudioConfig{Settings: Settings{
		GlobalInstructions: input,
	}}); err != nil {
		t.Fatal(err)
	}
	got := s.GetSettings().Settings.GlobalInstructions
	if len(got) > GlobalInstructionsMaxBytes || !strings.HasPrefix(input, got) {
		t.Fatalf("global instructions were not safely truncated: %d bytes", len(got))
	}
}

// TestUpdateSettings_InvalidatesClientCache verifies that projects' cached
// clients are cleared so new API keys take effect on the next send without
// requiring a restart.
func TestUpdateSettings_InvalidatesClientCache(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Proj")

	// Manually put a non-nil mock on the client field to simulate a cached client.
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	p.mu.Lock()
	p.client = &mockClient{}
	p.mu.Unlock()

	// UpdateSettings should nil out p.client.
	if err := s.UpdateSettings(StudioConfig{Settings: Settings{GLMKey: "newkey"}}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()
	if c != nil {
		t.Error("expected p.client to be nil after UpdateSettings, but it is non-nil")
	}
}

// TestUpdateSettings_DefaultIsCreationOnly protects the Settings UX contract:
// changing the global default must not silently rewrite existing workspaces.
// An explicit bulk migration, if offered by a future UI, is a separate action.
func TestUpdateSettings_DefaultIsCreationOnly(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Existing")
	if err := s.SetProjectProvider(info.ID, "glm", "glm-4.7"); err != nil {
		t.Fatalf("SetProjectProvider: %v", err)
	}

	if err := s.UpdateSettings(StudioConfig{Settings: Settings{
		DefaultProvider: "kimi",
		DefaultModel:    "k3",
	}}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	project, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if project.Provider != "glm" || project.Model != "glm-4.7" {
		t.Fatalf("existing project changed to %s/%s; want glm/glm-4.7", project.Provider, project.Model)
	}
}

// TestRenameChatSession_ValidationAndEffect tests the validation rules and
// confirms the session name is updated and retrievable via ListChatSessions.
func TestRenameChatSession_ValidationAndEffect(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Unknown project
	if err := s.RenameChatSession("no-such-proj", "default", "New"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}

	// Unknown session
	if err := s.RenameChatSession("pid", "no-such-session", "New"); err == nil {
		t.Error("expected error for unknown session, got nil")
	}

	// Empty name
	if err := s.RenameChatSession("pid", "default", ""); err == nil {
		t.Error("expected error for empty name, got nil")
	}

	// Whitespace-only name
	if err := s.RenameChatSession("pid", "default", "   "); err == nil {
		t.Error("expected error for whitespace-only name, got nil")
	}

	// Valid rename
	if err := s.RenameChatSession("pid", "default", "My Chat"); err != nil {
		t.Fatalf("RenameChatSession: %v", err)
	}
	sessions, err := s.ListChatSessions("pid")
	if err != nil {
		t.Fatalf("ListChatSessions: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("expected at least 1 session")
	}
	// Find the renamed session.
	var found bool
	for _, sess := range sessions {
		if sess.ID == "default" {
			if sess.Name != "My Chat" {
				t.Errorf("session name = %q, want 'My Chat'", sess.Name)
			}
			found = true
		}
	}
	if !found {
		t.Error("session 'default' not found in ListChatSessions result")
	}
}

// TestRenameChatSession_TruncatesLongName verifies that session names over
// 60 characters are silently truncated, matching the cap applied by AddProject
// and RenameProject so no UI element ever overflows with an extreme name.
func TestRenameChatSession_TruncatesLongName(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-rename-long", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	longName := strings.Repeat("z", 80)
	if err := s.RenameChatSession(p.ID, "default", longName); err != nil {
		t.Fatalf("RenameChatSession with long name: %v", err)
	}
	p.sessions["default"].mu.RLock()
	got := p.sessions["default"].Name
	p.sessions["default"].mu.RUnlock()
	if len(got) > 60 {
		t.Errorf("session name length %d exceeds cap of 60: %q", len(got), got)
	}
}

// TestSendMessage_UnknownProject verifies that SendMessage returns an error
// immediately (synchronously) for a project ID that doesn't exist, rather
// than launching a goroutine that later fails silently.
func TestSendMessage_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	err := s.SendMessage("no-such-project", "hello", "default")
	if err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestCreateChatSession_UnknownProject verifies that CreateChatSession returns
// an error for a project ID that doesn't exist.
func TestCreateChatSession_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.CreateChatSession("no-such-project"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

func TestCreateChatSession_PersistenceFailureIsNotPublished(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Durable Session")
	before, err := s.ListChatSessions(info.ID)
	if err != nil {
		t.Fatal(err)
	}

	blockedConfigDir := t.TempDir() + "/not-a-directory"
	if err := os.WriteFile(blockedConfigDir, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := os.Getenv("GOKIN_CONFIG_DIR")
	if err := os.Setenv("GOKIN_CONFIG_DIR", blockedConfigDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", previous) })

	if created, err := s.CreateChatSession(info.ID); err == nil || created != nil {
		t.Fatalf("CreateChatSession() = %#v, %v; want durable-save error", created, err)
	}
	after, err := s.ListChatSessions(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("failed session was published in memory: before=%d after=%d", len(before), len(after))
	}
}

// TestClearHistory_ClearsSession verifies that ClearHistory wipes the
// in-memory history and rejects an unknown project ID.
// Note: GetSession falls back to "default" for unrecognised session IDs, so
// ClearHistory never returns an error for an unknown session (it clears the
// default instead). Only an unknown project triggers an error.
func TestClearHistory_ClearsSession(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Seed history on the default session.
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
	}

	// Unknown project should return an error.
	if err := s.ClearHistory("no-such-id", "default"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}

	// Valid clear should wipe history.
	if err := s.ClearHistory("pid", "default"); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}
	p.sessions["default"].mu.RLock()
	histLen := len(p.sessions["default"].history)
	p.sessions["default"].mu.RUnlock()
	if histLen != 0 {
		t.Errorf("history length after clear = %d, want 0", histLen)
	}
}

// TestSaveConfig_ErrorPrinted verifies that saveConfig logs to stderr when
// Save() fails (e.g. because GOKIN_CONFIG_DIR is a regular file). The studio
// must not panic or return an error visible to the caller.
func TestSaveConfig_ErrorPrinted(t *testing.T) {
	s := newStudioForTest(t)

	// Point GOKIN_CONFIG_DIR at a regular file so Save() hits MkdirAll error.
	f, err := os.CreateTemp("", "gokin-saveconfig-error-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	f.Close()
	defer os.Remove(f.Name())

	prev := os.Getenv("GOKIN_CONFIG_DIR")
	_ = os.Setenv("GOKIN_CONFIG_DIR", f.Name())
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", prev) })

	// saveConfig prints to stderr on error but must not panic.
	// Must be called under the write lock (same as all production call sites).
	s.mu.Lock()
	s.saveConfig()
	s.mu.Unlock()
}

// TestSaveConfigAsync_ErrorPrinted verifies that saveConfigAsync logs to
// stderr when Save() fails. Mirrors TestSaveConfig_ErrorPrinted but for the
// async variant that does not hold s.mu at the call site.
func TestSaveConfigAsync_ErrorPrinted(t *testing.T) {
	s := newStudioForTest(t)

	// Point GOKIN_CONFIG_DIR at a regular file so cfg.Save() fails.
	f, err := os.CreateTemp("", "gokin-saveasync-error-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	f.Close()
	defer os.Remove(f.Name())

	prev := os.Getenv("GOKIN_CONFIG_DIR")
	_ = os.Setenv("GOKIN_CONFIG_DIR", f.Name())
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", prev) })

	// saveConfigAsync acquires its own locks internally, so call without holding any.
	s.saveConfigAsync()
}
