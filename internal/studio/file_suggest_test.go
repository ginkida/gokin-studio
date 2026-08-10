package studio

import (
	"os"
	"path/filepath"
	"testing"
)

// TestListProjectFiles_BasicWalk creates a small project tree and confirms
// the walker returns all regular files relative to root, sorted.
func TestListProjectFiles_BasicWalk(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()

	files := []string{
		"README.md",
		"src/main.go",
		"src/util/helper.go",
		"docs/intro.md",
	}
	for _, rel := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	info, err := s.AddProject("walker", dir)
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	got, err := s.ListProjectFiles(info.ID)
	if err != nil {
		t.Fatalf("ListProjectFiles: %v", err)
	}
	if len(got) != len(files) {
		t.Fatalf("got %d files, want %d: %v", len(got), len(files), got)
	}
	// Sorted alphabetically (filepath.ToSlash applied).
	want := []string{
		"README.md",
		"docs/intro.md",
		"src/main.go",
		"src/util/helper.go",
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestListProjectFiles_ExcludesNoise confirms the hardcoded noise dirs
// and hidden directories are skipped.
func TestListProjectFiles_ExcludesNoise(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()

	// Real files we want to find:
	keep := []string{
		"README.md",
		"src/main.go",
	}
	// Noise: should be excluded.
	noise := []string{
		"node_modules/foo/bar.js",
		"dist/bundle.js",
		"build/output.exe",
		".git/HEAD",
		".gokin/pinned_context.md",
		".idea/workspace.xml",
		".venv/lib/python3.11/site-packages/requests/__init__.py",
		"__pycache__/foo.pyc",
		".cache/file.tmp",
		// Hidden dir at any depth
		"src/.hidden/secret.txt",
	}
	for _, rel := range append(keep, noise...) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	info, err := s.AddProject("noise-test", dir)
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	got, err := s.ListProjectFiles(info.ID)
	if err != nil {
		t.Fatalf("ListProjectFiles: %v", err)
	}

	// Only the kept files should appear.
	if len(got) != len(keep) {
		t.Errorf("expected %d files (only non-noise), got %d: %v", len(keep), len(got), got)
	}
	for _, k := range keep {
		found := false
		for _, g := range got {
			if g == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected to find %q in results, got %v", k, got)
		}
	}
	for _, n := range noise {
		nNorm := filepath.ToSlash(n)
		for _, g := range got {
			if g == nNorm {
				t.Errorf("noise file %q should have been excluded, but found in results", n)
			}
		}
	}
}

// TestListProjectFiles_KeepsHiddenFilesAtRoot confirms that .gitignore
// and similar root-level hidden files are kept (only hidden DIRECTORIES
// are skipped).
func TestListProjectFiles_KeepsHiddenFilesAtRoot(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()

	for _, name := range []string{".gitignore", ".env.example", "README.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	info, _ := s.AddProject("hidden-files", dir)
	got, err := s.ListProjectFiles(info.ID)
	if err != nil {
		t.Fatalf("ListProjectFiles: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 files including hidden ones, got %d: %v", len(got), got)
	}
}

// TestListProjectFiles_UnknownProject errors cleanly.
func TestListProjectFiles_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.ListProjectFiles("no-such-id"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestListProjectFiles_EmptyProjectID rejected.
func TestListProjectFiles_EmptyProjectID(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.ListProjectFiles(""); err == nil {
		t.Error("expected error for empty projectID, got nil")
	}
}

// TestListProjectFiles_EmptyDir returns an empty list cleanly.
func TestListProjectFiles_EmptyDir(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()
	info, _ := s.AddProject("empty", dir)
	got, err := s.ListProjectFiles(info.ID)
	if err != nil {
		t.Fatalf("ListProjectFiles: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 files in empty dir, got %d: %v", len(got), got)
	}
}

func TestListProjectFilesSkipsSymlinks(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "outside-link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("real.txt", filepath.Join(dir, "inside-link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	info, err := s.AddProject("links", dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ListProjectFiles(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "real.txt" {
		t.Fatalf("symlinks leaked into suggestions: %v", got)
	}
}
