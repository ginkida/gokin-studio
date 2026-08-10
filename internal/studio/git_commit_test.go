package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitChanges_UnknownProject(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	if _, err := s.CommitChanges("nope", "msg"); err == nil {
		t.Fatal("expected error for unknown project")
	}
}

func TestCommitChanges_EmptyMessage(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")
	for _, m := range []string{"", "   ", "\n\t"} {
		if _, err := s.CommitChanges(info.ID, m); err == nil {
			t.Errorf("expected error for empty message %q", m)
		}
	}
}

func TestCommitChanges_NotARepo(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P") // plain temp dir, no git init
	if _, err := s.CommitChanges(info.ID, "msg"); err == nil {
		t.Fatal("expected error for non-repo directory")
	}
}

func TestCommitChanges_NothingToCommit(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir) // initial empty commit → clean tree
	info, err := s.AddProject("P", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitChanges(info.ID, "nothing here"); err == nil {
		t.Fatal("expected 'nothing to commit' error on a clean tree")
	}
}

func TestCommitChanges_Success(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)

	// A tracked modification + a brand-new untracked file → both committed
	// (git add -A stages untracked too).
	if err := writeFile(filepath.Join(dir, "tracked.txt"), "v1\n"); err != nil {
		t.Fatal(err)
	}
	gitMust(t, dir, "add", "tracked.txt")
	gitMust(t, dir, "commit", "-m", "add tracked")
	if err := writeFile(filepath.Join(dir, "tracked.txt"), "v2\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, "fresh.txt"), "new\n"); err != nil {
		t.Fatal(err)
	}

	info, err := s.AddProject("P", dir)
	if err != nil {
		t.Fatal(err)
	}

	res, err := s.CommitChanges(info.ID, "wire up the context panel")
	if err != nil {
		t.Fatalf("CommitChanges: %v", err)
	}
	if res.Hash == "" {
		t.Error("expected a commit hash")
	}
	if res.Subject != "wire up the context panel" {
		t.Errorf("Subject = %q", res.Subject)
	}
	if res.Branch != "main" {
		t.Errorf("Branch = %q, want main", res.Branch)
	}

	// Working tree should be clean now, and the commit should be in the log
	// with both files included.
	if out := runGit(dir, "status", "--porcelain"); out != "" {
		t.Errorf("working tree not clean after commit: %q", out)
	}
	if log := runGit(dir, "log", "--oneline", "-1"); !strings.Contains(log, "wire up the context panel") {
		t.Errorf("commit not in log: %q", log)
	}
	if files := runGit(dir, "show", "--name-only", "--pretty=format:", "HEAD"); !strings.Contains(files, "fresh.txt") {
		t.Errorf("untracked fresh.txt not included in commit: %q", files)
	}
}

func TestCommitSessionChangesCommitsOnlySelectedWorktree(t *testing.T) {
	s := newStudioForTest(t)
	repo := prepareSessionWorktreeRepo(t)
	info, err := s.AddProject("session-commit", repo)
	if err != nil {
		t.Fatal(err)
	}
	session := s.projects[info.ID].sessions["default"].Info()
	if err := writeFile(filepath.Join(session.WorktreePath, "tracked.txt"), "worktree-only\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(repo, "root-only.txt"), "leave me uncommitted\n"); err != nil {
		t.Fatal(err)
	}

	result, err := s.CommitSessionChanges(info.ID, "default", "commit isolated review")
	if err != nil {
		t.Fatalf("CommitSessionChanges: %v", err)
	}
	if result.Branch != session.WorktreeBranch || result.Hash == "" {
		t.Fatalf("commit result = %+v", result)
	}
	if status := runGit(session.WorktreePath, "status", "--porcelain"); status != "" {
		t.Fatalf("session checkout remains dirty: %q", status)
	}
	if rootStatus := runGit(repo, "status", "--porcelain"); !strings.Contains(rootStatus, "root-only.txt") {
		t.Fatalf("project root change was unexpectedly committed: %q", rootStatus)
	}
	rootTracked, err := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil || string(rootTracked) != "root-v1\n" {
		t.Fatalf("session commit changed project root: %q, %v", rootTracked, err)
	}
}
