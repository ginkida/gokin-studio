package studio

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// captureExec swaps the package-level execCommand for the duration of the
// test so we observe the command + args without actually launching the OS
// file manager. Restores on cleanup.
func captureExec(t *testing.T) *struct {
	called bool
	cmd    string
	args   []string
} {
	t.Helper()
	rec := &struct {
		called bool
		cmd    string
		args   []string
	}{}
	prev := execCommand
	execCommand = func(name string, args ...string) error {
		rec.called = true
		rec.cmd = name
		rec.args = args
		return nil
	}
	t.Cleanup(func() { execCommand = prev })
	return rec
}

func TestDefaultPlatformOpener(t *testing.T) {
	// Verify the current runtime gets the expected command. Useful to
	// confirm cross-platform table.
	cmd, args, err := defaultPlatformOpener("/some/path")
	if err != nil {
		t.Fatalf("err=%v on supported platform %s", err, runtime.GOOS)
	}
	switch runtime.GOOS {
	case "darwin":
		if cmd != "open" {
			t.Errorf("darwin cmd=%q, want open", cmd)
		}
	case "linux":
		if cmd != "xdg-open" {
			t.Errorf("linux cmd=%q, want xdg-open", cmd)
		}
	case "windows":
		if cmd != "cmd" {
			t.Errorf("windows cmd=%q, want cmd", cmd)
		}
	}
	if len(args) == 0 || args[len(args)-1] != "/some/path" {
		t.Errorf("path arg should be last in args=%+v", args)
	}
}

func TestOpenConfigDir_CallsExec(t *testing.T) {
	dir := withTempHistoryDir(t)
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := captureExec(t)

	s := NewStudio()
	if err := s.OpenConfigDir(); err != nil {
		t.Fatalf("OpenConfigDir failed: %v", err)
	}
	if !rec.called {
		t.Error("exec was not called")
	}
	// The configDir path should appear in args (last position for most
	// platforms, somewhere for Windows).
	found := false
	for _, a := range rec.args {
		if a == parent {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("configDir path %q not in args %+v", parent, rec.args)
	}
}

func TestOpenAutoBackupsDir_CallsExec(t *testing.T) {
	_ = withTempHistoryDir(t)
	// Auto-backups dir doesn't exist by default — create it so the stat check passes.
	if err := os.MkdirAll(autoBackupDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := captureExec(t)

	s := NewStudio()
	if err := s.OpenAutoBackupsDir(); err != nil {
		t.Fatalf("OpenAutoBackupsDir failed: %v", err)
	}
	if !rec.called {
		t.Error("exec was not called")
	}
	// The backups dir path should be in args.
	want := autoBackupDir()
	found := false
	for _, a := range rec.args {
		if a == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("backups dir %q not in args %+v", want, rec.args)
	}
}

func TestOpenPath_RejectsOutsideConfigDir(t *testing.T) {
	_ = withTempHistoryDir(t)
	rec := captureExec(t)

	s := NewStudio()
	// Try to open /etc — outside config dir → should be rejected.
	err := s.openPathInFileManager("/etc")
	if err == nil {
		t.Fatal("expected error for path outside config dir")
	}
	if !strings.Contains(err.Error(), "not inside config dir") {
		t.Errorf("error=%q, want 'not inside config dir'", err.Error())
	}
	if rec.called {
		t.Error("exec should NOT have been called for rejected path")
	}
}

func TestOpenPath_RejectsConfigDirAdjacent(t *testing.T) {
	// SECURITY: if configDir is /tmp/x and the user passes /tmp/xevil, the
	// HasPrefix check without separator would accept it. The separator
	// guard is the protection.
	_ = withTempHistoryDir(t)
	cfg := configDir()
	// Create an adjacent path that shares the prefix but isn't inside.
	adjacent := cfg + "-adjacent"
	if err := os.MkdirAll(adjacent, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := captureExec(t)

	s := NewStudio()
	err := s.openPathInFileManager(adjacent)
	if err == nil {
		t.Errorf("expected rejection for adjacent path %q (HasPrefix bypass)", adjacent)
	}
	if rec.called {
		t.Error("exec should NOT have been called for adjacent path")
	}
}

func TestOpenPath_RejectsNonExistent(t *testing.T) {
	_ = withTempHistoryDir(t)
	// Path inside configDir but doesn't exist.
	missing := filepath.Join(configDir(), "nope-does-not-exist")
	rec := captureExec(t)

	s := NewStudio()
	err := s.openPathInFileManager(missing)
	if err == nil {
		t.Error("expected error for non-existent path")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error=%q, want 'does not exist'", err.Error())
	}
	if rec.called {
		t.Error("exec should NOT have been called for missing path")
	}
}

func TestOpenPath_EmptyPath(t *testing.T) {
	s := NewStudio()
	err := s.openPathInFileManager("")
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestOpenPath_ExecError(t *testing.T) {
	dir := withTempHistoryDir(t)
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	// Swap exec to return an error.
	prev := execCommand
	execCommand = func(name string, args ...string) error {
		return errors.New("simulated exec failure")
	}
	t.Cleanup(func() { execCommand = prev })

	s := NewStudio()
	err := s.OpenConfigDir()
	if err == nil {
		t.Error("expected error when exec fails")
	}
	if !strings.Contains(err.Error(), "simulated") {
		t.Errorf("error=%q, want underlying error wrapped", err.Error())
	}
}

func TestOpenPath_LogsToEventLog(t *testing.T) {
	dir := withTempHistoryDir(t)
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	captureExec(t)

	s := NewStudio()
	if err := s.OpenConfigDir(); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range s.GetRecentLogs() {
		if l.Source == "filemanager" && strings.Contains(l.Message, "opened") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("open should log to event log; got %+v", s.GetRecentLogs())
	}
}

func TestConfigDirPath_Returns(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	got := s.ConfigDirPath()
	if got != configDir() {
		t.Errorf("ConfigDirPath=%q, want %q", got, configDir())
	}
}
