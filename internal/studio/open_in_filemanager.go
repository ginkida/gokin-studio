package studio

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// fileManagerOpener resolves the per-platform exec command for "show this
// path in the OS file manager". Returned as (cmd, args) so tests can swap
// the exec hook without touching real shell commands.
type fileManagerOpener func(path string) (cmd string, args []string, err error)

// platformOpener is the default opener picked from runtime.GOOS. Variable
// (not const) so tests can override per-test and verify the path argument
// reaches the resolver correctly.
var platformOpener fileManagerOpener = defaultPlatformOpener

func defaultPlatformOpener(path string) (string, []string, error) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{path}, nil
	case "linux":
		return "xdg-open", []string{path}, nil
	case "windows":
		// `start` is a cmd builtin, so go through cmd /c. `explorer` is
		// the more direct option but doesn't handle quoted paths with
		// spaces well; cmd start handles them via the empty title arg.
		return "cmd", []string{"/c", "start", "", path}, nil
	default:
		return "", nil, fmt.Errorf("unsupported OS for file manager: %s", runtime.GOOS)
	}
}

// execCommand is the inject point for tests so we can verify the exec
// hook fires WITHOUT actually opening a Finder/Explorer window during
// `go test ./internal/studio/`. Default just runs the command.
var execCommand = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Start()
}

// openPathInFileManager runs the per-platform file-manager command for
// the given path. Validates that the path is inside configDir() — defends
// against the frontend tricking the backend into opening arbitrary paths
// like /etc or the user's home dir.
func (s *Studio) openPathInFileManager(path string) error {
	if path == "" {
		return errors.New("path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("cannot resolve path: %w", err)
	}
	cfg, err := filepath.Abs(configDir())
	if err != nil {
		return fmt.Errorf("cannot resolve config dir: %w", err)
	}
	// Allow opening the config dir itself OR any path strictly inside it.
	// The Has-prefix check uses cfg + separator so cfg-adjacent paths
	// (e.g. <cfg>-evil) don't slip through.
	if abs != cfg && !strings.HasPrefix(abs, cfg+string(filepath.Separator)) {
		return fmt.Errorf("path %q is not inside config dir %q", abs, cfg)
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("path does not exist: %w", err)
	}
	cmd, args, err := platformOpener(abs)
	if err != nil {
		return err
	}
	if err := execCommand(cmd, args...); err != nil {
		return fmt.Errorf("failed to open: %w", err)
	}
	s.logf("info", "filemanager", "opened %q in OS file manager", abs)
	return nil
}

// OpenConfigDir reveals <configDir>/ in the OS file manager. Convenient
// for power users who want to inspect the config / history / drafts /
// backups directly instead of going through the Settings UI.
func (s *Studio) OpenConfigDir() error {
	return s.openPathInFileManager(configDir())
}

// OpenAutoBackupsDir reveals <configDir>/backups/ in the OS file manager.
// More targeted than OpenConfigDir for users specifically managing backups.
func (s *Studio) OpenAutoBackupsDir() error {
	return s.openPathInFileManager(autoBackupDir())
}

// ConfigDirPath returns the absolute config directory path as a string.
// Useful when the frontend wants to display the path AND offer "open in
// file manager" — display the path so the user knows where their data is,
// the button just provides a one-click way to get there.
func (s *Studio) ConfigDirPath() string {
	return configDir()
}
