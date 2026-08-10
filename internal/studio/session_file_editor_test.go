package studio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSessionFileEditorReadsAndSavesExactSessionFile(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Editor")
	path := filepath.Join(project.Directory, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package main\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	snapshot, err := s.GetSessionFileSnapshot(project.ID, "default", "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Content != "package main\n" || snapshot.Path != "src/main.go" || len(snapshot.Revision) != 64 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	result, err := s.SaveSessionFileContent(project.ID, "default", "src/main.go", "package main\n\nfunc main() {}\n", snapshot.Revision)
	if err != nil || !result.Saved || result.Conflict || result.Current.Revision == snapshot.Revision {
		t.Fatalf("save = %#v, %v", result, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("disk = %q, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, %v", info.Mode().Perm(), err)
	}
}

func TestSessionFileEditorConflictPreservesBothSides(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Conflict")
	path := filepath.Join(project.Directory, "notes.txt")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := s.GetSessionFileSnapshot(project.ID, "default", "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	conflict, err := s.SaveSessionFileContent(project.ID, "default", "notes.txt", "draft", opened.Revision)
	if err != nil || conflict.Saved || !conflict.Conflict || conflict.Current.Content != "external" {
		t.Fatalf("conflict = %#v, %v", conflict, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "external" {
		t.Fatalf("conflicting save overwrote disk: %q", data)
	}
	override, err := s.SaveSessionFileContent(project.ID, "default", "notes.txt", "draft", conflict.Current.Revision)
	if err != nil || !override.Saved || override.Conflict {
		t.Fatalf("override = %#v, %v", override, err)
	}
}

func TestSessionFileEditorRejectsUnsafeAndUnsupportedTargets(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Safety")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project.Directory, "escape.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(project.Directory, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project.Directory, "real", "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(project.Directory, "linked")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project.Directory, "binary.dat"), []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project.Directory, "large.txt"), []byte(strings.Repeat("x", sessionFileEditorMaxBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside.txt", ".git", ".GIT/config", ".gokin/state.json", "escape.txt", "linked/inside.txt", "binary.dat", "large.txt"} {
		if _, err := s.GetSessionFileSnapshot(project.ID, "default", path); err == nil {
			t.Fatalf("unsafe editor target %q accepted", path)
		}
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "secret" {
		t.Fatalf("outside target changed: %q, %v", data, err)
	}
}

func TestSessionFileEditorRejectsReadOnlyAndInvalidRevision(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Read only")
	path := filepath.Join(project.Directory, "locked.txt")
	if err := os.WriteFile(path, []byte("locked"), 0o400); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.GetSessionFileSnapshot(project.ID, "default", "locked.txt")
	if err != nil || !snapshot.ReadOnly {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	if _, err := s.SaveSessionFileContent(project.ID, "default", "locked.txt", "new", snapshot.Revision); err == nil {
		t.Fatal("read-only file was saved")
	}
	if _, err := s.SaveSessionFileContent(project.ID, "default", "locked.txt", "new", "bad"); err == nil {
		t.Fatal("invalid revision was accepted")
	}
}

func TestSessionFileEditorConcurrentSameRevisionHasOneWinner(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Concurrent editor")
	path := filepath.Join(project.Directory, "shared.txt")
	if err := os.WriteFile(path, []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.GetSessionFileSnapshot(project.ID, "default", "shared.txt")
	if err != nil {
		t.Fatal(err)
	}

	const writers = 12
	start := make(chan struct{})
	results := make(chan *SessionFileSaveResult, writers)
	errors := make(chan error, writers)
	var wg sync.WaitGroup
	for index := 0; index < writers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			result, err := s.SaveSessionFileContent(project.ID, "default", "shared.txt", fmt.Sprintf("writer-%d", index), snapshot.Revision)
			if err != nil {
				errors <- err
				return
			}
			results <- result
		}(index)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatalf("concurrent save: %v", err)
	}
	winners, conflicts := 0, 0
	for result := range results {
		if result.Saved {
			winners++
		}
		if result.Conflict {
			conflicts++
		}
	}
	if winners != 1 || conflicts != writers-1 {
		t.Fatalf("winners=%d conflicts=%d, want 1/%d", winners, conflicts, writers-1)
	}
}
