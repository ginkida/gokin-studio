package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCloneProjectMemoryMergesAndRebindsProject(t *testing.T) {
	configDir := t.TempDir()
	oldPath := filepath.Join(t.TempDir(), "old-workspace")
	newPath := filepath.Join(t.TempDir(), "new-workspace")
	memDir := filepath.Join(configDir, "memory")
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	write := func(path string, entries []*Entry) {
		t.Helper()
		data, err := json.Marshal(entries)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldHash, newHash := hashPath(oldPath), hashPath(newPath)
	write(filepath.Join(memDir, oldHash+".json"), []*Entry{
		{ID: "source", Type: MemoryProject, Project: oldHash, Content: "source", Timestamp: now},
		{ID: "collision", Type: MemoryProject, Project: oldHash, Content: "newer source", Timestamp: now.Add(time.Second)},
	})
	write(filepath.Join(memDir, newHash+".json"), []*Entry{
		{ID: "target", Type: MemoryProject, Project: newHash, Content: "target", Timestamp: now.Add(-time.Second)},
		{ID: "collision", Type: MemoryProject, Project: newHash, Content: "stale target", Timestamp: now.Add(-time.Hour)},
	})
	write(filepath.Join(memDir, oldHash+".archive.json"), []*Entry{
		{ID: "archived", Type: MemoryProject, Project: oldHash, Content: "archive", Timestamp: now},
	})

	if err := CloneProjectMemory(configDir, oldPath, newPath); err != nil {
		t.Fatalf("CloneProjectMemory: %v", err)
	}
	assertFile := func(path string, want map[string]string) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var entries []*Entry
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatal(err)
		}
		if len(entries) != len(want) {
			t.Fatalf("%s entries = %d, want %d: %#v", path, len(entries), len(want), entries)
		}
		for _, entry := range entries {
			if entry.Project != newHash {
				t.Errorf("%s project = %q, want %q", entry.ID, entry.Project, newHash)
			}
			if content, ok := want[entry.ID]; !ok || entry.Content != content {
				t.Errorf("entry %q content = %q, want %q (present=%v)", entry.ID, entry.Content, content, ok)
			}
		}
	}
	assertFile(filepath.Join(memDir, newHash+".json"), map[string]string{
		"source": "source", "target": "target", "collision": "newer source",
	})
	assertFile(filepath.Join(memDir, newHash+".archive.json"), map[string]string{"archived": "archive"})
	if _, err := os.Stat(filepath.Join(memDir, oldHash+".json")); err != nil {
		t.Fatalf("source memory backup was removed: %v", err)
	}
}

func TestCloneProjectMemoryRejectsCorruptSource(t *testing.T) {
	configDir := t.TempDir()
	oldPath, newPath := "old", "new"
	memDir := filepath.Join(configDir, "memory")
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, hashPath(oldPath)+".json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CloneProjectMemory(configDir, oldPath, newPath); err == nil {
		t.Fatal("corrupt source memory was silently discarded")
	}
	if _, err := os.Stat(filepath.Join(memDir, hashPath(newPath)+".json")); !os.IsNotExist(err) {
		t.Fatalf("destination memory was written after failure: %v", err)
	}
}
