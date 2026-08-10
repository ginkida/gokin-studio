package studio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSessionTerminalAtUsesValidatedSubdirectory(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Terminal directory")
	want := filepath.Join(project.Directory, "src", "feature")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}

	termID, err := s.OpenSessionTerminalAt(project.ID, "default", "src/feature")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.CloseTerminal(termID) })
	s.mu.RLock()
	terminal := s.terminals[termID]
	s.mu.RUnlock()
	if terminal == nil || terminal.cmd.Dir != want {
		t.Fatalf("terminal cwd = %v, want %q", terminal, want)
	}

	actions, err := s.ListSessionPathActions(project.ID, "default", "src/feature")
	if err != nil {
		t.Fatal(err)
	}
	if !actions.IsDirectory || actions.Path != "src/feature" || actions.AbsolutePath != want || len(actions.Applications) != 0 {
		t.Fatalf("directory actions = %#v", actions)
	}
}

func TestSessionTerminalDirectoryRejectsUnsafePaths(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Safe terminal directory")
	if err := os.WriteFile(filepath.Join(project.Directory, "file.txt"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(project.Directory, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	for _, path := range []string{"../outside", "file.txt", ".git", ".GOKIN/cache", "escape"} {
		if _, _, err := sessionTerminalDirectory(s, project.ID, "default", path); err == nil {
			t.Fatalf("unsafe terminal directory %q accepted", path)
		}
		if _, err := s.ListSessionPathActions(project.ID, "default", path); err == nil && path != "file.txt" {
			t.Fatalf("unsafe context-menu path %q accepted", path)
		}
	}
}

func TestOpenSessionTerminalAtDefaultsToWorktreeRoot(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Root terminal")
	termID, err := s.OpenSessionTerminalAt(project.ID, "default", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.CloseTerminal(termID) })
	s.mu.RLock()
	terminal := s.terminals[termID]
	s.mu.RUnlock()
	if terminal == nil || terminal.cmd.Dir != project.Directory {
		t.Fatalf("root terminal cwd = %v, want %q", terminal, project.Directory)
	}
}
