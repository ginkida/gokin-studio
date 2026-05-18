package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempDraftsDir points GOKIN_CONFIG_DIR at a fresh temp dir so draft
// files don't collide with the user's real config or with other tests.
// Returns the temp dir for assertions; t.Cleanup restores the env var.
func withTempDraftsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := os.Getenv("GOKIN_CONFIG_DIR")
	os.Setenv("GOKIN_CONFIG_DIR", dir)
	t.Cleanup(func() {
		if prev == "" {
			os.Unsetenv("GOKIN_CONFIG_DIR")
		} else {
			os.Setenv("GOKIN_CONFIG_DIR", prev)
		}
	})
	return dir
}

func TestSaveDraft_RoundTrip(t *testing.T) {
	withTempDraftsDir(t)
	s := newStudioForTest(t)

	if err := s.SaveDraft("proj1", "sess1", "in-progress message"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	got, err := s.GetDraft("proj1", "sess1")
	if err != nil {
		t.Fatalf("GetDraft: %v", err)
	}
	if got != "in-progress message" {
		t.Errorf("got %q, want %q", got, "in-progress message")
	}
}

func TestSaveDraft_EmptyClearsFile(t *testing.T) {
	dir := withTempDraftsDir(t)
	s := newStudioForTest(t)

	if err := s.SaveDraft("p", "s", "hello"); err != nil {
		t.Fatalf("SaveDraft hello: %v", err)
	}
	// Empty text should remove the on-disk draft, not write an empty file.
	if err := s.SaveDraft("p", "s", ""); err != nil {
		t.Fatalf("SaveDraft empty: %v", err)
	}
	// File should be gone.
	want := filepath.Join(dir, "drafts", "p_s.txt")
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Errorf("expected %q to be removed, stat err = %v", want, err)
	}
	// And GetDraft returns "" without error.
	got, err := s.GetDraft("p", "s")
	if err != nil || got != "" {
		t.Errorf("after clear: got %q, err %v; want \"\", nil", got, err)
	}
}

func TestSaveDraft_WhitespaceClears(t *testing.T) {
	withTempDraftsDir(t)
	s := newStudioForTest(t)

	_ = s.SaveDraft("p", "s", "actual draft")
	if err := s.SaveDraft("p", "s", "   \n\t   "); err != nil {
		t.Fatalf("SaveDraft whitespace: %v", err)
	}
	got, _ := s.GetDraft("p", "s")
	if got != "" {
		t.Errorf("whitespace-only draft kept: %q", got)
	}
}

func TestSaveDraft_TruncatesAtMaxBytes(t *testing.T) {
	withTempDraftsDir(t)
	s := newStudioForTest(t)

	huge := strings.Repeat("a", DraftMaxBytes+5000)
	if err := s.SaveDraft("p", "s", huge); err != nil {
		t.Fatalf("SaveDraft huge: %v", err)
	}
	got, _ := s.GetDraft("p", "s")
	if len(got) != DraftMaxBytes {
		t.Errorf("draft length = %d, want %d (capped)", len(got), DraftMaxBytes)
	}
}

func TestGetDraft_NoFileReturnsEmpty(t *testing.T) {
	withTempDraftsDir(t)
	s := newStudioForTest(t)

	got, err := s.GetDraft("nope", "nope")
	if err != nil {
		t.Errorf("expected no error for missing draft, got %v", err)
	}
	if got != "" {
		t.Errorf("expected \"\" for missing draft, got %q", got)
	}
}

func TestClearDraft_MissingIsNotError(t *testing.T) {
	withTempDraftsDir(t)
	s := newStudioForTest(t)

	if err := s.ClearDraft("never", "existed"); err != nil {
		t.Errorf("ClearDraft on missing: %v", err)
	}
}

// TestClearDraft_EmptyProjectIDIsNoop covers the early-return path that
// guards against frontend calls before a project is selected.
func TestClearDraft_EmptyProjectIDIsNoop(t *testing.T) {
	withTempDraftsDir(t)
	s := newStudioForTest(t)
	if err := s.ClearDraft("", ""); err != nil {
		t.Errorf("ClearDraft empty pid: %v", err)
	}
	if err := s.ClearDraft("", "any"); err != nil {
		t.Errorf("ClearDraft empty pid + sid: %v", err)
	}
}

// TestClearDraft_EmptySessionDefaultsToDefault verifies that an empty
// sessionID is treated as "default", matching SaveDraft / GetDraft.
func TestClearDraft_EmptySessionDefaultsToDefault(t *testing.T) {
	withTempDraftsDir(t)
	s := newStudioForTest(t)
	_ = s.SaveDraft("p", "default", "x")
	if err := s.ClearDraft("p", ""); err != nil { // empty → default
		t.Fatalf("ClearDraft: %v", err)
	}
	if got, _ := s.GetDraft("p", "default"); got != "" {
		t.Errorf("draft survived clear-via-empty-sid: %q", got)
	}
}

// TestGetDraft_EmptyProjectIDIsNoop covers the early-return guard in GetDraft.
func TestGetDraft_EmptyProjectIDIsNoop(t *testing.T) {
	withTempDraftsDir(t)
	s := newStudioForTest(t)
	got, err := s.GetDraft("", "")
	if err != nil || got != "" {
		t.Errorf("GetDraft empty pid: got=%q err=%v", got, err)
	}
}

func TestSaveDraft_EmptyProjectIDIsNoop(t *testing.T) {
	withTempDraftsDir(t)
	s := newStudioForTest(t)

	// Frontend renders before a project is selected; SaveDraft mustn't error
	// or create a stray draft file when projectID is empty.
	if err := s.SaveDraft("", "", "ghost"); err != nil {
		t.Errorf("SaveDraft empty pid: %v", err)
	}
	got, _ := s.GetDraft("", "")
	if got != "" {
		t.Errorf("ghost draft survived: %q", got)
	}
}

func TestSaveDraft_EmptySessionDefaultsToDefault(t *testing.T) {
	withTempDraftsDir(t)
	s := newStudioForTest(t)

	if err := s.SaveDraft("p", "", "x"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	// Should be readable as both "" and "default" — they're aliased.
	got1, _ := s.GetDraft("p", "")
	got2, _ := s.GetDraft("p", "default")
	if got1 != "x" || got2 != "x" {
		t.Errorf("alias mismatch: empty=%q default=%q", got1, got2)
	}
}

func TestSanitiseDraftKey_RejectsTraversal(t *testing.T) {
	// "../" or absolute paths must not escape the drafts directory.
	cases := []struct {
		in, want string
	}{
		{"../etc/passwd", "___etc_passwd"}, // ".", ".", "/" each → "_"
		{"/abs/path", "_abs_path"},
		{"con\x00trol", "con_trol"},
		{"", "_"},
		{".hidden", "_hidden"},
		{"a:b", "a_b"},
		{"a\\b", "a_b"},
	}
	for _, c := range cases {
		got := sanitiseDraftKey(c.in)
		if got != c.want {
			t.Errorf("sanitiseDraftKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRemoveProjectDrafts_ClearsAllSessionsForProject(t *testing.T) {
	withTempDraftsDir(t)
	s := newStudioForTest(t)

	// Two sessions on project A, one on project B. Removing A must keep B.
	_ = s.SaveDraft("projA", "default", "A1")
	_ = s.SaveDraft("projA", "chat2", "A2")
	_ = s.SaveDraft("projB", "default", "B1")

	removeProjectDrafts("projA")

	if got, _ := s.GetDraft("projA", "default"); got != "" {
		t.Errorf("A/default not removed: %q", got)
	}
	if got, _ := s.GetDraft("projA", "chat2"); got != "" {
		t.Errorf("A/chat2 not removed: %q", got)
	}
	if got, _ := s.GetDraft("projB", "default"); got != "B1" {
		t.Errorf("B/default got removed: %q", got)
	}
}

func TestRemoveProject_ClearsDrafts(t *testing.T) {
	withTempHistoryDir(t)
	withTempDraftsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	// Stash a draft before removal.
	if err := s.SaveDraft(pInfo.ID, "default", "remember me"); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	if err := s.RemoveProject(pInfo.ID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if got, _ := s.GetDraft(pInfo.ID, "default"); got != "" {
		t.Errorf("draft survived RemoveProject: %q", got)
	}
}

func TestDeleteChatSession_ClearsDraft(t *testing.T) {
	withTempHistoryDir(t)
	withTempDraftsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	// Need at least 2 sessions so DeleteChatSession's last-session guard passes.
	extra, err := s.CreateChatSession(pInfo.ID)
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}
	_ = s.SaveDraft(pInfo.ID, extra.ID, "draft for chat 2")

	if err := s.DeleteChatSession(pInfo.ID, extra.ID); err != nil {
		t.Fatalf("DeleteChatSession: %v", err)
	}
	if got, _ := s.GetDraft(pInfo.ID, extra.ID); got != "" {
		t.Errorf("draft survived DeleteChatSession: %q", got)
	}
}

func TestClearHistory_ClearsDraft(t *testing.T) {
	withTempHistoryDir(t)
	withTempDraftsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	_ = s.SaveDraft(pInfo.ID, "default", "in flight")
	if err := s.ClearHistory(pInfo.ID, "default"); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}
	if got, _ := s.GetDraft(pInfo.ID, "default"); got != "" {
		t.Errorf("draft survived ClearHistory: %q", got)
	}
}
