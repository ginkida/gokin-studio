//go:build darwin

package security

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	darwinIsolationOnce   sync.Once
	darwinIsolationStatus WorkspaceIsolationStatus
)

func DetectWorkspaceIsolation() WorkspaceIsolationStatus {
	darwinIsolationOnce.Do(func() {
		const binary = "/usr/bin/sandbox-exec"
		if info, err := os.Stat(binary); err != nil || info.IsDir() {
			darwinIsolationStatus = WorkspaceIsolationStatus{
				Mode: "host", Detail: "macOS sandbox-exec is unavailable; commands require explicit host-execution approval.",
			}
			return
		}
		ctx, cancel := contextWithShortTimeout()
		defer cancel()
		if err := exec.CommandContext(ctx, binary, "-p", "(version 1) (allow default)", "/usr/bin/true").Run(); err != nil {
			darwinIsolationStatus = WorkspaceIsolationStatus{
				Mode: "host", Detail: "macOS denied sandbox activation; commands require explicit host-execution approval.",
			}
			return
		}
		darwinIsolationStatus = WorkspaceIsolationStatus{
			Available: true,
			Enforced:  true,
			Mode:      "macos-sandbox",
			Detail:    "macOS workspace sandbox: project and isolated runtime are writable; HOME/external volumes are hidden and network access is blocked unless separately approved.",
		}
	})
	return darwinIsolationStatus
}

func contextWithShortTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Second)
}

func (sc *SandboxedCommand) applySandbox(_ string) error {
	status := DetectWorkspaceIsolation()
	if !status.Available {
		return fmt.Errorf("%s", status.Detail)
	}
	profile, err := darwinWorkspaceProfile(sc.workDir, sc.runtimeDir, sc.config.AllowNetwork)
	if err != nil {
		return err
	}
	original := sc.cmd
	args := []string{"-p", profile, original.Path}
	args = append(args, original.Args[1:]...)
	wrapped := exec.CommandContext(sc.ctx, "/usr/bin/sandbox-exec", args...)
	wrapped.Dir = original.Dir
	wrapped.Env = original.Env
	sc.cmd = wrapped
	sc.mode = status.Mode
	return nil
}

func darwinWorkspaceProfile(workDir, runtimeDir string, allowNetwork bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home for workspace sandbox: %w", err)
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return "", fmt.Errorf("resolve user home for workspace sandbox: %w", err)
	}
	tempRoot := os.TempDir()
	if resolved, resolveErr := filepath.EvalSymlinks(tempRoot); resolveErr == nil {
		tempRoot = resolved
	}

	readExceptions := []string{workDir, runtimeDir, tempRoot, runtime.GOROOT()}
	for _, value := range []string{os.Getenv("VIRTUAL_ENV"), os.Getenv("NODE_PATH"), os.Getenv("NPM_CONFIG_PREFIX")} {
		if value = strings.TrimSpace(value); value != "" {
			readExceptions = append(readExceptions, value)
		}
	}
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		entry = strings.TrimSpace(entry)
		if entry != "" && pathWithin(entry, home) {
			readExceptions = append(readExceptions, toolchainRoot(entry))
		}
	}

	var profile strings.Builder
	profile.WriteString("(version 1)\n")
	profile.WriteString("(import \"system.sb\")\n")
	profile.WriteString("(allow default)\n")
	profile.WriteString("(deny appleevent-send)\n")
	profile.WriteString("(deny mach-lookup (global-name \"com.apple.securityd\") (global-name \"com.apple.securityd.xpc\") (global-name \"com.apple.pboard\") (global-name-regex #\"^com\\\\.apple\\\\.(securityd|pboard)(\\\\.|$)\"))\n")
	if !allowNetwork {
		// Deny every IP socket, including loopback. Local test servers therefore
		// need the same exact network grant as Internet/LAN access; Unix-domain
		// IPC remains available because these rules match only IP endpoints.
		profile.WriteString("(deny network-outbound (remote ip))\n")
		profile.WriteString("(deny network-inbound (local ip))\n")
	}
	profile.WriteString("(deny file-write*)\n")
	fmt.Fprintf(&profile, "(deny file-read* (subpath %s) (subpath %s))\n",
		sandboxSchemeString(home), sandboxSchemeString("/Volumes"))
	for _, path := range uniqueCleanPaths(readExceptions) {
		fmt.Fprintf(&profile, "(allow file-read* (subpath %s))\n", sandboxSchemeString(path))
	}
	for _, path := range uniqueCleanPaths([]string{workDir, runtimeDir, tempRoot}) {
		fmt.Fprintf(&profile, "(allow file-write* (subpath %s))\n", sandboxSchemeString(path))
	}
	return profile.String(), nil
}

func toolchainRoot(path string) string {
	clean := filepath.Clean(path)
	marker := string(os.PathSeparator) + ".nvm" + string(os.PathSeparator) + "versions" + string(os.PathSeparator) + "node" + string(os.PathSeparator)
	if index := strings.Index(clean, marker); index >= 0 {
		rest := clean[index+len(marker):]
		version := strings.SplitN(rest, string(os.PathSeparator), 2)[0]
		return clean[:index+len(marker)] + version
	}
	return clean
}

func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func uniqueCleanPaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		} else if absolute, absErr := filepath.Abs(path); absErr == nil {
			path = absolute
		}
		path = filepath.Clean(path)
		if !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	return out
}

func sandboxSchemeString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`)
	return `"` + replacer.Replace(value) + `"`
}
