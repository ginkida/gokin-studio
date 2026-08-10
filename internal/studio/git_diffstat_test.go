package studio

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

func TestGetSessionGitReviewUsesWorktreeAndIncludesUntrackedAndBinary(t *testing.T) {
	s := newStudioForTest(t)
	repo := prepareSessionWorktreeRepo(t)
	info, err := s.AddProject("review-worktree", repo)
	if err != nil {
		t.Fatal(err)
	}
	session := s.projects[info.ID].sessions["default"].Info()
	if err := writeFile(filepath.Join(session.WorktreePath, "tracked.txt"), "session change\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(session.WorktreePath, "fresh.txt"), "one\ntwo\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session.WorktreePath, "image.bin"), []byte{0, 1, 2, 0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(repo, "root-only.txt"), "must not leak\n"); err != nil {
		t.Fatal(err)
	}

	review, err := s.GetSessionGitReview(info.ID, "default")
	if err != nil {
		t.Fatalf("GetSessionGitReview: %v", err)
	}
	if !review.IsRepo || review.Branch != session.WorktreeBranch || review.Fingerprint == "" {
		t.Fatalf("review metadata = %+v", review)
	}
	files := make(map[string]GitReviewFile, len(review.Files))
	for _, file := range review.Files {
		files[file.Path] = file
	}
	if _, leaked := files["root-only.txt"]; leaked {
		t.Fatal("session review leaked a project-root-only file")
	}
	tracked, ok := files["tracked.txt"]
	if !ok || tracked.Status != "modified" || !strings.Contains(tracked.Patch, "+session change") {
		t.Fatalf("tracked review = %+v", tracked)
	}
	fresh, ok := files["fresh.txt"]
	if !ok || fresh.Status != "untracked" || fresh.Insertions != 2 || !strings.Contains(fresh.Patch, "+one") {
		t.Fatalf("untracked review = %+v", fresh)
	}
	binary, ok := files["image.bin"]
	if !ok || !binary.Binary || !strings.Contains(binary.Patch, "Binary files") {
		t.Fatalf("binary review = %+v", binary)
	}
	if review.Insertions < 3 || review.Deletions < 1 {
		t.Fatalf("review stats = +%d -%d", review.Insertions, review.Deletions)
	}
}

func TestGetSessionGitReviewRenameAndBoundsLargeUntrackedFile(t *testing.T) {
	s := newStudioForTest(t)
	repo := prepareSessionWorktreeRepo(t)
	info, err := s.AddProject("review-rename", repo)
	if err != nil {
		t.Fatal(err)
	}
	workDir := s.projects[info.ID].sessions["default"].Info().WorktreePath
	if err := os.Rename(filepath.Join(workDir, "tracked.txt"), filepath.Join(workDir, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	// Staging makes Git record the pair as a rename in porcelain -z; an
	// entirely unstaged move may legitimately appear as deleted + untracked.
	gitMust(t, workDir, "add", "-A")
	large := bytes.Repeat([]byte("bounded review line\n"), (gitReviewMaxFileBytes/20)+100)
	if err := os.WriteFile(filepath.Join(workDir, "large.txt"), large, 0o600); err != nil {
		t.Fatal(err)
	}

	review, err := s.GetSessionGitReview(info.ID, "default")
	if err != nil {
		t.Fatal(err)
	}
	var renamed, largeFile *GitReviewFile
	for index := range review.Files {
		switch review.Files[index].Path {
		case "renamed.txt":
			renamed = &review.Files[index]
		case "large.txt":
			largeFile = &review.Files[index]
		}
	}
	if renamed == nil || renamed.Status != "renamed" || renamed.PreviousPath != "tracked.txt" {
		t.Fatalf("rename was not parsed from porcelain -z: %+v", renamed)
	}
	if largeFile == nil || !largeFile.Truncated || len(largeFile.Patch) > gitReviewMaxFileBytes+512 {
		t.Fatalf("large untracked preview was not bounded: %+v", largeFile)
	}
	if !review.Truncated {
		t.Fatal("aggregate review did not report truncation")
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

func TestGetProjectGitReviewReturnsTrackedUnifiedDiff(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	path := filepath.Join(dir, "review.txt")
	if err := writeFile(path, "before\n"); err != nil {
		t.Fatal(err)
	}
	gitMust(t, dir, "add", "review.txt")
	gitMust(t, dir, "commit", "-m", "baseline")
	if err := writeFile(path, "after\nadded\n"); err != nil {
		t.Fatal(err)
	}
	info, err := s.AddProject("P", dir)
	if err != nil {
		t.Fatal(err)
	}
	review, err := s.GetProjectGitReview(info.ID)
	if err != nil {
		t.Fatalf("GetProjectGitReview: %v", err)
	}
	if !review.IsRepo || review.Truncated {
		t.Fatalf("review metadata = %+v", review)
	}
	for _, want := range []string{"diff --git", "-before", "+after", "+added"} {
		if !strings.Contains(review.Diff, want) {
			t.Errorf("diff missing %q:\n%s", want, review.Diff)
		}
	}
}

func TestGetProjectGitReviewNonRepoAndUnknownProject(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "plain")
	review, err := s.GetProjectGitReview(info.ID)
	if err != nil || review.IsRepo || review.Diff != "" {
		t.Fatalf("non-repo review = %+v, err=%v", review, err)
	}
	if _, err := s.GetProjectGitReview("missing"); err == nil {
		t.Fatal("unknown project did not fail")
	}
}
