package studio

import (
	"os"
	"testing"

	"google.golang.org/genai"
)

// TestForkChatSession_StoresParentID verifies the new fork sets ParentID on
// the freshly-created ChatSession in memory so the UI can show lineage
// without an extra GetProject call.
func TestForkChatSession_StoresParentID(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	p := projectFromInfo(t, s, pInfo)
	seedHistory(t, s, pInfo.ID, "default", userTurn("hi"))

	info, err := s.ForkChatSession(pInfo.ID, "default", 0, "")
	if err != nil {
		t.Fatalf("ForkChatSession: %v", err)
	}
	forked := p.sessions[info.ID]
	if forked.ParentID != "default" {
		t.Errorf("forked.ParentID = %q, want %q", forked.ParentID, "default")
	}
	if info.ParentID != "default" {
		t.Errorf("info.ParentID = %q, want %q", info.ParentID, "default")
	}
}

// TestForkChatSession_PersistsParentID verifies that the parent ID is
// persisted to disk and reloaded correctly — important for the lineage
// indicator surviving an app restart.
func TestForkChatSession_PersistsParentID(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	seedHistory(t, s, pInfo.ID, "default", userTurn("hi"))

	info, err := s.ForkChatSession(pInfo.ID, "default", 0, "branch1")
	if err != nil {
		t.Fatalf("ForkChatSession: %v", err)
	}

	// Read the parent ID directly from the persisted history file.
	got := LoadHistoryParent(pInfo.ID + "_" + info.ID)
	if got != "default" {
		t.Errorf("LoadHistoryParent = %q, want %q", got, "default")
	}
}

// TestNewProject_RestoresParentID simulates an app restart by saving a
// history file with a parent ID, then constructing a new Project from
// that on-disk state and verifying the loaded session has its ParentID
// populated.
func TestNewProject_RestoresParentID(t *testing.T) {
	withTempHistoryDir(t)

	// Stamp a forked session's history file with parent metadata.
	forkedHist := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hi")}},
	}
	if err := SaveHistoryWithMetadata("p1_abc12345", "Branch", "default", forkedHist); err != nil {
		t.Fatalf("SaveHistoryWithMetadata: %v", err)
	}
	// Stamp a parent session too so ListChatSessions can resolve the name.
	if err := SaveHistoryWithMetadata("p1_default", "Source", "", forkedHist); err != nil {
		t.Fatalf("SaveHistoryWithMetadata source: %v", err)
	}

	// Reconstruct the project — NewProject should load both sessions and
	// populate ParentID on the forked one.
	p := NewProject(ProjectConfig{ID: "p1", Name: "P", Directory: t.TempDir()})
	branch, ok := p.sessions["abc12345"]
	if !ok {
		t.Fatalf("forked session not loaded; got sessions: %v", p.sessions)
	}
	if branch.ParentID != "default" {
		t.Errorf("loaded branch.ParentID = %q, want %q", branch.ParentID, "default")
	}
	src, ok := p.sessions["default"]
	if !ok {
		t.Fatalf("source session not loaded")
	}
	if src.ParentID != "" {
		t.Errorf("source.ParentID = %q, want empty", src.ParentID)
	}
}

// TestListChatSessions_FillsParentName verifies the sibling-lookup that
// converts a ParentID into a human-readable name for the UI.
func TestListChatSessions_FillsParentName(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	p := projectFromInfo(t, s, pInfo)
	p.sessions["default"].Name = "Original"
	seedHistory(t, s, pInfo.ID, "default", userTurn("seed"))

	branch, err := s.ForkChatSession(pInfo.ID, "default", 0, "Branch A")
	if err != nil {
		t.Fatalf("ForkChatSession: %v", err)
	}

	list, err := s.ListChatSessions(pInfo.ID)
	if err != nil {
		t.Fatalf("ListChatSessions: %v", err)
	}
	var found *ChatSessionInfo
	for _, info := range list {
		if info.ID == branch.ID {
			found = info
			break
		}
	}
	if found == nil {
		t.Fatal("forked session missing from ListChatSessions")
	}
	if found.ParentID != "default" {
		t.Errorf("ParentID = %q, want %q", found.ParentID, "default")
	}
	if found.ParentName != "Original" {
		t.Errorf("ParentName = %q, want %q", found.ParentName, "Original")
	}
}

// TestListChatSessions_DeletedParentFallback verifies that when the parent
// session has been deleted, the lineage chip shows "(deleted)" rather
// than dropping the link silently.
func TestListChatSessions_DeletedParentFallback(t *testing.T) {
	withTempHistoryDir(t)
	withTempPinsDir(t) // DeleteChatSession touches pins; needs the override.
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	p := projectFromInfo(t, s, pInfo)

	// Need a NON-default session to fork from so we can delete it later
	// without hitting the last-session guard.
	src, err := s.CreateChatSession(pInfo.ID)
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}
	p.sessions[src.ID].history = []*genai.Content{userTurn("hi from src")}

	branch, err := s.ForkChatSession(pInfo.ID, src.ID, 0, "Branch")
	if err != nil {
		t.Fatalf("ForkChatSession: %v", err)
	}

	// Delete the source — there are now ≥2 sessions (default + branch),
	// so the last-session guard is satisfied.
	if err := s.DeleteChatSession(pInfo.ID, src.ID); err != nil {
		t.Fatalf("DeleteChatSession: %v", err)
	}

	list, _ := s.ListChatSessions(pInfo.ID)
	var found *ChatSessionInfo
	for _, info := range list {
		if info.ID == branch.ID {
			found = info
			break
		}
	}
	if found == nil {
		t.Fatal("branch missing from list")
	}
	if found.ParentName != "(deleted)" {
		t.Errorf("ParentName after parent deletion = %q, want %q", found.ParentName, "(deleted)")
	}
}

// TestListChatSessions_NoParentForTopLevel verifies non-forked sessions
// have empty ParentID/ParentName so the UI doesn't show a misleading "↳"
// indicator on every session.
func TestListChatSessions_NoParentForTopLevel(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	list, _ := s.ListChatSessions(pInfo.ID)
	for _, info := range list {
		if info.ParentID != "" || info.ParentName != "" {
			t.Errorf("top-level session %q has unexpected parent: id=%q name=%q",
				info.Name, info.ParentID, info.ParentName)
		}
	}
}

// TestSaveHistoryWithName_PreservesParentID verifies the read-then-write
// behaviour: a normal turn-finished save shouldn't strip the parent ID
// stamped by ForkChatSession on the first write.
func TestSaveHistoryWithName_PreservesParentID(t *testing.T) {
	withTempHistoryDir(t)

	// First write: explicit parent.
	hist := []*genai.Content{userTurn("first")}
	if err := SaveHistoryWithMetadata("p1_branch", "Branch", "default", hist); err != nil {
		t.Fatalf("SaveHistoryWithMetadata: %v", err)
	}
	if got := LoadHistoryParent("p1_branch"); got != "default" {
		t.Fatalf("after first save, parent = %q, want %q", got, "default")
	}

	// Subsequent save via the name-only API (the normal turn path) MUST
	// preserve the parent.
	hist2 := append(hist, userTurn("second"))
	if err := SaveHistoryWithName("p1_branch", "Branch", hist2); err != nil {
		t.Fatalf("SaveHistoryWithName: %v", err)
	}
	if got := LoadHistoryParent("p1_branch"); got != "default" {
		t.Errorf("after second save, parent = %q, want %q (must be preserved)", got, "default")
	}
}

// TestSaveHistoryWithMetadata_EmptyAllSkipsWrite verifies the early-return
// when nothing meaningful is being persisted (matches SaveHistoryWithName's
// pre-existing behaviour for the legacy case).
func TestSaveHistoryWithMetadata_EmptyAllSkipsWrite(t *testing.T) {
	withTempHistoryDir(t)
	if err := SaveHistoryWithMetadata("p1_nope", "", "", nil); err != nil {
		t.Errorf("expected nil for empty save, got %v", err)
	}
	// The history file should not exist.
	if got := LoadHistoryName("p1_nope"); got != "" {
		t.Errorf("unexpected name after no-op save: %q", got)
	}
}

// TestLoadHistoryParent_LegacyArrayReturnsEmpty verifies the legacy bare-array
// format (pre-versioned history) reports no parent. Forking didn't exist
// when those files were written.
func TestLoadHistoryParent_LegacyArrayReturnsEmpty(t *testing.T) {
	withTempHistoryDir(t)
	// MkdirAll the history dir; atomicWriteFile uses os.Rename which fails
	// if the destination directory doesn't exist.
	if err := os.MkdirAll(historyDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Write a bare-array history file (legacy v0 / v1 format).
	legacy := []byte(`[{"role":"user","text":"old"}]`)
	if err := atomicWriteFile(historyPath("p1_legacy"), legacy, 0o600); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	got := LoadHistoryParent("p1_legacy")
	if got != "" {
		t.Errorf("legacy-format parent = %q, want empty", got)
	}
}
