//go:build darwin

package security

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDarwinWorkspaceProfileDeclaresWriteAndHomeBoundaries(t *testing.T) {
	workspace := t.TempDir()
	runtimeDir, err := workspaceSandboxRuntimeDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := darwinWorkspaceProfile(workspace, runtimeDir, false)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	resolved := uniqueCleanPaths([]string{workspace, runtimeDir})
	for _, want := range []string{
		"(deny file-write*)",
		"(deny appleevent-send)",
		"com.apple.securityd",
		"com.apple.pboard",
		"(deny network-outbound",
		"(deny network-inbound",
		"(deny file-read*",
		sandboxSchemeString(home),
		sandboxSchemeString(resolved[0]),
		sandboxSchemeString(resolved[1]),
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing %q:\n%s", want, profile)
		}
	}

	networked, err := darwinWorkspaceProfile(workspace, runtimeDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(networked, "(deny network-outbound") || strings.Contains(networked, "(deny network-inbound") {
		t.Fatalf("approved network profile still denies network:\n%s", networked)
	}
}

func TestDarwinWorkspaceSandboxBlocksHomeSymlinkAndExternalWrite(t *testing.T) {
	status := DetectWorkspaceIsolation()
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
	external := filepath.Join("/private/tmp", fmt.Sprintf("gokin-sandbox-escape-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(external) })
	command := strings.Join([]string{
		"printf allowed > allowed.txt",
		"if /bin/ls " + shellQuoteForSandboxTest(home) + " >/dev/null 2>&1; then exit 41; fi",
		"if /bin/ls home-link/ >/dev/null 2>&1; then exit 42; fi",
		"if /usr/bin/touch " + shellQuoteForSandboxTest(external) + " >/dev/null 2>&1; then exit 43; fi",
		"if /usr/bin/security list-keychains >/dev/null 2>&1; then exit 44; fi",
	}, "\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	isolated, err := NewSandboxedCommand(ctx, workspace, command, DefaultSandboxConfig())
	if err != nil {
		t.Fatal(err)
	}
	result := isolated.Run(10 * time.Second)
	if result.Error != nil || result.ExitCode != 0 {
		t.Fatalf("sandbox result = %#v, stderr=%s", result, result.Stderr)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "allowed.txt")); err != nil || string(data) != "allowed" {
		t.Fatalf("workspace write = %q, %v", data, err)
	}
	if _, err := os.Stat(external); !os.IsNotExist(err) {
		t.Fatalf("sandbox wrote outside workspace: %v", err)
	}
}

func TestDarwinWorkspaceSandboxNetworkRequiresExplicitGrant(t *testing.T) {
	status := DetectWorkspaceIsolation()
	if !status.Available {
		t.Skip(status.Detail)
	}
	if _, err := os.Stat("/usr/bin/nc"); err != nil {
		t.Skip("nc is unavailable")
	}

	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	hostIP := firstNonLoopbackIPv4(t)
	workspace := t.TempDir()
	command := strings.Join([]string{
		"if /usr/bin/nc -z -w 1 127.0.0.1 " + strconv.Itoa(port) + "; then exit 51; fi",
		"if /usr/bin/nc -z -w 1 " + hostIP + " " + strconv.Itoa(port) + "; then exit 52; fi",
	}, "\n")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	isolated, err := NewSandboxedCommand(ctx, workspace, command, DefaultSandboxConfig())
	if err != nil {
		t.Fatal(err)
	}
	result := isolated.Run(10 * time.Second)
	if result.Error != nil || result.ExitCode != 0 {
		t.Fatalf("network-restricted sandbox result = %#v, stderr=%s", result, result.Stderr)
	}

	allowed := DefaultSandboxConfig()
	allowed.AllowNetwork = true
	isolated, err = NewSandboxedCommand(
		ctx,
		workspace,
		"/usr/bin/nc -z -w 1 127.0.0.1 "+strconv.Itoa(port)+
			" && /usr/bin/nc -z -w 1 "+hostIP+" "+strconv.Itoa(port),
		allowed,
	)
	if err != nil {
		t.Fatal(err)
	}
	result = isolated.Run(10 * time.Second)
	if result.Error != nil || result.ExitCode != 0 {
		t.Fatalf("approved network sandbox result = %#v, stderr=%s", result, result.Stderr)
	}
}

func firstNonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.To4() != nil && !ip.IsLoopback() {
			return ip.String()
		}
	}
	t.Skip("no non-loopback IPv4 address available")
	return ""
}

func shellQuoteForSandboxTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
