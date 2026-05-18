package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestListDirectory_UnknownProject verifies that an unknown project ID returns
// an error rather than panicking or listing arbitrary directories.
func TestListDirectory_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.ListDirectory("no-such-id", ""); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestListDirectory_PathTraversal verifies that "../" sequences are resolved
// and rejected before hitting the filesystem.
func TestListDirectory_PathTraversal(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")

	// "../../etc" resolves to a path outside the project directory.
	if _, err := s.ListDirectory(info.ID, "../../etc"); err == nil {
		t.Error("expected error for '../' traversal, got nil")
	}
	if !containsStr(err1(s, info.ID, "../../etc"), "outside") {
		t.Errorf("expected 'outside' in error, got %q", err1(s, info.ID, "../../etc"))
	}
}

// TestListDirectory_Lists verifies that listing the root directory returns the
// expected entries: directories before files, alphabetically within each group,
// with hidden files and noise directories (node_modules, __pycache__) excluded.
func TestListDirectory_Lists(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()

	// Populate: hidden file, noise dir, a real subdir, two real files.
	for _, d := range []string{"src", "pkg", "node_modules", "__pycache__"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"main.go", "go.mod", ".gitignore"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	info, err := s.AddProject("Q", dir)
	if err != nil {
		t.Fatal(err)
	}

	entries, err := s.ListDirectory(info.ID, "")
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}

	// Noise entries must be absent.
	for _, bad := range []string{"node_modules", "__pycache__", ".gitignore"} {
		if names[bad] {
			t.Errorf("unexpected entry %q in listing", bad)
		}
	}
	// Real entries must be present.
	for _, want := range []string{"src", "pkg", "main.go", "go.mod"} {
		if !names[want] {
			t.Errorf("missing entry %q in listing", want)
		}
	}

	// Directories must precede files.
	seenFile := false
	for _, e := range entries {
		if !e.IsDir {
			seenFile = true
		}
		if seenFile && e.IsDir {
			t.Error("directory appeared after file — not sorted dirs-first")
			break
		}
	}

	// Within the directory group verify alphabetical order.
	dirs := filterDirs(entries, true)
	for i := 1; i < len(dirs); i++ {
		if dirs[i-1].Name > dirs[i].Name {
			t.Errorf("directories not sorted: %q > %q", dirs[i-1].Name, dirs[i].Name)
		}
	}
}

// TestListDirectory_Subdirectory verifies that a sub-path is listed correctly
// and that paths in entries are relative to the project root (not absolute).
func TestListDirectory_Subdirectory(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dir, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := s.AddProject("R", dir)
	if err != nil {
		t.Fatal(err)
	}

	entries, err := s.ListDirectory(info.ID, "src")
	if err != nil {
		t.Fatalf("ListDirectory(src): %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "main.go" {
		t.Fatalf("expected [main.go], got %v", entries)
	}
	// The Path field must be relative to the project root, not absolute.
	if filepath.IsAbs(entries[0].Path) {
		t.Errorf("entry Path should be relative, got %q", entries[0].Path)
	}
	if entries[0].Path != filepath.Join("src", "main.go") {
		t.Errorf("entry Path = %q, want %q", entries[0].Path, filepath.Join("src", "main.go"))
	}
}

// TestReadFileContent_UnknownProject verifies that an unknown project ID errors.
func TestReadFileContent_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.ReadFileContent("no-such-id", "file.txt"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestReadFileContent_PathTraversal verifies traversal via "../" is rejected.
func TestReadFileContent_PathTraversal(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")

	_, err := s.ReadFileContent(info.ID, "../../etc/passwd")
	if err == nil {
		t.Error("expected error for '../' traversal, got nil")
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("error = %q, want 'outside' substring", err.Error())
	}
}

// TestReadFileContent_ReadsFile verifies that a normal file is read verbatim.
func TestReadFileContent_ReadsFile(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()

	const body = "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := s.AddProject("S", dir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ReadFileContent(info.ID, "main.go")
	if err != nil {
		t.Fatalf("ReadFileContent: %v", err)
	}
	if got != body {
		t.Errorf("content = %q, want %q", got, body)
	}
}

// TestReadFileContent_EmptyFile verifies that an empty file returns "" (not an error).
func TestReadFileContent_EmptyFile(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "empty.txt"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	info, err := s.AddProject("T", dir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ReadFileContent(info.ID, "empty.txt")
	if err != nil {
		t.Fatalf("ReadFileContent empty: %v", err)
	}
	if got != "" {
		t.Errorf("empty file content = %q, want empty string", got)
	}
}

// TestReadFileContent_TruncatesAt100KB verifies that files larger than 100KB
// are truncated with a notice rather than sent in full.
func TestReadFileContent_TruncatesAt100KB(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()

	// Write 150 KB of 'x' characters.
	large := make([]byte, 150*1024)
	for i := range large {
		large[i] = 'x'
	}
	if err := os.WriteFile(filepath.Join(dir, "large.bin"), large, 0644); err != nil {
		t.Fatal(err)
	}

	info, err := s.AddProject("U", dir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ReadFileContent(info.ID, "large.bin")
	if err != nil {
		t.Fatalf("ReadFileContent large: %v", err)
	}
	if !strings.Contains(got, "[truncated at 100KB]") {
		t.Error("large file missing truncation notice")
	}
	// The 100KB of content + newlines + notice should be notably less than 150KB.
	if len(got) >= 150*1024 {
		t.Errorf("content len %d >= 150KB — file was not truncated", len(got))
	}
}

// TestReadFileContent_MissingFile verifies that a missing file returns an error.
func TestReadFileContent_MissingFile(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "V")

	if _, err := s.ReadFileContent(info.ID, "does-not-exist.txt"); err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

// TestListDirectory_MissingSubdirectory verifies that listing a sub-path that
// doesn't exist within the project directory returns an error (from os.ReadDir),
// not a nil slice — so the frontend can distinguish "empty dir" from "not found".
func TestListDirectory_MissingSubdirectory(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")

	if _, err := s.ListDirectory(info.ID, "nonexistent-subdirectory"); err == nil {
		t.Error("expected error listing nonexistent subdirectory, got nil")
	}
}

// --- helpers ---

func err1(s *Studio, projectID, subPath string) string {
	_, err := s.ListDirectory(projectID, subPath)
	if err == nil {
		return ""
	}
	return err.Error()
}

func filterDirs(entries []FileEntry, isDir bool) []FileEntry {
	var out []FileEntry
	for _, e := range entries {
		if e.IsDir == isDir {
			out = append(out, e)
		}
	}
	return out
}
