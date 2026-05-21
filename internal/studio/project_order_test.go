package studio

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReorderProjects_RoundTrip: explicit order persists through a fresh
// GetProjectOrder call.
func TestReorderProjects_RoundTrip(t *testing.T) {
	s := newStudioForTest(t)
	a := addTestProject(t, s, "alpha")
	b := addTestProject(t, s, "beta")
	c := addTestProject(t, s, "gamma")

	if err := s.ReorderProjects([]string{c.ID, a.ID, b.ID}); err != nil {
		t.Fatalf("ReorderProjects: %v", err)
	}
	got := s.GetProjectOrder()
	if len(got) != 3 || got[0] != c.ID || got[1] != a.ID || got[2] != b.ID {
		t.Errorf("expected [c, a, b], got %v", got)
	}
}

// TestReorderProjects_FiltersUnknownIDs: stale frontend state passing IDs
// that no longer exist must be dropped silently. The file should contain
// only the live IDs in the requested order.
func TestReorderProjects_FiltersUnknownIDs(t *testing.T) {
	s := newStudioForTest(t)
	a := addTestProject(t, s, "alpha")

	if err := s.ReorderProjects([]string{"ghost-id", a.ID, "another-ghost"}); err != nil {
		t.Fatalf("ReorderProjects: %v", err)
	}
	got := s.GetProjectOrder()
	if len(got) != 1 || got[0] != a.ID {
		t.Errorf("expected [a], got %v", got)
	}
}

// TestReorderProjects_DedupsRepeats: a buggy frontend that sends the same
// ID twice must not duplicate in the order file.
func TestReorderProjects_DedupsRepeats(t *testing.T) {
	s := newStudioForTest(t)
	a := addTestProject(t, s, "alpha")
	b := addTestProject(t, s, "beta")

	if err := s.ReorderProjects([]string{a.ID, b.ID, a.ID, b.ID}); err != nil {
		t.Fatalf("ReorderProjects: %v", err)
	}
	got := s.GetProjectOrder()
	if len(got) != 2 || got[0] != a.ID || got[1] != b.ID {
		t.Errorf("expected dedup'd [a, b], got %v", got)
	}
}

// TestReorderProjects_EmptyClearsFile: passing an empty list removes the
// order file. The next GetProjectOrder returns empty (revert to default
// sort).
func TestReorderProjects_EmptyClearsFile(t *testing.T) {
	s := newStudioForTest(t)
	a := addTestProject(t, s, "alpha")

	// First, set an order to create the file.
	if err := s.ReorderProjects([]string{a.ID}); err != nil {
		t.Fatalf("setup ReorderProjects: %v", err)
	}
	if _, err := os.Stat(projectOrderPath()); err != nil {
		t.Fatalf("expected file to exist after first reorder: %v", err)
	}
	// Now clear it.
	if err := s.ReorderProjects([]string{}); err != nil {
		t.Fatalf("clear ReorderProjects: %v", err)
	}
	if _, err := os.Stat(projectOrderPath()); !os.IsNotExist(err) {
		t.Errorf("expected file removed after empty reorder, got err=%v", err)
	}
	if got := s.GetProjectOrder(); len(got) != 0 {
		t.Errorf("expected empty order after clear, got %v", got)
	}
}

// TestGetProjectOrder_FiltersDeletedProjects: a stale ID in the order file
// (project deleted after the order was saved) must not be returned by
// GetProjectOrder. Defensive — without this filter, the frontend would
// build sort keys referencing ghost IDs.
func TestGetProjectOrder_FiltersDeletedProjects(t *testing.T) {
	s := newStudioForTest(t)
	a := addTestProject(t, s, "alpha")
	b := addTestProject(t, s, "beta")
	if err := s.ReorderProjects([]string{a.ID, b.ID}); err != nil {
		t.Fatalf("ReorderProjects: %v", err)
	}
	if err := s.RemoveProject(a.ID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	got := s.GetProjectOrder()
	if len(got) != 1 || got[0] != b.ID {
		t.Errorf("expected [b] after deleting a, got %v", got)
	}
}

// TestGetProjectOrder_EmptyWhenNoFile: cold start (no project-order.json)
// returns an empty slice cleanly — frontend falls back to default sort.
func TestGetProjectOrder_EmptyWhenNoFile(t *testing.T) {
	s := newStudioForTest(t)
	if got := s.GetProjectOrder(); len(got) != 0 {
		t.Errorf("expected empty on fresh studio, got %v", got)
	}
}

// TestLoadProjectOrder_CorruptJSONReturnsEmpty: a malformed file is
// silently treated as no-order-set rather than breaking the sidebar.
func TestLoadProjectOrder_CorruptJSONReturnsEmpty(t *testing.T) {
	s := newStudioForTest(t)
	_ = s
	if err := os.MkdirAll(filepath.Dir(projectOrderPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectOrderPath(), []byte("not-json{["), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadProjectOrder()
	if err != nil {
		t.Errorf("corrupt JSON should be silently treated as empty, got error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// TestLoadProjectOrder_DropsEmptyStrings: a defensively crafted file
// with "" entries (e.g. produced by an old frontend bug) is sanitised
// on read.
func TestLoadProjectOrder_DropsEmptyStrings(t *testing.T) {
	s := newStudioForTest(t)
	_ = s
	if err := os.MkdirAll(filepath.Dir(projectOrderPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectOrderPath(), []byte(`["alpha","","beta",""]`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadProjectOrder()
	if err != nil {
		t.Fatalf("loadProjectOrder: %v", err)
	}
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("expected [alpha, beta], got %v", got)
	}
}
