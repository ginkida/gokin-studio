package studio

import (
	"path/filepath"
	"testing"
)

// TestGetProjectGitContext_DiffStat verifies the +N/-M line counts surfaced
// to the context panel's "Changes" indicator (git diff HEAD --numstat).
func TestGetProjectGitContext_DiffStat(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)

	dir := t.TempDir()
	initGitRepo(t, dir)
	f := filepath.Join(dir, "a.txt")
	if err := writeFile(f, "line1\nline2\nline3\n"); err != nil {
		t.Fatal(err)
	}
	gitMust(t, dir, "add", "a.txt")
	gitMust(t, dir, "commit", "-m", "init")

	// Change line2 and append two new lines → +3 / -1 vs HEAD.
	if err := writeFile(f, "line1\nCHANGED\nline3\nnew4\nnew5\n"); err != nil {
		t.Fatal(err)
	}

	info, err := s.AddProject("P", dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.GetProjectGitContext(info.ID)
	if err != nil {
		t.Fatalf("GetProjectGitContext: %v", err)
	}
	if ctx.Insertions != 3 || ctx.Deletions != 1 {
		t.Errorf("diff stat = +%d -%d, want +3 -1", ctx.Insertions, ctx.Deletions)
	}
}

// TestGetProjectGitContext_DiffStatClean verifies a committed-and-unchanged
// tree reports zero insertions/deletions (the "working tree clean" case).
func TestGetProjectGitContext_DiffStatClean(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)

	dir := t.TempDir()
	initGitRepo(t, dir)
	if err := writeFile(filepath.Join(dir, "a.txt"), "x\n"); err != nil {
		t.Fatal(err)
	}
	gitMust(t, dir, "add", "a.txt")
	gitMust(t, dir, "commit", "-m", "init")

	info, err := s.AddProject("P", dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.GetProjectGitContext(info.ID)
	if err != nil {
		t.Fatalf("GetProjectGitContext: %v", err)
	}
	if ctx.Insertions != 0 || ctx.Deletions != 0 {
		t.Errorf("clean tree diff stat = +%d -%d, want +0 -0", ctx.Insertions, ctx.Deletions)
	}
}
