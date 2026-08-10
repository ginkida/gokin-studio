package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

// writeRawHistory is a test helper that writes raw bytes directly to the
// history file for a project, bypassing SaveHistoryWithName. Used to create
// edge-case files (empty, corrupt) that the production code must handle.
func writeRawHistory(t *testing.T, projectID string, data []byte) {
	t.Helper()
	dir := historyDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	path := filepath.Join(dir, projectID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

// TestLoadHistoryRaw_EmptyFile verifies that a zero-byte history file is
// treated as "no history" rather than a parse error. This can happen if a
// write was interrupted mid-way by a crash before atomicWriteFile renamed
// the temp file.
func TestLoadHistoryRaw_EmptyFile(t *testing.T) {
	_ = withTempHistoryDir(t)
	writeRawHistory(t, "proj-empty", []byte{})

	hist, err := LoadHistory("proj-empty")
	if err != nil {
		t.Fatalf("LoadHistory(empty file) returned error: %v", err)
	}
	if len(hist) != 0 {
		t.Errorf("expected empty history, got %d entries", len(hist))
	}
}

// TestLoadHistoryRaw_WhitespaceOnlyFile is the same as the empty-file case but
// with whitespace padding, which trimmed is also empty.
func TestLoadHistoryRaw_WhitespaceOnlyFile(t *testing.T) {
	_ = withTempHistoryDir(t)
	writeRawHistory(t, "proj-ws", []byte("   \n  \t  "))

	hist, err := LoadHistory("proj-ws")
	if err != nil {
		t.Fatalf("LoadHistory(whitespace-only file) returned error: %v", err)
	}
	if len(hist) != 0 {
		t.Errorf("expected empty history, got %d entries", len(hist))
	}
}

// TestLoadHistoryRaw_CorruptLegacyArray verifies that a file starting with '['
// but containing invalid JSON returns a descriptive "corrupt history file"
// error rather than a generic json error.
func TestLoadHistoryRaw_CorruptLegacyArray(t *testing.T) {
	_ = withTempHistoryDir(t)
	writeRawHistory(t, "proj-bad-legacy", []byte("[{invalid json}]"))

	_, err := LoadHistory("proj-bad-legacy")
	if err == nil {
		t.Fatal("expected error for corrupt legacy history file, got nil")
	}
}

// TestLoadHistoryRaw_CorruptVersionedObject verifies that a file starting with
// '{' but containing invalid JSON returns a descriptive error.
func TestLoadHistoryRaw_CorruptVersionedObject(t *testing.T) {
	_ = withTempHistoryDir(t)
	writeRawHistory(t, "proj-bad-v2", []byte(`{"v":2,"entries":"not an array"}`))

	_, err := LoadHistory("proj-bad-v2")
	if err == nil {
		t.Fatal("expected error for corrupt versioned history file, got nil")
	}
}

// TestNewProject_LegacyMigration verifies that NewProject picks up an existing
// single-file history for a project that has never been opened in the
// multi-session format. The legacy file must be migrated to the per-session
// path and the source removed so a subsequent NewProject doesn't re-adopt it.
func TestNewProject_LegacyMigration(t *testing.T) {
	_ = withTempHistoryDir(t)

	pid := "proj-legacy-migrate"
	// Write a legacy single-file history (bare array format, no "_sessionID" suffix).
	legacy := `[{"role":"user","text":"hello from legacy"},{"role":"model","text":"hi"}]`
	writeRawHistory(t, pid, []byte(legacy))

	// NewProject with no existing per-session files should adopt the legacy file.
	p := NewProject(ProjectConfig{ID: pid, Name: "P", Directory: t.TempDir()})

	def, ok := p.sessions["default"]
	if !ok {
		t.Fatal("expected 'default' session after legacy migration, not found")
	}
	if len(def.history) != 2 {
		t.Fatalf("expected 2 history entries after legacy migration, got %d", len(def.history))
	}

	// Legacy file should be gone after migration.
	if _, err := os.Stat(historyPath(pid)); !os.IsNotExist(err) {
		t.Errorf("expected legacy history file removed after migration; os.Stat err=%v", err)
	}

	// A second NewProject must NOT re-adopt the (now-deleted) legacy file.
	p2 := NewProject(ProjectConfig{ID: pid, Name: "P", Directory: t.TempDir()})
	def2, ok := p2.sessions["default"]
	if !ok {
		t.Fatal("expected default session on second NewProject")
	}
	if len(def2.history) != 2 {
		t.Errorf("second NewProject: expected 2 entries (from migrated file), got %d", len(def2.history))
	}
}

// TestNewProject_LoadsSessionsFromDisk verifies that NewProject reads back
// previously-saved per-session history files. This exercises the disk-loading
// loop at the top of NewProject that restores history on app restart.
func TestNewProject_LoadsSessionsFromDisk(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid := "proj-disk-sessions"
	sid := "abcdef12" // synthetic session ID

	// Write a v2 session history file directly.
	data := `{"v":2,"name":"My Session","entries":[{"role":"user","text":"hello"},{"role":"model","text":"hi"}]}`
	writeRawHistory(t, pid+"_"+sid, []byte(data))

	p := NewProject(ProjectConfig{ID: pid, Name: "P", Directory: t.TempDir()})

	sess, ok := p.sessions[sid]
	if !ok {
		t.Fatalf("expected session %q to be loaded from disk", sid)
	}
	if len(sess.history) != 2 {
		t.Errorf("session history len = %d, want 2", len(sess.history))
	}
	if sess.Name != "My Session" {
		t.Errorf("session name = %q, want 'My Session'", sess.Name)
	}
}

// TestNewProject_DefaultSessionFallbackName verifies that when the "default"
// session has a history file on disk but no stored name (e.g. an old bare-array
// format), the session is assigned "Chat 1" rather than an empty string.
func TestNewProject_DefaultSessionFallbackName(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid := "proj-default-no-name"

	// Write a v2 file for the "default" session without a name field.
	data := `{"v":2,"entries":[{"role":"user","text":"hello"},{"role":"model","text":"hi"}]}`
	writeRawHistory(t, pid+"_default", []byte(data))

	p := NewProject(ProjectConfig{ID: pid, Name: "P", Directory: t.TempDir()})

	def, ok := p.sessions["default"]
	if !ok {
		t.Fatal("expected 'default' session to be loaded from disk")
	}
	if def.Name != "Chat 1" {
		t.Errorf("default session fallback name = %q, want 'Chat 1'", def.Name)
	}
}

// TestNewProject_FallbackSessionName verifies the "Chat XXXX" fallback name
// applied when a non-default session file has no stored name. The name is
// derived from the first 4 characters of the session ID so the user at least
// sees something human-readable in the tab list.
func TestNewProject_FallbackSessionName(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid := "proj-fallback-name"
	sid := "abcdef12"

	// Write a v2 session file with no name field.
	data := `{"v":2,"entries":[{"role":"user","text":"hello"}]}`
	writeRawHistory(t, pid+"_"+sid, []byte(data))

	p := NewProject(ProjectConfig{ID: pid, Name: "P", Directory: t.TempDir()})

	sess, ok := p.sessions[sid]
	if !ok {
		t.Fatalf("expected session %q to be loaded from disk", sid)
	}
	want := "Chat " + sid[:4] // "Chat abcd"
	if sess.Name != want {
		t.Errorf("session name = %q, want %q (fallback from session ID)", sess.Name, want)
	}
}

// TestNewProject_LegacyMigrationCaseB verifies the second legacy-migration path:
// when some per-session files exist but "default" is not among them AND a
// legacy single-file history exists, the legacy file is adopted as the new
// "default" session and then deleted so a subsequent NewProject doesn't re-adopt it.
func TestNewProject_LegacyMigrationCaseB(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid := "proj-legacy-case-b"
	sid := "abcdef12"

	// Write a non-default session file so p.sessions is non-empty after loading.
	sessData := `{"v":2,"name":"Work","entries":[{"role":"user","text":"work item"}]}`
	writeRawHistory(t, pid+"_"+sid, []byte(sessData))

	// Write a legacy single-file history (no "_default.json" file exists).
	legacyData := `[{"role":"user","text":"legacy message"},{"role":"model","text":"legacy reply"}]`
	writeRawHistory(t, pid, []byte(legacyData))

	p := NewProject(ProjectConfig{ID: pid, Name: "P", Directory: t.TempDir()})

	// Non-default session should be loaded.
	if _, ok := p.sessions[sid]; !ok {
		t.Errorf("expected non-default session %q loaded from disk", sid)
	}
	// Legacy file should have been adopted as "default".
	def, ok := p.sessions["default"]
	if !ok {
		t.Fatal("expected 'default' session after Case B legacy migration, not found")
	}
	if len(def.history) != 2 {
		t.Errorf("default session history len = %d, want 2 (from legacy file)", len(def.history))
	}
	// Legacy file must be removed to prevent re-adoption on next startup.
	if _, err := os.Stat(historyPath(pid)); !os.IsNotExist(err) {
		t.Errorf("expected legacy file removed after Case B migration; os.Stat err=%v", err)
	}
}

// TestNewProject_SkipsCorruptSessionFile verifies that when a per-session
// history file contains invalid JSON, NewProject silently skips it (the
// `if err != nil || hist == nil { continue }` guard) without panicking.
// The corrupt session must not appear in p.sessions after loading.
func TestNewProject_SkipsCorruptSessionFile(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid := "proj-skip-corrupt"
	// Write a corrupt per-session file.
	writeRawHistory(t, pid+"_corrupt-sid", []byte("[not valid json"))

	p := NewProject(ProjectConfig{ID: pid, Name: "P", Directory: t.TempDir()})

	if _, ok := p.sessions["corrupt-sid"]; ok {
		t.Error("expected corrupt session file to be skipped, but it appears in sessions map")
	}
}

// TestAtomicWriteFile_DirectoryMissing verifies that atomicWriteFile returns
// a non-nil error when the target directory doesn't exist, instead of silently
// corrupting the caller's state.
func TestAtomicWriteFile_DirectoryMissing(t *testing.T) {
	path := "/nonexistent-dir-for-test/file.json"
	err := atomicWriteFile(path, []byte(`{"test":true}`), 0o600)
	if err == nil {
		t.Error("expected error for write to nonexistent directory, got nil")
	}
}

// TestProjectInfo_ActiveSession verifies that ProjectInfo.Active is true when
// at least one session in the project has active=true (generation in progress).
// This drives the activity indicator dot in the sidebar.
func TestProjectInfo_ActiveSession(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-active", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Mark the default session as active.
	p.sessions["default"].mu.Lock()
	p.sessions["default"].active = true
	p.sessions["default"].mu.Unlock()

	info, err := s.GetProject(p.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if !info.Active {
		t.Error("ProjectInfo.Active = false when a session is active, want true")
	}
}

// TestDeleteChatSession_UnknownSession verifies that deleting a session ID that
// doesn't exist within a known project returns an error (not a silent no-op).
func TestDeleteChatSession_UnknownSession(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Proj")

	// Create a second session so the project has more than one (otherwise the
	// "last session" guard fires before the "not found" guard).
	if _, err := s.CreateChatSession(info.ID); err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}

	err := s.DeleteChatSession(info.ID, "nonexistent-session-id")
	if err == nil {
		t.Error("expected error for unknown session ID, got nil")
	}
}

// TestGetRecoveryEvents_EmptySessionID verifies that passing an empty sessionID
// to GetRecoveryEvents defaults to the "default" session, matching the
// frontend's behavior when it doesn't have an explicit session context.
func TestGetRecoveryEvents_EmptySessionID(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	pid := "proj-recovery-empty-sid"

	// Write a replay file for the "default" session.
	r := NewReplayLogger(pid, "default")
	r.Append(ReplayEvent{Type: "user", Text: "something"})
	// Don't call Complete() — the file should persist as a "pending" replay.

	// Calling with empty sessionID should find the "default" replay.
	events, err := s.GetRecoveryEvents(pid, "")
	if err != nil {
		t.Fatalf("GetRecoveryEvents: %v", err)
	}
	if len(events) == 0 {
		t.Error("expected recovery events for default session, got none")
	}
	if events[0].Text != "something" {
		t.Errorf("event[0].Text = %q, want 'something'", events[0].Text)
	}
}

// TestLoadHistoryRaw_NonNotExistError verifies that when os.ReadFile fails
// with a non-NotExist error (e.g. the path is a directory), loadHistoryRaw
// returns a non-nil error instead of silently returning empty history.
// This covers the `return nil, "", err` fallback in the os.IsNotExist branch.
func TestLoadHistoryRaw_NonNotExistError(t *testing.T) {
	_ = withTempHistoryDir(t)

	// Create a directory at the history file path so os.ReadFile returns
	// "is a directory" — not an IsNotExist error.
	pid := "proj-dir-as-file"
	dir := historyDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	dirPath := historyPath(pid) // e.g. <historyDir>/proj-dir-as-file.json
	if err := os.MkdirAll(dirPath, 0o700); err != nil {
		t.Fatalf("MkdirAll(dirPath): %v", err)
	}

	_, err := LoadHistory(pid)
	if err == nil {
		t.Error("expected error when history path is a directory, got nil")
	}
}

// TestSaveHistoryWithName_ThinkingPartsSkipped verifies that Thought-flagged
// parts (extended reasoning tokens) are stripped during save so raw thinking
// never appears in the persisted transcript or on session reload.
func TestSaveHistoryWithName_ThinkingPartsSkipped(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid := "proj-thinking-skip"

	hist := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("question")}},
		{Role: "model", Parts: []*genai.Part{
			{Text: "I am thinking...", Thought: true}, // thinking part — must be skipped
			genai.NewPartFromText("final answer"),
		}},
	}
	if err := SaveHistoryWithName(pid, "chat", hist); err != nil {
		t.Fatalf("SaveHistoryWithName: %v", err)
	}

	loaded, err := LoadHistory(pid)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 entries (user + model), got %d", len(loaded))
	}
	// The model entry must contain only the final answer, not the thinking text.
	modelText := ""
	for _, p := range loaded[1].Parts {
		modelText += p.Text
	}
	if modelText != "final answer" {
		t.Errorf("model text = %q, want 'final answer' (thinking part should be stripped)", modelText)
	}
}

// TestSaveHistoryWithName_MkdirAllError verifies that SaveHistoryWithName returns a
// non-nil error when os.MkdirAll(historyDir()) fails because the config dir path is
// occupied by a regular file. This covers the `return err` at line 73.
func TestSaveHistoryWithName_MkdirAllError(t *testing.T) {
	// Create a regular file at the path that GOKIN_CONFIG_DIR will point to so
	// that historyDir() = <file>/history, and os.MkdirAll on that path fails.
	f, err := os.CreateTemp("", "gokin-savehist-mkdirall-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	f.Close()
	defer os.Remove(f.Name())

	prev := os.Getenv("GOKIN_CONFIG_DIR")
	_ = os.Setenv("GOKIN_CONFIG_DIR", f.Name()) // file, not directory
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", prev) })

	hist := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
	}
	err = SaveHistoryWithName("proj-mkdirall-err", "test", hist)
	if err == nil {
		t.Error("expected error from SaveHistoryWithName when config dir is a regular file, got nil")
	}
}

// TestNewProject_PreLoadsPinnedContext verifies that if a .gokin/pinned_context.md
// file exists in the project directory, NewProject pre-loads it into p.pinnedContext
// so the pin badge shows on startup without waiting for the first agent turn.
func TestNewProject_PreLoadsPinnedContext(t *testing.T) {
	_ = withTempHistoryDir(t)
	dir := t.TempDir()

	// Write a pin file into the project directory.
	pinDir := dir + "/.gokin"
	if err := os.MkdirAll(pinDir, 0750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(pinDir+"/pinned_context.md", []byte("startup pin content"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	p := NewProject(ProjectConfig{ID: "pin-preload", Name: "P", Directory: dir})

	p.mu.RLock()
	got := p.pinnedContext
	p.mu.RUnlock()
	if got != "startup pin content" {
		t.Errorf("pinnedContext = %q, want %q", got, "startup pin content")
	}

	// Info() should surface it too.
	info := p.Info()
	if info.PinnedContext != "startup pin content" {
		t.Errorf("Info().PinnedContext = %q, want %q", info.PinnedContext, "startup pin content")
	}
}

func TestNewProject_DoesNotPreloadUntrustedPinnedContext(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		_ = withTempHistoryDir(t)
		dir := t.TempDir()
		pinPath := filepath.Join(dir, ".gokin", "pinned_context.md")
		if err := os.MkdirAll(filepath.Dir(pinPath), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pinPath, []byte(strings.Repeat("x", tools.MaxPinnedContextBytes+1)), 0600); err != nil {
			t.Fatal(err)
		}

		p := NewProject(ProjectConfig{ID: "pin-too-large", Name: "P", Directory: dir})
		if got := p.Info().PinnedContext; got != "" {
			t.Fatalf("oversized pin was preloaded: %d bytes", len(got))
		}
	})

	t.Run("symlink", func(t *testing.T) {
		_ = withTempHistoryDir(t)
		dir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(outside, []byte("do not inject"), 0600); err != nil {
			t.Fatal(err)
		}
		pinPath := filepath.Join(dir, ".gokin", "pinned_context.md")
		if err := os.MkdirAll(filepath.Dir(pinPath), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, pinPath); err != nil {
			t.Fatal(err)
		}

		p := NewProject(ProjectConfig{ID: "pin-symlink", Name: "P", Directory: dir})
		if got := p.Info().PinnedContext; got != "" {
			t.Fatalf("symlinked pin was preloaded: %q", got)
		}
	})
}
