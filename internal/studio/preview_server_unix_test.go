//go:build !windows

package studio

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionPreviewServerLifecycleAndSecretFreeEnvironment(t *testing.T) {
	t.Setenv("GLM_API_KEY", "must-not-reach-preview")
	s := newStudioForTest(t)
	s.ctx = context.Background()
	repo := prepareSessionWorktreeRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "apps", "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(repo, "apps", "web", ".keep"), "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	config := fmt.Sprintf(`{"version":"0.0.1","configurations":[{"name":"fixture","runtimeExecutable":"sh","runtimeArgs":["-c","printf 'key=%%s env=%%s cwd=%%s\\n' \"${GLM_API_KEY:-hidden}\" \"${PREVIEW_FIXTURE:-missing}\" \"$PWD\"; while :; do sleep 1; done"],"cwd":"apps/web","env":{"PREVIEW_FIXTURE":"from-config"},"port":%d}]}`, port)
	if err := writeFile(filepath.Join(repo, ".claude", "launch.json"), config); err != nil {
		t.Fatal(err)
	}
	gitMust(t, repo, "add", ".claude/launch.json", "apps/web/.keep")
	gitMust(t, repo, "commit", "-m", "preview fixture")
	info, err := s.AddProject("preview", repo)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetSessionPreviewConfig(info.ID, "default")
	if err != nil || len(loaded.Configurations) != 1 {
		t.Fatalf("load preview config: %+v, %v", loaded, err)
	}
	if _, err := s.StartSessionPreviewServer(info.ID, "default", "fixture", "stale command"); err == nil {
		t.Fatal("start accepted a command that was not the reviewed snapshot")
	}
	status, err := s.StartSessionPreviewServer(info.ID, "default", "fixture", loaded.Configurations[0].Command)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "starting" || status.PID == 0 || status.Port != port || status.URL == "" || status.BridgeToken == "" {
		t.Fatalf("start status = %+v", status)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err = s.GetSessionPreviewServerStatus(info.ID, "default", "fixture")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(status.Logs, "key=hidden env=from-config") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if strings.Contains(status.Logs, "must-not-reach-preview") || !strings.Contains(status.Logs, "key=hidden env=from-config") || !strings.Contains(status.Logs, filepath.Join("apps", "web")) {
		t.Fatalf("preview environment/logs = %q", status.Logs)
	}
	if err := s.StopSessionPreviewServer(info.ID, "default", "fixture"); err != nil {
		t.Fatal(err)
	}
	status, err = s.GetSessionPreviewServerStatus(info.ID, "default", "fixture")
	if err != nil || status.State != "stopped" {
		t.Fatalf("stop status = %+v, %v", status, err)
	}
	workDir := s.projects[info.ID].sessions["default"].Info().WorktreePath
	if _, err := os.Stat(filepath.Join(workDir, ".claude", "launch.json")); err != nil {
		t.Fatal(err)
	}
	s.stopPreviewServers("", "", true)
}
