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
		{"", t.TempDir()},             // empty name
		{"   ", t.TempDir()},          // whitespace-only name
		{"Proj", ""},                  // empty path must not resolve to cwd
		{"Proj", "   "},               // whitespace-only path
		{"Proj", "/no/such/path/xyz"}, // non-existent directory
	}
	for _, c := range cases {
		if _, err := s.AddProject(c.name, c.dir); err == nil {
			t.Errorf("AddProject(%q, %q): expected error, got nil", c.name, c.dir)
		}
	}
}

func TestAddProject_RejectsSymlinkAliasOfRegisteredDirectory(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()
	if _, err := s.AddProject("Real", dir); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddProject("Alias", alias); err == nil {
		t.Fatal("symlink alias registered the same workspace twice")
	}
}

func TestAddProject_PersistenceFailureDoesNotPublish(t *testing.T) {
	s := newStudioForTest(t)
	workspace := t.TempDir()
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := os.Getenv("GOKIN_CONFIG_DIR")
	if err := os.Setenv("GOKIN_CONFIG_DIR", blocked); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", previous) })

	if info, err := s.AddProject("Ghost", workspace); err == nil || info != nil {
		t.Fatalf("AddProject() = %#v, %v; want persistence failure", info, err)
	}
	if projects := s.ListProjects(); len(projects) != 0 {
		t.Fatalf("failed project was published: %+v", projects)
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

func TestSetProjectProvider_RejectsUnsupportedProviderOrModel(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Provider contract")
	for _, tc := range []struct{ provider, model string }{
		{"ollama", "llama3.1"},
		{"deepseek", "deepseek-v4-pro"},
		{"glm", "glm-6-unknown"},
		{"kimi", "not-a-kimi-model"},
	} {
		if err := s.SetProjectProvider(info.ID, tc.provider, tc.model); err == nil {
			t.Errorf("SetProjectProvider(%q, %q) unexpectedly succeeded", tc.provider, tc.model)
		}
	}
}

func TestConfigureProjectModel_CommitsOneCompleteSnapshot(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Atomic model config")

	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	p.mu.Lock()
	p.client = &mockClient{}
	p.mu.Unlock()

	result, err := s.ConfigureProjectModel(info.ID, " KIMI ", "k3", 0.4, 65536, "enabled", 32768)
	if err != nil {
		t.Fatalf("ConfigureProjectModel: %v", err)
	}
	if result == nil || result.Provider != "kimi" || result.Model != "k3" || !result.ThinkingActive || result.ThinkingBudgetEffective != 32768 {
		t.Fatalf("returned resolved project snapshot = %+v", result)
	}
	p.mu.RLock()
	provider, model := p.Provider, p.Model
	temperature, maxTokens := p.Temperature, p.MaxTokens
	mode, budget := p.ThinkingMode, p.ThinkingBudget
	cachedClient := p.client
	p.mu.RUnlock()
	if provider != "kimi" || model != "k3" || temperature != 0.4 || maxTokens != 65536 || mode != "enabled" || budget != 32768 {
		t.Fatalf("project snapshot = %s/%s temp=%v max=%d thinking=%s/%d", provider, model, temperature, maxTokens, mode, budget)
	}
	if cachedClient != nil {
		t.Fatal("cached client was not invalidated")
	}

	var persisted *ProjectConfig
	for i := range s.config.Projects {
		if s.config.Projects[i].ID == info.ID {
			persisted = &s.config.Projects[i]
			break
		}
	}
	if persisted == nil {
		t.Fatal("project missing from persisted snapshot")
	}
	if persisted.Provider != provider || persisted.Model != model || persisted.Temperature != temperature ||
		persisted.MaxTokens != maxTokens || persisted.ThinkingMode != mode || persisted.ThinkingBudget != budget {
		t.Fatalf("persisted snapshot diverged: %+v", *persisted)
	}
}

func TestConfigureProjectModel_ValidationIsAtomic(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Atomic validation")
	before, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatal(err)
	}

	// k3 allows at most 131072 output tokens. No earlier field may publish
	// when this last-stage validation fails.
	if _, err := s.ConfigureProjectModel(info.ID, "kimi", "k3", 0.5, 131073, "enabled", 8192); err == nil {
		t.Fatal("expected max-token validation failure")
	}
	after, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Provider != after.Provider || before.Model != after.Model ||
		before.Temperature != after.Temperature || before.MaxTokens != after.MaxTokens ||
		before.ThinkingMode != after.ThinkingMode || before.ThinkingBudget != after.ThinkingBudget {
		t.Fatalf("project changed after rejected configuration:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestProjectModelConfigurationRejectsActiveWork(t *testing.T) {
	for _, busyState := range []string{"active turn", "claimed queue worker"} {
		t.Run(busyState, func(t *testing.T) {
			s := newStudioForTest(t)
			info := addTestProject(t, s, "Busy model config")
			s.mu.RLock()
			project := s.projects[info.ID]
			s.mu.RUnlock()
			session := project.GetSession("default")
			session.mu.Lock()
			if busyState == "active turn" {
				session.active = true
			} else {
				session.queueWorker = true
			}
			session.mu.Unlock()
			t.Cleanup(func() {
				session.mu.Lock()
				session.active = false
				session.queueWorker = false
				session.mu.Unlock()
			})

			operations := []struct {
				name string
				run  func() error
			}{
				{"provider", func() error { return s.SetProjectProvider(info.ID, "kimi", "k3") }},
				{"atomic config", func() error {
					_, err := s.ConfigureProjectModel(info.ID, "kimi", "k3", 0.4, 65536, "enabled", 8192)
					return err
				}},
				{"parameters", func() error { return s.SetProjectModelParams(info.ID, 0.5, 8192) }},
				{"reasoning", func() error { return s.SetProjectThinking(info.ID, "enabled", 8192) }},
			}
			for _, operation := range operations {
				if err := operation.run(); err == nil || !strings.Contains(err.Error(), "stop all running chats") {
					t.Errorf("%s during %s error = %v", operation.name, busyState, err)
				}
			}

			project.mu.RLock()
			provider, model := project.Provider, project.Model
			temperature, maxTokens := project.Temperature, project.MaxTokens
			mode, budget := project.ThinkingMode, project.ThinkingBudget
			project.mu.RUnlock()
			if provider != "glm" || model != "glm-5.2" || temperature != 0 || maxTokens != 0 || mode != "" || budget != 0 {
				t.Fatalf("busy model settings mutated: %s/%s temp=%v max=%d thinking=%s/%d", provider, model, temperature, maxTokens, mode, budget)
			}
		})
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

	if err := s.SetProjectModelParams(info.ID, 0.7, 131_073); err == nil {
		t.Fatal("model-specific maximum output limit was not enforced")
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

func TestRelinkProjectDirectoryPreservesProjectIdentityAndSettings(t *testing.T) {
	s := newStudioForTest(t)
	oldDir := t.TempDir()
	info, err := s.AddProject("Moved", oldDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfigureProjectModel(info.ID, "kimi", "k3", 0.4, 65536, "enabled", 8192); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(oldDir); err != nil {
		t.Fatal(err)
	}
	newDir := t.TempDir()
	got, err := s.RelinkProjectDirectory(info.ID, newDir)
	if err != nil {
		t.Fatalf("RelinkProjectDirectory: %v", err)
	}
	wantDir, _ := filepath.Abs(newDir)
	if got.ID != info.ID || got.Name != "Moved" || got.Directory != wantDir || !got.DirectoryOK {
		t.Fatalf("relinked project identity/path = %+v", got)
	}
	if got.Provider != "kimi" || got.Model != "k3" || got.Temperature != 0.4 || got.MaxTokens != 65536 || got.ThinkingBudget != 8192 {
		t.Fatalf("relinked project settings changed: %+v", got)
	}
	if len(s.projects[info.ID].sessions) == 0 {
		t.Fatal("relink discarded chat sessions")
	}
	var persisted *ProjectConfig
	for index := range s.config.Projects {
		if s.config.Projects[index].ID == info.ID {
			persisted = &s.config.Projects[index]
			break
		}
	}
	if persisted == nil || persisted.Directory != wantDir {
		t.Fatalf("persisted directory = %+v, want %q", persisted, wantDir)
	}
}

func TestRelinkProjectDirectoryRejectsDuplicateAndActiveProject(t *testing.T) {
	s := newStudioForTest(t)
	first := addTestProject(t, s, "First")
	second := addTestProject(t, s, "Second")
	if _, err := s.RelinkProjectDirectory(first.ID, second.Directory); err == nil {
		t.Fatal("duplicate registered directory was accepted")
	}
	p := s.projects[first.ID]
	session := p.sessions["default"]
	session.mu.Lock()
	session.active = true
	session.mu.Unlock()
	if _, err := s.RelinkProjectDirectory(first.ID, t.TempDir()); err == nil || !strings.Contains(err.Error(), "running chats") {
		t.Fatalf("active project relink error = %v", err)
	}
	session.mu.Lock()
	session.active = false
	session.mu.Unlock()
	got, err := s.GetProject(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Directory != first.Directory {
		t.Fatalf("failed relink changed directory to %q", got.Directory)
	}
}

func TestRelinkProjectDirectoryFailureIsAtomic(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Atomic relink")
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := os.Getenv("GOKIN_CONFIG_DIR")
	if err := os.Setenv("GOKIN_CONFIG_DIR", blocked); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", previous) })
	if result, err := s.RelinkProjectDirectory(info.ID, t.TempDir()); err == nil || result != nil {
		t.Fatalf("RelinkProjectDirectory() = %+v, %v; want persistence failure", result, err)
	}
	got, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Directory != info.Directory {
		t.Fatalf("failed relink published directory %q, want %q", got.Directory, info.Directory)
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

// TestClearPinnedContext_RemoveError verifies that ClearPinnedContext preserves
// the in-memory pin when its persisted copy cannot be removed. The two views
// must never disagree about whether the pin is active.
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
	if !strings.Contains(err.Error(), "could not remove pinned context from disk") {
		t.Errorf("error = %q, want disk removal context", err)
	}

	// The in-memory pin must remain active when the disk operation fails.
	p.mu.RLock()
	remaining := p.pinnedContext
	p.mu.RUnlock()
	if remaining != "some note" {
		t.Errorf("pinnedContext after error = %q, want original value", remaining)
	}
}
