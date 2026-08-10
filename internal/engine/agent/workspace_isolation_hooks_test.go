package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPrepareGitWorktreeDoesNotExecuteRepositoryHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test hook uses a POSIX shell")
	}

	repo := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.name", "Gokin Test"},
		{"config", "user.email", "gokin@example.invalid"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "-m", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}

	marker := filepath.Join(t.TempDir(), "post-checkout-ran")
	hook := filepath.Join(repo, ".git", "hooks", "post-checkout")
	script := "#!/bin/sh\nprintf ran > " + marker + "\nexit 1\n"
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	workspace, err := prepareGitWorktree(repo, false)
	if err != nil {
		t.Fatalf("prepareGitWorktree failed: %v", err)
	}
	defer workspace.Cleanup()

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("repository post-checkout hook executed; stat error=%v", err)
	}
}
