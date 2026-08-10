package studio

import (
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/memory"
)

// newMemStore returns a freshly-initialised memory.Store backed by temp dirs.
func newMemStore(t *testing.T) *memory.Store {
	t.Helper()
	store, err := memory.NewStore(t.TempDir(), t.TempDir(), 100)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	return store
}

// TestListProjectMemory_UnknownProject verifies that an unknown project ID
// returns an error rather than panicking or returning an empty list silently.
func TestListProjectMemory_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.ListProjectMemory("no-such-id"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestListProjectMemory_NilStore verifies that ListProjectMemory returns an
// empty (non-nil) slice — not an error — when the project's memory store has
// not been initialised (e.g. a newly-created project before the first send).
func TestListProjectMemory_NilStore(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-mem", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p
	// p.memoryStore is nil (never initialised)

	entries, err := s.ListProjectMemory(p.ID)
	if err != nil {
		t.Fatalf("expected nil error for nil store, got %v", err)
	}
	if entries == nil {
		t.Error("expected non-nil empty slice, got nil")
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

// TestListProjectMemory_WithEntries verifies that entries written to the
// memory store appear in the ListProjectMemory output with correct fields.
func TestListProjectMemory_WithEntries(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-memfull", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	store := newMemStore(t)
	p.mu.Lock()
	p.memoryStore = store
	p.mu.Unlock()

	entry := memory.NewEntry("the project uses Go 1.25", memory.MemoryProject)
	if err := store.Add(entry); err != nil {
		t.Fatalf("store.Add: %v", err)
	}

	infos, err := s.ListProjectMemory(p.ID)
	if err != nil {
		t.Fatalf("ListProjectMemory: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(infos))
	}
	if infos[0].Content != "the project uses Go 1.25" {
		t.Errorf("content = %q, want 'the project uses Go 1.25'", infos[0].Content)
	}
	if infos[0].Type != string(memory.MemoryProject) {
		t.Errorf("type = %q, want 'project'", infos[0].Type)
	}
}

// TestDeleteMemoryEntry_UnknownProject verifies that an unknown project ID
// returns an error.
func TestDeleteMemoryEntry_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if err := s.DeleteMemoryEntry("no-such-id", "any-entry"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestDeleteMemoryEntry_NilStore verifies that deleting an entry when the
// memory store has not been initialised returns an informative error rather
// than panicking.
func TestDeleteMemoryEntry_NilStore(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-delnil", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p
	// p.memoryStore is nil

	err := s.DeleteMemoryEntry(p.ID, "some-entry-id")
	if err == nil {
		t.Error("expected error when store is nil, got nil")
	}
}

// TestDeleteMemoryEntry_NotFound verifies that deleting a non-existent entry
// returns an error (the store's Remove returns false for missing IDs).
func TestDeleteMemoryEntry_NotFound(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-delnotfound", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	store := newMemStore(t)
	p.mu.Lock()
	p.memoryStore = store
	p.mu.Unlock()

	err := s.DeleteMemoryEntry(p.ID, "nonexistent-id")
	if err == nil {
		t.Error("expected error for missing entry ID, got nil")
	}
}

// TestDeleteMemoryEntry_Success verifies the full lifecycle: add an entry,
// confirm it appears in ListProjectMemory, delete it, confirm it is gone.
func TestDeleteMemoryEntry_Success(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-delok", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	store := newMemStore(t)
	p.mu.Lock()
	p.memoryStore = store
	p.mu.Unlock()

	entry := memory.NewEntry("prefer snake_case for env vars", memory.MemoryProject)
	if err := store.Add(entry); err != nil {
		t.Fatalf("store.Add: %v", err)
	}

	// Confirm it appears.
	infos, err := s.ListProjectMemory(p.ID)
	if err != nil || len(infos) == 0 {
		t.Fatalf("expected entry in list; err=%v, len=%d", err, len(infos))
	}
	entryID := infos[0].ID

	// Delete it.
	if err := s.DeleteMemoryEntry(p.ID, entryID); err != nil {
		t.Fatalf("DeleteMemoryEntry: %v", err)
	}

	// Confirm it is gone.
	infos, err = s.ListProjectMemory(p.ID)
	if err != nil {
		t.Fatalf("ListProjectMemory after delete: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("expected 0 entries after delete, got %d", len(infos))
	}
}

func TestUpdateMemoryEntryPreservesIdentityAndMetadata(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-editok", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	store := newMemStore(t)
	p.mu.Lock()
	p.memoryStore = store
	p.mu.Unlock()

	entry := memory.NewEntry("use package oldname", memory.MemoryProject).WithKey("convention").WithTags([]string{"manual-tag"})
	entry.Reinforcement = 3
	if err := store.Add(entry); err != nil {
		t.Fatal(err)
	}
	before, ok := store.GetByID(entry.ID)
	if !ok {
		t.Fatal("entry missing before update")
	}

	updated, err := s.UpdateMemoryEntry(p.ID, entry.ID, "use package newname")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != before.ID || updated.Key != before.Key || updated.Type != string(before.Type) ||
		updated.Timestamp != before.Timestamp.UnixMilli() || updated.Reinforcement != before.Reinforcement {
		t.Fatalf("metadata changed: before=%#v after=%#v", before, updated)
	}
	if updated.Content != "use package newname" {
		t.Fatalf("content = %q", updated.Content)
	}
	if len(updated.Tags) != 1 || updated.Tags[0] != "newname" {
		t.Fatalf("automatic tags were not rebuilt: %#v", updated.Tags)
	}
}

func TestUpdateMemoryEntryValidatesBeforeMutation(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-editvalidation", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p
	store := newMemStore(t)
	p.mu.Lock()
	p.memoryStore = store
	p.mu.Unlock()
	entry := memory.NewEntry("original", memory.MemoryProject)
	if err := store.Add(entry); err != nil {
		t.Fatal(err)
	}

	for name, content := range map[string]string{
		"blank":    " \n\t",
		"NUL":      "unsafe\x00content",
		"oversize": strings.Repeat("x", MemoryContentMaxBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.UpdateMemoryEntry(p.ID, entry.ID, content); err == nil {
				t.Fatal("invalid memory content accepted")
			}
			got, ok := store.GetByID(entry.ID)
			if !ok || got.Content != "original" {
				t.Fatalf("entry mutated after rejection: %#v", got)
			}
		})
	}
	if _, err := s.UpdateMemoryEntry(p.ID, strings.Repeat("i", MemoryEntryIDMaxBytes+1), "valid"); err == nil {
		t.Fatal("oversized memory ID accepted")
	}
	if _, err := s.UpdateMemoryEntry(p.ID, "missing", "valid"); err == nil {
		t.Fatal("missing memory entry accepted")
	}
}

func TestUpdateMemoryEntryPersistsImmediately(t *testing.T) {
	configDir := t.TempDir()
	projectDir := t.TempDir()
	store, err := memory.NewStore(configDir, projectDir, 100)
	if err != nil {
		t.Fatal(err)
	}
	entry := memory.NewEntry("before restart", memory.MemoryProject)
	if err := store.Add(entry); err != nil {
		t.Fatal(err)
	}

	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-editpersist", Name: "P", Directory: projectDir})
	p.studio = s
	p.memoryStore = store
	s.projects[p.ID] = p
	if _, err := s.UpdateMemoryEntry(p.ID, entry.ID, "after restart"); err != nil {
		t.Fatal(err)
	}

	reloaded, err := memory.NewStore(configDir, projectDir, 100)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.GetByID(entry.ID)
	if !ok || got.Content != "after restart" {
		t.Fatalf("reloaded entry = %#v, found=%v", got, ok)
	}
}
