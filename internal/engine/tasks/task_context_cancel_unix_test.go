//go:build unix

package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTaskParentContextCancellationKillsProcessGroup(t *testing.T) {
	workDir := t.TempDir()
	pidPath := filepath.Join(workDir, "child.pid")
	task := NewTaskWithArgs(
		"context-group-cancel",
		"/bin/sh",
		[]string{"-c", `sleep 30 & child=$!; printf '%s' "$child" > child.pid; wait "$child"`},
		workDir,
	)
	ctx, cancel := context.WithCancel(context.Background())
	if err := task.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}

	childPID := waitForTaskChildPID(t, pidPath)
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })
	cancel()

	select {
	case <-task.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Done remained blocked after parent context cancellation")
	}
	if got := task.GetStatus(); got != StatusCancelled {
		t.Fatalf("status = %s, want cancelled", got)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if err != nil {
			t.Fatalf("probe child process %d: %v", childPID, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d survived context cancellation", childPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTaskPreCancelledContextIsCancelledNotFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	task := NewTaskWithArgs("pre-cancelled", "/bin/sh", []string{"-c", "exit 0"}, t.TempDir())
	if err := task.Start(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case <-task.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("pre-cancelled task did not finish")
	}
	if got := task.GetStatus(); got != StatusCancelled {
		t.Fatalf("status = %s, want cancelled; error = %q", got, task.GetError())
	}
}

func TestTaskCommandFailureRemainsFailed(t *testing.T) {
	task := NewTaskWithArgs("command-failure", "/bin/sh", []string{"-c", "exit 23"}, t.TempDir())
	if err := task.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-task.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("failed task did not finish")
	}
	info := task.GetInfo()
	if info.Status != StatusFailed.String() || info.ExitCode != 23 {
		t.Fatalf("task info = %+v, want failed with exit code 23", info)
	}
}

func TestTaskNilContextUsesBackgroundContext(t *testing.T) {
	task := NewTaskWithArgs("nil-context", "/bin/sh", []string{"-c", "exit 0"}, t.TempDir())
	if err := task.Start(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-task.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("task with nil context did not finish")
	}
	if got := task.GetStatus(); got != StatusCompleted {
		t.Fatalf("status = %s, want completed", got)
	}
}

func waitForTaskChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil || pid <= 0 {
				t.Fatalf("invalid child PID %q: %v", data, parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read child PID: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("child process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
