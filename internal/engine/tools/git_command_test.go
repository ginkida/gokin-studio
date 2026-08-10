package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGitCommitDoesNotExecuteRepositoryHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test hook uses a POSIX shell")
	}

	workDir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.name", "Gokin Test"},
		{"config", "user.email", "gokin@example.invalid"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}

	tracked := filepath.Join(workDir, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "add", "tracked.txt")
	add.Dir = workDir
	if output, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, output)
	}

	marker := filepath.Join(workDir, "hook-ran")
	hookDir := filepath.Join(workDir, ".git", "hooks")
	hook := filepath.Join(hookDir, "pre-commit")
	script := "#!/bin/sh\nprintf ran > " + marker + "\nexit 1\n"
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	result, err := NewGitCommitTool(workDir).Execute(context.Background(), map[string]any{
		"message": "safe commit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("commit failed: %s", result.Error)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("repository pre-commit hook executed; stat error=%v", err)
	}
}
