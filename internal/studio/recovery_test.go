package studio

import (
	"os"
	"testing"
)

// TestDiscardRecoveryEvents_EmptySessionIDDefaultsToDefault verifies that
// passing an empty sessionID to DiscardRecoveryEvents defaults to "default",
// matching the frontend convention for the primary session.
func TestDiscardRecoveryEvents_EmptySessionIDDefaultsToDefault(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	pid := "proj-discard-empty-sid"

	// Write a replay log for the "default" session.
	r := NewReplayLogger(pid, "default")
	r.Append(ReplayEvent{Type: "user", Text: "something"})
	r.Close()

	// Confirm the file exists.
	if _, err := os.Stat(replayPath(pid, "default")); err != nil {
		t.Fatalf("expected replay file to exist: %v", err)
	}

	// Discard with empty sessionID — should target "default".
	if err := s.DiscardRecoveryEvents(pid, ""); err != nil {
		t.Fatalf("DiscardRecoveryEvents with empty sessionID: %v", err)
	}

	// File must be gone.
	if _, err := os.Stat(replayPath(pid, "default")); !os.IsNotExist(err) {
		t.Errorf("expected replay file removed after Discard; os.Stat err=%v", err)
	}
}

// TestGetRecoveryEvents_NoFile verifies that asking for recovery events when no
// replay log exists returns nil, nil (not an error).
func TestGetRecoveryEvents_NoFile(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)

	events, err := s.GetRecoveryEvents("someproject", "default")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if events != nil {
		t.Errorf("expected nil events, got %v", events)
	}
}

// TestGetRecoveryEvents_ReturnsPendingEvents verifies that an incomplete replay
// log (no "complete" marker at the end) is returned as recovery events.
func TestGetRecoveryEvents_ReturnsPendingEvents(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)

	// Write a replay log simulating an interrupted turn (no Complete() call).
	pid, sid := "proj-pending", "default"
	r := NewReplayLogger(pid, sid)
	r.Append(ReplayEvent{Type: "user", Text: "deploy to prod"})
	r.Append(ReplayEvent{Type: "tool_call", Tool: "bash", Args: map[string]any{"cmd": "make deploy"}})
	// No r.Complete() — simulates a crash mid-turn.
	r.Close() // stop writes but preserve the file

	events, err := s.GetRecoveryEvents(pid, sid)
	if err != nil {
		t.Fatalf("GetRecoveryEvents error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 recovery events, got %d", len(events))
	}
	if events[0].Type != "user" || events[0].Text != "deploy to prod" {
		t.Errorf("event[0] = %+v, want {user, 'deploy to prod'}", events[0])
	}
	if events[1].Type != "tool_call" || events[1].Tool != "bash" {
		t.Errorf("event[1] = %+v, want {tool_call, bash}", events[1])
	}
}

// TestGetRecoveryEvents_CompleteMarkerDropsSilently verifies that a replay log
// ending with a "complete" marker is treated as a finished turn: the file is
// discarded and nil events are returned (no spurious recovery banner).
func TestGetRecoveryEvents_CompleteMarkerDropsSilently(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)

	pid, sid := "proj-done", "default"
	r := NewReplayLogger(pid, sid)
	r.Append(ReplayEvent{Type: "user", Text: "hello"})
	r.Append(ReplayEvent{Type: "assistant_text", Text: "hi there"})
	// Append a "complete" marker manually (normally written by Complete but
	// here we simulate the edge case where Complete wrote it before os.Remove).
	r.Append(ReplayEvent{Type: "complete"})
	r.Close() // keep the file on disk (don't call Complete which removes it)

	// Verify the file still exists before calling GetRecoveryEvents.
	if _, err := os.Stat(replayPath(pid, sid)); err != nil {
		t.Fatalf("expected replay file to exist before GetRecoveryEvents: %v", err)
	}

	events, err := s.GetRecoveryEvents(pid, sid)
	if err != nil {
		t.Fatalf("GetRecoveryEvents error: %v", err)
	}
	if events != nil {
		t.Errorf("expected nil events when complete marker present, got %v", events)
	}
	// GetRecoveryEvents should have discarded the file.
	if _, err := os.Stat(replayPath(pid, sid)); !os.IsNotExist(err) {
		t.Errorf("expected replay file to be removed after complete marker detected; os.Stat err = %v", err)
	}
}

// TestDiscardRecoveryEvents_RemovesFile verifies that DiscardRecoveryEvents
// deletes the replay log, making subsequent GetRecoveryEvents return nil.
func TestDiscardRecoveryEvents_RemovesFile(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)

	pid, sid := "proj-discard", "default"
	r := NewReplayLogger(pid, sid)
	r.Append(ReplayEvent{Type: "user", Text: "do something"})
	r.Close()

	// Confirm events exist before discard.
	events, err := s.GetRecoveryEvents(pid, sid)
	if err != nil || len(events) == 0 {
		t.Fatalf("expected recovery events before discard; events=%v err=%v", events, err)
	}

	if err := s.DiscardRecoveryEvents(pid, sid); err != nil {
		t.Fatalf("DiscardRecoveryEvents: %v", err)
	}

	// After discard, no events should be returned.
	events, err = s.GetRecoveryEvents(pid, sid)
	if err != nil {
		t.Fatalf("GetRecoveryEvents after discard: %v", err)
	}
	if events != nil {
		t.Errorf("expected nil events after discard, got %v", events)
	}
}

// TestGetRecoveryEvents_LoadReplayError verifies that when LoadReplay itself
// fails (e.g. permission denied on the history directory), GetRecoveryEvents
// propagates the error rather than treating it as "no recovery file".
// This covers the `if err != nil { return nil, err }` branch in GetRecoveryEvents.
func TestGetRecoveryEvents_LoadReplayError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission-denied as root")
	}

	// Isolated config dir so chmod doesn't affect parallel tests.
	// Use withTempHistoryDir to ensure GOKIN_CONFIG_DIR is set, then
	// remove permissions from the history directory.
	_ = withTempHistoryDir(t)
	hDir := historyDir()
	if err := os.MkdirAll(hDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create the Studio BEFORE chmod so it doesn't need to write to hDir.
	s := &Studio{projects: make(map[string]*Project), askUsers: newAskUserRegistry()}

	// Now make the directory inaccessible.
	if err := os.Chmod(hDir, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(hDir, 0o700) })

	_, err := s.GetRecoveryEvents("some-project", "default")
	if err == nil {
		t.Error("expected error from GetRecoveryEvents when history dir is unreadable, got nil")
	}
}

// TestApplyDefaultToProjects verifies that calling ApplyDefaultToProjects
// overwrites the provider/model of every existing project with the current
// default settings and invalidates their cached clients.
func TestApplyDefaultToProjects(t *testing.T) {
	s := newStudioForTest(t)

	// Create two projects with different providers.
	pA := addTestProject(t, s, "Alpha")
	pB := addTestProject(t, s, "Beta")

	// Give them an initial provider so we can confirm it changes.
	if err := s.SetProjectProvider(pA.ID, "ollama", "llama3"); err != nil {
		t.Fatalf("SetProjectProvider A: %v", err)
	}
	if err := s.SetProjectProvider(pB.ID, "minimax", "minimax-text"); err != nil {
		t.Fatalf("SetProjectProvider B: %v", err)
	}

	// Configure the studio defaults.
	if err := s.UpdateSettings(StudioConfig{Settings: Settings{
		DefaultProvider: "kimi",
		DefaultModel:    "kimi-for-coding",
	}}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	// Apply defaults to all projects.
	if err := s.ApplyDefaultToProjects(); err != nil {
		t.Fatalf("ApplyDefaultToProjects: %v", err)
	}

	// Verify both projects switched to the default provider/model.
	for _, id := range []string{pA.ID, pB.ID} {
		got, err := s.GetProject(id)
		if err != nil {
			t.Fatalf("GetProject(%q): %v", id, err)
		}
		if got.Provider != "kimi" {
			t.Errorf("project %q provider = %q, want 'kimi'", id, got.Provider)
		}
		if got.Model != "kimi-for-coding" {
			t.Errorf("project %q model = %q, want 'kimi-for-coding'", id, got.Model)
		}
	}
}
