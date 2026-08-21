package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLearningStoresRejectSymlinkedStorage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.json")
	if err := os.WriteFile(target, []byte(`{"danger":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"examples.json", "errors.json"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}
	examples, err := NewExampleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if stats := examples.GetStats(); stats.TotalExamples != 0 {
		t.Fatalf("example store loaded symlink: %+v", stats)
	}
	errors, err := NewErrorStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := errors.Count(); got != 0 {
		t.Fatalf("error store loaded %d symlink records", got)
	}
}

func TestMainMemoryStoreDoesNotQuarantineSymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	target := filepath.Join(root, "outside.json")
	if err := os.WriteFile(target, []byte(`[{"id":"outside"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, hashPath(project)+".json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store, err := NewStore(root, project, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Count(); got != 0 {
		t.Fatalf("main memory store loaded %d symlink records", got)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("memory symlink was quarantined or removed: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != `[{"id":"outside"}]` {
		t.Fatalf("memory symlink target changed: %q err=%v", data, err)
	}
}

func TestProjectLearningRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".gokin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.yaml")
	if err := os.WriteFile(target, []byte("preferences:\n  secret: outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "learning.yaml")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	learning, err := NewProjectLearning(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := learning.GetPreference("secret"); got != "" {
		t.Fatalf("project learning loaded symlinked preference %q", got)
	}
	if learning.Exists() {
		t.Fatal("ProjectLearning.Exists accepted a symlink")
	}
}

func TestExampleStoreRecoversFromNullDocument(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "examples.json"), []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewExampleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LearnFromSuccess("task", "prompt", "agent", "output", 0, 1); err != nil {
		t.Fatal(err)
	}
	if got := store.GetStats().TotalExamples; got != 1 {
		t.Fatalf("example store retained %d records after null recovery, want 1", got)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
}
