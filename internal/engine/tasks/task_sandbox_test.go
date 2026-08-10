package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/security"
)

func TestBackgroundTaskUsesWorkspaceSandboxWhenAvailable(t *testing.T) {
	status := security.DetectWorkspaceIsolation()
	if !status.Available {
		t.Skip(status.Detail)
	}
	workspace := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(workspace, "home-link")); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join("/private/tmp", fmt.Sprintf("gokin-background-escape-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(external) })
	command := strings.Join([]string{
		"printf background-ok > background.txt",
		"if ls home-link/ >/dev/null 2>&1; then exit 41; fi",
		"if touch " + shellQuoteTaskTest(external) + " >/dev/null 2>&1; then exit 42; fi",
	}, "\n")

	manager := NewManager(workspace)
	id, err := manager.Start(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	task, ok := manager.Get(id)
	if !ok {
		t.Fatalf("background task %q missing", id)
	}
	select {
	case <-task.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("background sandbox task timed out")
	}
	info := task.GetInfo()
	if info.Status != StatusCompleted.String() || info.ExitCode != 0 {
		t.Fatalf("background task = %#v", info)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "background.txt")); err != nil || string(data) != "background-ok" {
		t.Fatalf("workspace output = %q, %v", data, err)
	}
	if _, err := os.Stat(external); !os.IsNotExist(err) {
		t.Fatalf("background sandbox wrote outside workspace: %v", err)
	}
}

func TestManagerIsolationStatusMatchesNewTasks(t *testing.T) {
	manager := NewManager(t.TempDir())
	status := manager.WorkspaceIsolationStatus()
	if status.Enforced != security.DetectWorkspaceIsolation().Available {
		t.Fatalf("manager status = %#v", status)
	}
	manager.SetWorkspaceSandboxEnabled(false)
	if disabled := manager.WorkspaceIsolationStatus(); disabled.Enforced || disabled.Mode != "host" {
		t.Fatalf("disabled status = %#v", disabled)
	}
}

func shellQuoteTaskTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
