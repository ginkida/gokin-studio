package studio

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initGitRepo runs the minimum git commands to set up a working repo
// in dir with one commit. Returns the dir for fluent test setup.
func initGitRepo(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
	cmds := [][]string{
		{"git", "init", "-q", "-b", "main"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "initial commit"},
	}
	for _, c := range cmds {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", c, err, out)
		}
	}
	return dir
}

func TestGetProjectGitContext_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.GetProjectGitContext("ghost"); err == nil {
		t.Error("expected error for unknown project")
	}
}

func TestGetProjectGitContext_NotARepo(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P") // tempdir, no git init
	ctx, err := s.GetProjectGitContext(pInfo.ID)
	if err != nil {
		t.Fatalf("expected nil error for non-repo, got %v", err)
	}
	if ctx.IsRepo {
		t.Error("IsRepo should be false for non-git directory")
	}
	if ctx.Branch != "" || len(ctx.ChangedFiles) > 0 || len(ctx.RecentCommits) > 0 {
		t.Errorf("unexpected fields populated: %+v", ctx)
	}
}

// TestGetProjectGitContext_RealRepo seeds a tempdir with a git repo +
// commits + uncommitted changes, then verifies the snapshot shape.
func TestGetProjectGitContext_RealRepo(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)

	// Set up a real git repo BEFORE registering it as a project so
	// AddProject's directory-exists check passes.
	dir := t.TempDir()
	initGitRepo(t, dir)
	// Add an uncommitted modified file.
	tracked := filepath.Join(dir, "tracked.txt")
	if err := writeFile(tracked, "v1\n"); err != nil {
		t.Fatal(err)
	}
	gitMust(t, dir, "add", "tracked.txt")
	gitMust(t, dir, "commit", "-m", "add tracked")
	if err := writeFile(tracked, "v2 modified\n"); err != nil {
		t.Fatal(err)
	}
	// Untracked file
	if err := writeFile(filepath.Join(dir, "untracked.txt"), "new\n"); err != nil {
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
	if !ctx.IsRepo {
		t.Error("expected IsRepo true")
	}
	if ctx.Branch != "main" {
		t.Errorf("Branch = %q, want main", ctx.Branch)
	}
	// Should have 1 modified + 1 untracked.
	if len(ctx.ChangedFiles) != 1 || ctx.ChangedFiles[0].Path != "tracked.txt" || ctx.ChangedFiles[0].Status != "modified" {
		t.Errorf("ChangedFiles = %+v, want one modified tracked.txt", ctx.ChangedFiles)
	}
	if len(ctx.UntrackedFiles) != 1 || ctx.UntrackedFiles[0].Path != "untracked.txt" {
		t.Errorf("UntrackedFiles = %+v, want one untracked.txt", ctx.UntrackedFiles)
	}
	// At least the initial + "add tracked" commits.
	if len(ctx.RecentCommits) < 2 {
		t.Errorf("RecentCommits len = %d, want ≥2", len(ctx.RecentCommits))
	}
	for _, c := range ctx.RecentCommits {
		if c.Hash == "" || c.Subject == "" {
			t.Errorf("malformed commit: %+v", c)
		}
		if c.Age == "" {
			t.Errorf("missing Age for commit %q", c.Hash)
		}
	}
}

// TestGetProjectGitContext_RecentCommitsCappedAt5 verifies the -5 limit.
func TestGetProjectGitContext_RecentCommitsCappedAt5(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	for i := 0; i < 8; i++ {
		gitMust(t, dir, "commit", "--allow-empty", "-m", "commit")
	}
	info, _ := s.AddProject("P", dir)
	ctx, _ := s.GetProjectGitContext(info.ID)
	if len(ctx.RecentCommits) != 5 {
		t.Errorf("RecentCommits len = %d, want 5 (capped)", len(ctx.RecentCommits))
	}
}

// TestGetProjectGitContext_NoChangesEmptyLists verifies a clean repo
// reports no changes.
func TestGetProjectGitContext_NoChangesEmptyLists(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	info, _ := s.AddProject("P", dir)
	ctx, _ := s.GetProjectGitContext(info.ID)
	if !ctx.IsRepo {
		t.Fatal("expected IsRepo true")
	}
	if len(ctx.ChangedFiles) != 0 || len(ctx.UntrackedFiles) != 0 {
		t.Errorf("expected no changes, got changed=%v untracked=%v", ctx.ChangedFiles, ctx.UntrackedFiles)
	}
}

func TestHumanAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{2 * time.Minute, "2 minutes ago"},
		{1 * time.Minute, "1 minute ago"},
		{3 * time.Hour, "3 hours ago"},
		{1 * time.Hour, "1 hour ago"},
		{36 * time.Hour, "1 day ago"},
		{4 * 24 * time.Hour, "4 days ago"},
		{10 * 24 * time.Hour, "1 week ago"},
		{50 * 24 * time.Hour, "1 month ago"},
		{400 * 24 * time.Hour, "1 year ago"},
		{800 * 24 * time.Hour, "2 years ago"},
	}
	for _, c := range cases {
		if got := humanAge(c.d); got != c.want {
			t.Errorf("humanAge(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestPlural(t *testing.T) {
	if got := plural(1, "thing"); got != "1 thing" {
		t.Errorf("plural(1) = %q", got)
	}
	if got := plural(3, "thing"); got != "3 things" {
		t.Errorf("plural(3) = %q", got)
	}
	if got := plural(0, "thing"); got != "0 things" {
		t.Errorf("plural(0) = %q", got)
	}
}

// TestGetProjectGitContext_DetectsAddedAndDeleted seeds adds-then-stages
// and a deletion to exercise the "A" and "D" status branches in
// GetProjectGitContext that the modified-only test doesn't cover.
func TestGetProjectGitContext_DetectsAddedAndDeleted(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Commit a baseline so we have a "delete" to detect.
	_ = writeFile(filepath.Join(dir, "old.txt"), "x\n")
	gitMust(t, dir, "add", "old.txt")
	gitMust(t, dir, "commit", "-m", "baseline")

	// Now make: 1 added (staged), 1 deleted.
	_ = writeFile(filepath.Join(dir, "new.txt"), "n\n")
	gitMust(t, dir, "add", "new.txt")
	gitMust(t, dir, "rm", "old.txt")

	info, _ := s.AddProject("P", dir)
	ctx, _ := s.GetProjectGitContext(info.ID)

	statusByPath := map[string]string{}
	for _, f := range ctx.ChangedFiles {
		statusByPath[f.Path] = f.Status
	}
	if statusByPath["new.txt"] != "added" {
		t.Errorf("new.txt status = %q, want %q (statuses=%v)", statusByPath["new.txt"], "added", statusByPath)
	}
	if statusByPath["old.txt"] != "deleted" {
		t.Errorf("old.txt status = %q, want %q (statuses=%v)", statusByPath["old.txt"], "deleted", statusByPath)
	}
}

// TestGetProjectGitContext_AheadBehindWithUpstream sets up a "remote" by
// cloning the bare repo into a local-clone scenario so HEAD has a
// configured upstream. Verifies AheadBehind is populated when the local
// branch diverges.
func TestGetProjectGitContext_AheadBehindWithUpstream(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)

	// Bare repo serves as the "remote".
	bare := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "--bare")
	cmd.Dir = bare
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}

	// Clone into a working dir.
	work := t.TempDir() + "/work"
	cloneCmd := exec.Command("git", "clone", "-q", bare, work)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	gitMust(t, work, "config", "user.email", "test@example.com")
	gitMust(t, work, "config", "user.name", "Test")
	gitMust(t, work, "commit", "--allow-empty", "-m", "initial")
	gitMust(t, work, "push", "-q", "-u", "origin", "HEAD")
	// Add 2 ahead-of-upstream commits so the count is non-zero.
	gitMust(t, work, "commit", "--allow-empty", "-m", "ahead 1")
	gitMust(t, work, "commit", "--allow-empty", "-m", "ahead 2")

	info, _ := s.AddProject("P", work)
	ctx, err := s.GetProjectGitContext(info.ID)
	if err != nil {
		t.Fatalf("GetProjectGitContext: %v", err)
	}
	if ctx.AheadBehind != "+2 -0" {
		t.Errorf("AheadBehind = %q, want %q", ctx.AheadBehind, "+2 -0")
	}
}

// TestRunGit_NonZeroExitReturnsEmpty verifies the helper swallows errors
// from non-existent commands instead of crashing the caller.
func TestRunGit_NonZeroExitReturnsEmpty(t *testing.T) {
	// `git` with a bogus subcommand returns non-zero.
	got := runGit(t.TempDir(), "this-is-not-a-real-git-subcommand")
	if got != "" {
		t.Errorf("expected empty string for failed git, got %q", got)
	}
}

// gitMust runs git in dir and fatals on error.
func gitMust(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// writeFile is a tiny helper to keep the tests above terse.
func writeFile(path, content string) error {
	cmd := exec.Command("sh", "-c", "cat > "+path)
	cmd.Stdin = strings.NewReader(content)
	return cmd.Run()
}
