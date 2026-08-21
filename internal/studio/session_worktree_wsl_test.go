package studio

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// simulateWindowsHost makes the Windows-only branches reachable from a test on
// any platform. Only the GOOS decision is faked; everything else runs for real.
func simulateWindowsHost(t *testing.T) {
	t.Helper()
	previous := hostGOOS
	hostGOOS = "windows"
	t.Cleanup(func() { hostGOOS = previous })
}

// A WSL project's repository lives inside the distro. A worktree created under
// <configDir>/worktrees on the Windows drive would be linked to it by an
// absolute Windows path that the distro's git cannot resolve — and that
// checkout would then become the agent's working directory, putting it outside
// WSL entirely.
func TestProvisionSessionWorktreeSkipsRemoteProjectDirectory(t *testing.T) {
	simulateWindowsHost(t)
	withTempConfigDir(t)

	project := &Project{ID: "p1", Directory: `\\wsl.localhost\Ubuntu\home\me\repo`}
	session := NewChatSession("Chat 1")

	// Must be nil, not an error: every caller treats a non-nil error as fatal
	// and AddProject would refuse the project outright.
	if err := provisionSessionWorktree(project, session, ""); err != nil {
		t.Fatalf("provisionSessionWorktree returned %v; a WSL project must be accepted", err)
	}

	session.mu.RLock()
	defer session.mu.RUnlock()
	if session.IsolationSkippedReason == "" {
		t.Fatal("the skip was not explained to the user")
	}
	if !strings.Contains(session.IsolationSkippedReason, "WSL") {
		t.Fatalf("reason = %q", session.IsolationSkippedReason)
	}
	// A non-empty WorktreeError turns into a hard per-turn failure.
	if session.WorktreeError != "" {
		t.Fatalf("WorktreeError = %q; it must stay empty or every turn fails", session.WorktreeError)
	}
	if session.WorktreePath != "" || session.WorktreeWorkDir != "" {
		t.Fatalf("a checkout was recorded anyway: %q / %q", session.WorktreePath, session.WorktreeWorkDir)
	}
}

// The same call on a real local repository must behave exactly as before.
func TestProvisionSessionWorktreeUnchangedForLocalDirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	withTempConfigDir(t)
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git setup failed: %v (%s)", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git commit failed: %v (%s)", err, out)
		}
	}

	project := &Project{ID: "p-local", Directory: repo}
	session := NewChatSession("Chat 1")
	if err := provisionSessionWorktree(project, session, repo); err != nil {
		t.Fatalf("provisionSessionWorktree: %v", err)
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	if session.IsolationSkippedReason != "" {
		t.Fatalf("a local project was treated as remote: %q", session.IsolationSkippedReason)
	}
	if session.WorktreePath == "" {
		t.Fatal("a local project lost its isolated checkout")
	}
}

// The pre-existing "not a git repository" early return must be untouched.
func TestProvisionSessionWorktreeStillReturnsNilForNonGitDirectory(t *testing.T) {
	withTempConfigDir(t)
	project := &Project{ID: "p-plain", Directory: t.TempDir()}
	session := NewChatSession("Chat 1")
	if err := provisionSessionWorktree(project, session, project.Directory); err != nil {
		t.Fatalf("provisionSessionWorktree: %v", err)
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	if session.WorktreePath != "" || session.IsolationSkippedReason != "" {
		t.Fatalf("non-git directory produced %q / %q", session.WorktreePath, session.IsolationSkippedReason)
	}
}

func TestChatSessionInfoSurfacesIsolationSkippedReason(t *testing.T) {
	session := NewChatSession("Chat 1")
	session.IsolationSkippedReason = wslIsolationSkippedReason
	if got := session.Info().IsolationSkippedReason; got != wslIsolationSkippedReason {
		t.Fatalf("Info().IsolationSkippedReason = %q", got)
	}
	// A normal session says nothing, so the UI renders no notice.
	if got := NewChatSession("Chat 2").Info().IsolationSkippedReason; got != "" {
		t.Fatalf("an ordinary session reported %q", got)
	}
}

// remoteProjectDirectory is the single decision point; it must be inert off
// Windows so macOS and Linux keep byte-identical behaviour.
func TestRemoteProjectDirectoryIsWindowsOnly(t *testing.T) {
	if remoteProjectDirectory(`\\wsl.localhost\Ubuntu\home\me\repo`) {
		t.Fatal("a WSL-shaped path was treated as remote off Windows")
	}
	simulateWindowsHost(t)
	if !remoteProjectDirectory(`\\wsl.localhost\Ubuntu\home\me\repo`) {
		t.Fatal("a WSL path was not recognised on Windows")
	}
	for _, dir := range []string{`C:\Users\me\repo`, "/home/me/repo", ""} {
		if remoteProjectDirectory(dir) {
			t.Fatalf("%q was treated as remote", dir)
		}
	}
}
