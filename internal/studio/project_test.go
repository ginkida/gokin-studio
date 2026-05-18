package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// addTestProject is a helper that calls AddProject with a fresh TempDir and
// fails the test immediately if it returns an error.
func addTestProject(t *testing.T, s *Studio, name string) *ProjectInfo {
	t.Helper()
	info, err := s.AddProject(name, t.TempDir())
	if err != nil {
		t.Fatalf("AddProject(%q): %v", name, err)
	}
	return info
}

// TestAddProject_RejectsInvalidInputs verifies name and directory validation.
func TestAddProject_RejectsInvalidInputs(t *testing.T) {
	s := newStudioForTest(t)

	cases := []struct {
		name string
		dir  string
	}{
		{"", t.TempDir()},              // empty name
		{"   ", t.TempDir()},           // whitespace-only name
		{"Proj", "/no/such/path/xyz"},  // non-existent directory
	}
	for _, c := range cases {
		if _, err := s.AddProject(c.name, c.dir); err == nil {
			t.Errorf("AddProject(%q, %q): expected error, got nil", c.name, c.dir)
		}
	}
}

// TestAddProject_RejectsDuplicateDirectory ensures the same path can't be added twice.
func TestAddProject_RejectsDuplicateDirectory(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()

	if _, err := s.AddProject("First", dir); err != nil {
		t.Fatalf("AddProject first: %v", err)
	}
	if _, err := s.AddProject("Second", dir); err == nil {
		t.Error("expected error for duplicate directory, got nil")
	}
}

// TestAddProject_TruncatesLongName verifies names over 60 chars are capped.
func TestAddProject_TruncatesLongName(t *testing.T) {
	s := newStudioForTest(t)
	longName := strings.Repeat("x", 80) // 80 chars, 20 over the 60-char cap

	info, err := s.AddProject(longName, t.TempDir())
	if err != nil {
		t.Fatalf("AddProject with long name: %v", err)
	}
	if len(info.Name) > 60 {
		t.Errorf("name length %d exceeds cap of 60: %q", len(info.Name), info.Name)
	}
}

// TestAddProject_TrimsName verifies leading/trailing whitespace is stripped.
func TestAddProject_TrimsName(t *testing.T) {
	s := newStudioForTest(t)
	info, err := s.AddProject("  My Project  ", t.TempDir())
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if info.Name != "My Project" {
		t.Errorf("name = %q, want 'My Project'", info.Name)
	}
}

// TestRenameProject_TruncatesLongName verifies that names over 60 chars are
// silently truncated to 60 — matching the same cap applied in AddProject —
// so the sidebar never overflows with extremely long names.
func TestRenameProject_TruncatesLongName(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Proj")
	longName := strings.Repeat("z", 80)
	if err := s.RenameProject(info.ID, longName); err != nil {
		t.Fatalf("RenameProject with long name: %v", err)
	}
	got, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if len(got.Name) > 60 {
		t.Errorf("name length %d exceeds cap of 60: %q", len(got.Name), got.Name)
	}
}

// TestRenameProject_ValidationAndEffect covers both error and success paths.
func TestRenameProject_ValidationAndEffect(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Original")

	// Unknown project ID
	if err := s.RenameProject("no-such-id", "New"); err == nil {
		t.Error("expected error for unknown project ID, got nil")
	}

	// Empty name
	if err := s.RenameProject(info.ID, ""); err == nil {
		t.Error("expected error for empty new name, got nil")
	}

	// Whitespace-only name
	if err := s.RenameProject(info.ID, "   "); err == nil {
		t.Error("expected error for whitespace-only new name, got nil")
	}

	// Valid rename
	if err := s.RenameProject(info.ID, "Renamed"); err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	got, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject after rename: %v", err)
	}
	if got.Name != "Renamed" {
		t.Errorf("name after rename = %q, want 'Renamed'", got.Name)
	}
}

// TestRemoveProject_ValidationAndEffect covers both error and success paths.
func TestRemoveProject_ValidationAndEffect(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "ToRemove")

	// Unknown project ID
	if err := s.RemoveProject("no-such-id"); err == nil {
		t.Error("expected error for unknown project ID, got nil")
	}

	// Valid removal
	if err := s.RemoveProject(info.ID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}

	// Project is gone
	if _, err := s.GetProject(info.ID); err == nil {
		t.Error("expected error from GetProject after removal, got nil")
	}
	if projects := s.ListProjects(); len(projects) != 0 {
		t.Errorf("expected 0 projects after removal, got %d", len(projects))
	}
}

// TestSetProjectSystemPrompt verifies the prompt is stored and retrievable.
func TestSetProjectSystemPrompt(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Proj")

	// Unknown project
	if err := s.SetProjectSystemPrompt("no-such-id", "hello"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}

	// Valid set
	const prompt = "You are a concise assistant."
	if err := s.SetProjectSystemPrompt(info.ID, prompt); err != nil {
		t.Fatalf("SetProjectSystemPrompt: %v", err)
	}
	got, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.SystemPrompt != prompt {
		t.Errorf("system prompt = %q, want %q", got.SystemPrompt, prompt)
	}
}

// TestSetProjectProvider verifies that the provider/model are stored and the
// client cache is invalidated so the new provider takes effect on the next send.
func TestSetProjectProvider(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Proj")

	// Unknown project
	if err := s.SetProjectProvider("no-such-id", "kimi", "kimi-for-coding"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}

	// Valid change: store a non-nil mock client first so we can verify it gets nil'd.
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	p.mu.Lock()
	p.client = &mockClient{}
	p.mu.Unlock()

	if err := s.SetProjectProvider(info.ID, "kimi", "kimi-for-coding"); err != nil {
		t.Fatalf("SetProjectProvider: %v", err)
	}
	got, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Provider != "kimi" {
		t.Errorf("Provider = %q, want 'kimi'", got.Provider)
	}
	if got.Model != "kimi-for-coding" {
		t.Errorf("Model = %q, want 'kimi-for-coding'", got.Model)
	}
	// Client must be nil so the next send picks up the new provider.
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()
	if c != nil {
		t.Error("expected p.client to be nil after SetProjectProvider, but it is non-nil")
	}
}

// TestSetProjectModelParams verifies that temperature and maxTokens are stored
// and the client cache is invalidated.
func TestSetProjectModelParams(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Proj")

	// Unknown project
	if err := s.SetProjectModelParams("no-such-id", 0.5, 2048); err == nil {
		t.Error("expected error for unknown project, got nil")
	}

	if err := s.SetProjectModelParams(info.ID, 0.7, 4096); err != nil {
		t.Fatalf("SetProjectModelParams: %v", err)
	}

	// Verify via the raw project struct (ProjectInfo doesn't expose these fields).
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	p.mu.RLock()
	temp := p.Temperature
	maxTok := p.MaxTokens
	p.mu.RUnlock()

	if temp != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", temp)
	}
	if maxTok != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", maxTok)
	}
}

// TestGetProject_DirectoryOK verifies that ProjectInfo.DirectoryOK reflects
// the real filesystem state so the frontend can warn when a project directory
// goes missing (user moved or deleted it outside the app).
func TestGetProject_DirectoryOK(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()

	info, err := s.AddProject("DirTest", dir)
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	// Directory exists → DirectoryOK must be true.
	got, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if !got.DirectoryOK {
		t.Errorf("DirectoryOK = false for existing directory %q, want true", dir)
	}

	// Remove the directory to simulate the user deleting it outside the app.
	if err := os.Remove(dir); err != nil {
		t.Fatalf("os.Remove(%q): %v", dir, err)
	}

	got, err = s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject after directory removal: %v", err)
	}
	if got.DirectoryOK {
		t.Errorf("DirectoryOK = true after directory was removed, want false")
	}
}

// TestSetProjectThinking validates mode/budget constraints and applies the change.
func TestSetProjectThinking(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Proj")

	// Invalid mode string
	if err := s.SetProjectThinking(info.ID, "always", 0); err == nil {
		t.Error("expected error for invalid mode 'always', got nil")
	}

	// Negative budget
	if err := s.SetProjectThinking(info.ID, "enabled", -1); err == nil {
		t.Error("expected error for negative budget, got nil")
	}

	// Unknown project
	if err := s.SetProjectThinking("no-such-id", "enabled", 0); err == nil {
		t.Error("expected error for unknown project, got nil")
	}

	// Valid: enabled with a custom budget
	if err := s.SetProjectThinking(info.ID, "enabled", 8192); err != nil {
		t.Fatalf("SetProjectThinking enabled: %v", err)
	}
	got, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.ThinkingMode != "enabled" {
		t.Errorf("thinking mode = %q, want 'enabled'", got.ThinkingMode)
	}
	if got.ThinkingBudget != 8192 {
		t.Errorf("thinking budget = %d, want 8192", got.ThinkingBudget)
	}

	// Valid: disabled clears the budget
	if err := s.SetProjectThinking(info.ID, "disabled", 0); err != nil {
		t.Fatalf("SetProjectThinking disabled: %v", err)
	}
	got, err = s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.ThinkingMode != "disabled" {
		t.Errorf("thinking mode = %q, want 'disabled'", got.ThinkingMode)
	}
}

// TestClearPinnedContext verifies that ClearPinnedContext clears the in-memory
// pinnedContext, removes the disk file, and returns an error for unknown projects.
func TestClearPinnedContext(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "PinTest")

	// Manually set pinnedContext on the project.
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	p.mu.Lock()
	p.pinnedContext = "important note"
	p.mu.Unlock()

	// Verify Info() surfaces the pinned content.
	got, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.PinnedContext != "important note" {
		t.Errorf("PinnedContext before clear = %q, want %q", got.PinnedContext, "important note")
	}

	// Clear it.
	if err := s.ClearPinnedContext(info.ID); err != nil {
		t.Fatalf("ClearPinnedContext: %v", err)
	}

	// Verify it's gone.
	got, err = s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject after clear: %v", err)
	}
	if got.PinnedContext != "" {
		t.Errorf("PinnedContext after clear = %q, want empty", got.PinnedContext)
	}

	// Unknown project returns error.
	if err := s.ClearPinnedContext("nonexistent"); err == nil {
		t.Error("expected error for unknown project ID, got nil")
	}
}

// TestClearPinnedContext_RemoveError verifies that ClearPinnedContext still
// clears the in-memory pin but returns an error when the disk file cannot be
// removed for a reason other than it not existing (e.g., it's a directory).
func TestClearPinnedContext_RemoveError(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()
	p := NewProject(ProjectConfig{ID: "pid-pin-err", Name: "P", Directory: dir})
	p.studio = s
	s.projects[p.ID] = p
	p.pinnedContext = "some note"

	// Place a non-empty directory at the path where the pin file should be.
	// os.Remove succeeds on empty directories (via rmdir), so we put a child
	// file inside to ensure rmdir fails with ENOTEMPTY (not os.IsNotExist).
	pinDir := filepath.Join(dir, ".gokin", "pinned_context.md")
	if err := os.MkdirAll(pinDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pinDir, "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile child: %v", err)
	}

	err := s.ClearPinnedContext(p.ID)
	if err == nil {
		t.Fatal("expected error when disk remove fails, got nil")
	}
	if !strings.Contains(err.Error(), "could not remove disk file") {
		t.Errorf("error = %q, want 'could not remove disk file'", err)
	}

	// In-memory pin must be cleared even when the disk op fails.
	p.mu.RLock()
	remaining := p.pinnedContext
	p.mu.RUnlock()
	if remaining != "" {
		t.Errorf("pinnedContext after error = %q, want empty", remaining)
	}
}
