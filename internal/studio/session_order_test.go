package studio

import (
	"os"
	"testing"
)

// TestReorderChatSessions_Basic verifies the new order is reflected in
// ListChatSessions (within the unpinned group). Default-sort sessions
// have LastUsedAt=0 so the user-order takes effect immediately.
func TestReorderChatSessions_Basic(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "ReorderProj")

	// Create two more sessions so we have three to reorder.
	second, err := s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatalf("CreateChatSession 2: %v", err)
	}
	third, err := s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatalf("CreateChatSession 3: %v", err)
	}

	// Default order (lastUsedAt=0 on all) — "default" sorts last.
	got, err := s.ListChatSessions(info.ID)
	if err != nil {
		t.Fatalf("ListChatSessions: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(got))
	}

	// Apply explicit order: third → second → default
	if err := s.ReorderChatSessions(info.ID, []string{third.ID, second.ID, "default"}); err != nil {
		t.Fatalf("ReorderChatSessions: %v", err)
	}
	got, _ = s.ListChatSessions(info.ID)
	if got[0].ID != third.ID || got[1].ID != second.ID || got[2].ID != "default" {
		t.Errorf("expected order [%s, %s, default], got [%s, %s, %s]",
			third.ID, second.ID, got[0].ID, got[1].ID, got[2].ID)
	}
}

// TestReorderChatSessions_PinnedFirst confirms the pinned-first rule
// still wins over an explicit order: a non-pinned session can't leapfrog
// a pinned one regardless of order array position.
func TestReorderChatSessions_PinnedFirst(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "PinPlusOrder")

	second, err := s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}

	// Pin "default" — it must always sort first.
	if err := s.SetSessionPinned(info.ID, "default", true); err != nil {
		t.Fatalf("SetSessionPinned: %v", err)
	}

	// Try to reorder with second-then-default — pinning should override.
	if err := s.ReorderChatSessions(info.ID, []string{second.ID, "default"}); err != nil {
		t.Fatalf("ReorderChatSessions: %v", err)
	}

	got, _ := s.ListChatSessions(info.ID)
	if got[0].ID != "default" {
		t.Errorf("expected pinned 'default' first despite reorder, got %s", got[0].ID)
	}
}

// TestReorderChatSessions_DropsUnknownIDs filters stale IDs that no
// longer match a live session — defends against frontend race conditions
// where a deleted session's ID still appears in a drag operation.
func TestReorderChatSessions_DropsUnknownIDs(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "FilterIDs")

	if err := s.ReorderChatSessions(info.ID, []string{"default", "ghost-id", "nonexistent"}); err != nil {
		t.Fatalf("ReorderChatSessions: %v", err)
	}

	// Read the persisted order file and confirm only "default" survived.
	order, err := loadSessionOrder(info.ID)
	if err != nil {
		t.Fatalf("loadSessionOrder: %v", err)
	}
	if len(order) != 1 || order[0] != "default" {
		t.Errorf("expected order [default] after filter, got %v", order)
	}
}

// TestReorderChatSessions_DedupsRepeatedIDs handles a defensive case
// where the same ID appears twice in the input.
func TestReorderChatSessions_DedupsRepeatedIDs(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Dedup")
	second, _ := s.CreateChatSession(info.ID)

	if err := s.ReorderChatSessions(info.ID, []string{second.ID, "default", second.ID}); err != nil {
		t.Fatalf("ReorderChatSessions: %v", err)
	}
	order, _ := loadSessionOrder(info.ID)
	if len(order) != 2 {
		t.Errorf("expected 2 entries after dedup, got %d: %v", len(order), order)
	}
	if order[0] != second.ID || order[1] != "default" {
		t.Errorf("expected [second, default], got %v", order)
	}
}

// TestReorderChatSessions_Validation covers reject paths.
func TestReorderChatSessions_Validation(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Validation")

	if err := s.ReorderChatSessions("", []string{"default"}); err == nil {
		t.Error("expected error for empty projectID, got nil")
	}
	if err := s.ReorderChatSessions("no-such-id", []string{"default"}); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
	// Empty order array is valid (clears the order file).
	if err := s.ReorderChatSessions(info.ID, []string{}); err != nil {
		t.Errorf("expected empty array to be valid, got %v", err)
	}
}

// TestSessionOrderRoundTrip verifies the file save/load cycle.
func TestSessionOrderRoundTrip(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	pid := "round-trip-proj"

	got, err := loadSessionOrder(pid)
	if err != nil {
		t.Fatalf("loadSessionOrder: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty load = %v, want empty", got)
	}

	in := []string{"sess1", "sess2", "sess3"}
	if err := saveSessionOrder(pid, in); err != nil {
		t.Fatalf("saveSessionOrder: %v", err)
	}
	got, _ = loadSessionOrder(pid)
	if len(got) != 3 || got[0] != "sess1" {
		t.Errorf("after save, expected %v, got %v", in, got)
	}

	// Empty save removes the file.
	if err := saveSessionOrder(pid, []string{}); err != nil {
		t.Fatalf("saveSessionOrder empty: %v", err)
	}
	if _, err := os.Stat(sessionOrderPath(pid)); !os.IsNotExist(err) {
		t.Errorf("expected file removed after empty save, stat err = %v", err)
	}

	// Empty projectID rejected on save.
	if err := saveSessionOrder("", []string{"x"}); err == nil {
		t.Error("expected error for empty projectID, got nil")
	}
}

// TestRemoveProject_CleansUpSessionOrder confirms cleanup on project delete.
func TestRemoveProject_CleansUpSessionOrder(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Cleanup")
	if err := s.ReorderChatSessions(info.ID, []string{"default"}); err != nil {
		t.Fatalf("ReorderChatSessions: %v", err)
	}
	path := sessionOrderPath(info.ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("session-order file should exist before removal: %v", err)
	}
	if err := s.RemoveProject(info.ID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected session-order file removed after RemoveProject, stat err = %v", err)
	}
}
