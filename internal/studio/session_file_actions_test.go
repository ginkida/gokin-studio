package studio

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func withTestSessionFileApplications(t *testing.T, applications []sessionFileApplicationCommand) {
	t.Helper()
	previous := discoverSessionFileApplications
	discoverSessionFileApplications = func() []sessionFileApplicationCommand { return applications }
	t.Cleanup(func() { discoverSessionFileApplications = previous })
}

func TestSessionFileActionsResolveAndLaunchFixedApplication(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "File actions")
	path := filepath.Join(project.Directory, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withTestSessionFileApplications(t, []sessionFileApplicationCommand{{
		SessionFileApplication: SessionFileApplication{ID: "test-editor", Name: "Test Editor"},
		Command:                "test-editor-bin",
		Args:                   []string{"--reuse-window"},
	}})
	record := captureExec(t)

	actions, err := s.ListSessionFileActions(project.ID, "default", "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if actions.Path != "src/main.go" || actions.AbsolutePath != path || len(actions.Applications) != 1 || actions.Applications[0].ID != "test-editor" {
		t.Fatalf("actions = %#v", actions)
	}
	if err := s.OpenSessionFileInApplication(project.ID, "default", "src/main.go", "test-editor"); err != nil {
		t.Fatal(err)
	}
	if !record.called || record.cmd != "test-editor-bin" || !slices.Equal(record.args, []string{"--reuse-window", path}) {
		t.Fatalf("launch = %q %#v", record.cmd, record.args)
	}
}

func TestSessionFileActionsRevealExactFile(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Reveal")
	path := filepath.Join(project.Directory, "notes.txt")
	if err := os.WriteFile(path, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := captureExec(t)
	if err := s.ShowSessionFileInFileManager(project.ID, "default", "notes.txt"); err != nil {
		t.Fatal(err)
	}
	if !record.called {
		t.Fatal("file manager was not launched")
	}
	joined := strings.Join(record.args, "\x00")
	if !strings.Contains(joined, path) && !strings.Contains(joined, filepath.Dir(path)) {
		t.Fatalf("resolved path missing from reveal args: %#v", record.args)
	}
}

func TestSessionFileActionsRejectInjectionAndUnsafePaths(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Safe actions")
	if err := os.WriteFile(filepath.Join(project.Directory, "safe.txt"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project.Directory, "escape.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	withTestSessionFileApplications(t, []sessionFileApplicationCommand{{
		SessionFileApplication: SessionFileApplication{ID: "editor", Name: "Editor"}, Command: "editor",
	}})
	record := captureExec(t)
	for _, path := range []string{"../outside.txt", ".git", ".GOKIN/state", "escape.txt"} {
		if _, err := s.ListSessionFileActions(project.ID, "default", path); err == nil {
			t.Fatalf("unsafe path %q accepted", path)
		}
	}
	if err := s.OpenSessionFileInApplication(project.ID, "default", "safe.txt", "editor; rm -rf /"); err == nil {
		t.Fatal("arbitrary application ID accepted")
	}
	if record.called {
		t.Fatal("unsafe action launched a process")
	}
}
