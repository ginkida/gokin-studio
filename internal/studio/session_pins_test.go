package studio

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestSetSessionPinned validates the pin/unpin round-trip and that
// ListChatSessions surfaces the new flag.
func TestSetSessionPinned(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "PinnedSession")

	// Default session starts unpinned.
	got, err := s.ListChatSessions(info.ID)
	if err != nil {
		t.Fatalf("ListChatSessions: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one session")
	}
	if got[0].Pinned {
		t.Errorf("default session Pinned = true, want false")
	}

	defaultID := got[0].ID

	// Empty projectID rejected.
	if err := s.SetSessionPinned("", defaultID, true); err == nil {
		t.Error("expected error for empty projectID, got nil")
	}
	// Unknown project rejected.
	if err := s.SetSessionPinned("no-such-id", defaultID, true); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
	// Unknown session rejected.
	if err := s.SetSessionPinned(info.ID, "no-such-session", true); err == nil {
		t.Error("expected error for unknown session, got nil")
	}

	// Pin the default session.
	if err := s.SetSessionPinned(info.ID, defaultID, true); err != nil {
		t.Fatalf("SetSessionPinned(true): %v", err)
	}
	got, _ = s.ListChatSessions(info.ID)
	if !got[0].Pinned {
		t.Errorf("session Pinned after pin = false, want true")
	}

	// Unpin and verify.
	if err := s.SetSessionPinned(info.ID, defaultID, false); err != nil {
		t.Fatalf("SetSessionPinned(false): %v", err)
	}
	got, _ = s.ListChatSessions(info.ID)
	if got[0].Pinned {
		t.Errorf("session Pinned after unpin = true, want false")
	}
}

func TestSetSessionPinned_ConcurrentUpdatesRemainDurable(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Concurrent Session Pins")
	ids := []string{"default"}
	for i := 0; i < 15; i++ {
		session, err := s.CreateChatSession(info.ID)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, session.ID)
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(ids))
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			errs <- s.SetSessionPinned(info.ID, id, true)
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("SetSessionPinned: %v", err)
		}
	}
	persisted, err := loadPinnedSessions(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != len(ids) {
		t.Fatalf("persisted %d/%d concurrent pin updates", len(persisted), len(ids))
	}
	for _, id := range ids {
		if !persisted[id] {
			t.Errorf("session %q missing from persisted pins", id)
		}
	}
}

func TestSetSessionPinned_PersistenceFailureDoesNotChangeMemory(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Failed Session Pin")
	path := sessionPinsPath(info.ID)
	if err := os.MkdirAll(filepath.Join(path, "child"), 0700); err != nil {
		t.Fatal(err)
	}

	if err := s.SetSessionPinned(info.ID, "default", true); err == nil {
		t.Fatal("expected persistence error")
	}
	sessions, err := s.ListChatSessions(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		if session.ID == "default" && session.Pinned {
			t.Fatal("in-memory pin changed despite persistence failure")
		}
	}
}

// TestSetSessionPinned_EmptySessionIDDefaultsToDefault confirms the empty-
// sessionID convention used elsewhere in the studio API.
func TestSetSessionPinned_EmptySessionIDDefaultsToDefault(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "DefaultPin")

	if err := s.SetSessionPinned(info.ID, "", true); err != nil {
		t.Fatalf("SetSessionPinned with empty sessionID: %v", err)
	}
	got, _ := s.ListChatSessions(info.ID)
	if !got[0].Pinned || got[0].ID != "default" {
		t.Errorf("expected default session pinned, got id=%s pinned=%v", got[0].ID, got[0].Pinned)
	}
}

// TestSetSessionPinned_SortPinnedFirst pins one of two sessions and confirms
// ListChatSessions returns the pinned one first regardless of LastUsedAt.
func TestSetSessionPinned_SortPinnedFirst(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "PinSort")

	// Create a second session so we have two to compare.
	second, err := s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}

	// Pretend the second session was used more recently than the default.
	// Since the default was created first and never used, it has lastUsedAt=0
	// and the new one starts at 0 too. Bump the new one.
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	p.mu.RLock()
	secondSess := p.sessions[second.ID]
	p.mu.RUnlock()
	secondSess.mu.Lock()
	secondSess.lastUsedAt = 1234567890
	secondSess.mu.Unlock()

	// Without pinning: second should sort first (more recently used).
	got, _ := s.ListChatSessions(info.ID)
	if got[0].ID != second.ID {
		t.Errorf("pre-pin: expected %s first (more recent), got %s", second.ID, got[0].ID)
	}

	// Now pin the default session — it should leapfrog above the more-recent one.
	if err := s.SetSessionPinned(info.ID, "default", true); err != nil {
		t.Fatalf("SetSessionPinned: %v", err)
	}
	got, _ = s.ListChatSessions(info.ID)
	if got[0].ID != "default" {
		t.Errorf("post-pin: expected default first (pinned), got %s", got[0].ID)
	}
	if !got[0].Pinned {
		t.Error("post-pin: default Pinned flag not set in ListChatSessions output")
	}
}

// TestPinnedSessionsRoundTrip verifies the file is written + reread correctly.
func TestPinnedSessionsRoundTrip(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	pid := "round-trip-proj"

	// Empty load returns empty map.
	got, err := loadPinnedSessions(pid)
	if err != nil {
		t.Fatalf("loadPinnedSessions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty load = %v, want empty", got)
	}

	// Save then reload.
	in := map[string]bool{"sess1": true, "sess2": true, "sess3": false}
	if err := savePinnedSessions(pid, in); err != nil {
		t.Fatalf("savePinnedSessions: %v", err)
	}
	got, _ = loadPinnedSessions(pid)
	if !got["sess1"] || !got["sess2"] {
		t.Errorf("after save, expected sess1+sess2 pinned, got %v", got)
	}
	if got["sess3"] {
		t.Errorf("after save, sess3 (false-valued) should not be pinned, got %v", got)
	}

	// Empty save removes the file.
	if err := savePinnedSessions(pid, map[string]bool{}); err != nil {
		t.Fatalf("savePinnedSessions empty: %v", err)
	}
	if _, err := os.Stat(sessionPinsPath(pid)); !os.IsNotExist(err) {
		t.Errorf("expected file removed after empty save, stat err = %v", err)
	}

	// Empty projectID rejected on save.
	if err := savePinnedSessions("", map[string]bool{"x": true}); err == nil {
		t.Error("expected error for empty projectID, got nil")
	}
}

// TestPinnedSessionsRestart writes pin state to disk, simulates restart by
// calling NewProject directly, and confirms sessions come back pinned.
func TestPinnedSessionsRestart(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	dir := t.TempDir()
	pc := ProjectConfig{
		ID:        "restart-test",
		Name:      "Restart",
		Directory: dir,
		Provider:  "glm",
		Model:     "glm-5.1",
	}
	// Pre-write the pin file before NewProject is called so hydration sees it.
	if err := savePinnedSessions(pc.ID, map[string]bool{"default": true}); err != nil {
		t.Fatalf("savePinnedSessions: %v", err)
	}
	// Need a session file on disk for the default session, otherwise
	// NewProject creates a fresh one without checking pins. Easiest path:
	// write an empty history.
	if err := SaveHistoryWithName(pc.ID+"_default", "Chat 1", nil); err != nil {
		t.Fatalf("SaveHistoryWithName: %v", err)
	}

	p := NewProject(pc)
	sess := p.sessions["default"]
	if sess == nil {
		t.Fatal("expected default session after NewProject")
	}
	if !sess.Pinned {
		t.Errorf("default session Pinned after restart = false, want true (hydration failed)")
	}
}

// TestRemoveProject_CleansUpSessionPins confirms the per-project pins file
// is removed when the project is deleted.
func TestRemoveProject_CleansUpSessionPins(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Cleanup")
	if err := s.SetSessionPinned(info.ID, "default", true); err != nil {
		t.Fatalf("SetSessionPinned: %v", err)
	}
	// Verify the file exists.
	pinPath := sessionPinsPath(info.ID)
	if _, err := os.Stat(pinPath); err != nil {
		t.Fatalf("session-pins file should exist before removal: %v", err)
	}
	// Remove the project — file should be cleaned up.
	if err := s.RemoveProject(info.ID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if _, err := os.Stat(pinPath); !os.IsNotExist(err) {
		t.Errorf("expected session-pins file removed after RemoveProject, stat err = %v", err)
	}
	// And the parent directory should still exist (other projects may use it).
	if _, err := os.Stat(filepath.Dir(pinPath)); err != nil && !os.IsNotExist(err) {
		// Either the directory is gone (OK if no other projects) or still
		// there. Both are fine — we only care that OUR file is gone.
	}
}
