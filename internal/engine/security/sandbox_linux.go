//go:build linux

package security

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	linuxIsolationOnce   sync.Once
	linuxIsolationBinary string
	linuxIsolationStatus WorkspaceIsolationStatus
)

func DetectWorkspaceIsolation() WorkspaceIsolationStatus {
	linuxIsolationOnce.Do(func() {
		binary, err := exec.LookPath("bwrap")
		if err != nil {
			linuxIsolationStatus = WorkspaceIsolationStatus{
				Mode: "host", Detail: "bubblewrap is unavailable; commands require explicit host-execution approval.",
			}
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		probe := exec.CommandContext(ctx, binary,
			"--die-with-parent", "--new-session", "--ro-bind", "/", "/",
			"--proc", "/proc", "--dev", "/dev", "/bin/true",
		)
		if err := probe.Run(); err != nil {
			linuxIsolationStatus = WorkspaceIsolationStatus{
				Mode: "host", Detail: "bubblewrap could not create a user namespace; commands require explicit host-execution approval.",
			}
			return
		}
		linuxIsolationBinary = binary
		linuxIsolationStatus = WorkspaceIsolationStatus{
			Available: true,
			Enforced:  true,
			Mode:      "bubblewrap",
			Detail:    "bubblewrap workspace sandbox: the project/runtime are writable, HOME is hidden, and the network namespace is disabled unless separately approved.",
		}
	})
	return linuxIsolationStatus
}

func (sc *SandboxedCommand) applySandbox(_ string) error {
	status := DetectWorkspaceIsolation()
	if !status.Available {
		return fmt.Errorf("%s", status.Detail)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home for workspace sandbox: %w", err)
	}
	home = filepath.Clean(home)
	args := []string{
		"--die-with-parent", "--new-session",
		"--unshare-user", "--unshare-pid", "--unshare-uts", "--unshare-ipc",
		"--ro-bind", "/", "/",
		"--tmpfs", home,
	}
	if !sc.config.AllowNetwork {
		args = append(args, "--unshare-net")
	}
	if pathWithinLinux(sc.workDir, home) {
		relative, _ := filepath.Rel(home, sc.workDir)
		current := home
		if relative != "." {
			parts := strings.Split(relative, string(os.PathSeparator))
			for _, part := range parts {
				current = filepath.Join(current, part)
				args = append(args, "--dir", current)
			}
		}
	}
	args = append(args,
		"--bind", sc.workDir, sc.workDir,
		"--bind", sc.runtimeDir, sc.runtimeDir,
		"--proc", "/proc", "--dev", "/dev",
		"--chdir", sc.workDir,
		sc.cmd.Path,
	)
	args = append(args, sc.cmd.Args[1:]...)
	original := sc.cmd
	wrapped := exec.CommandContext(sc.ctx, linuxIsolationBinary, args...)
	wrapped.Dir = original.Dir
	wrapped.Env = original.Env
	sc.cmd = wrapped
	sc.mode = status.Mode
	return nil
}

func pathWithinLinux(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
