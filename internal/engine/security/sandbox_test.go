package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultSandboxConfigBlocksNetwork(t *testing.T) {
	if DefaultSandboxConfig().AllowNetwork {
		t.Fatal("workspace sandbox must block network access by default")
	}
}

func TestWorkspaceSafeEnvironmentHidesRealUserState(t *testing.T) {
	previous := WorkspaceEnvironmentSnapshot()
	t.Cleanup(func() { _ = SetWorkspaceEnvironment(previous) })
	if err := SetWorkspaceEnvironment(nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GLM_API_KEY", "must-not-leak")
	t.Setenv("KIMI_API_KEY", "must-not-leak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-leak")
	workspace := t.TempDir()
	env, err := WorkspaceSafeEnvironment(workspace)
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string, len(env))
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	realHome, _ := os.UserHomeDir()
	if values["HOME"] == "" || filepath.Clean(values["HOME"]) == filepath.Clean(realHome) {
		t.Fatalf("isolated HOME = %q, real HOME = %q", values["HOME"], realHome)
	}
	for _, key := range []string{
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
		"TMPDIR", "GOPATH", "GOCACHE", "NPM_CONFIG_CACHE", "PIP_CACHE_DIR",
	} {
		if values[key] == "" {
			t.Errorf("%s is not isolated: %#v", key, values)
		}
		if realHome != "" && pathIsWithin(values[key], realHome) {
			t.Errorf("%s points into real HOME: %q", key, values[key])
		}
	}
	for _, secret := range []string{"GLM_API_KEY", "KIMI_API_KEY", "AWS_SECRET_ACCESS_KEY"} {
		if _, exists := values[secret]; exists {
			t.Errorf("credential variable %s leaked into workspace environment", secret)
		}
	}
	if !strings.Contains(values["PATH"], filepath.Join(workspace, "node_modules", ".bin")) {
		t.Fatalf("PATH does not include project-local tools: %q", values["PATH"])
	}
}

func TestWorkspaceEnvironmentValidationAndMerge(t *testing.T) {
	previous := WorkspaceEnvironmentSnapshot()
	t.Cleanup(func() { _ = SetWorkspaceEnvironment(previous) })
	if err := SetWorkspaceEnvironment(map[string]string{"APP_TOKEN": "secret", "EMPTY_VALUE": ""}); err != nil {
		t.Fatal(err)
	}
	snapshot := WorkspaceEnvironmentSnapshot()
	snapshot["APP_TOKEN"] = "mutated"
	if WorkspaceEnvironmentSnapshot()["APP_TOKEN"] != "secret" {
		t.Fatal("environment snapshot mutated the active configuration")
	}
	merged := MergeWorkspaceEnvironment([]string{"APP_TOKEN=old", "UNCHANGED=yes"})
	values := make(map[string]string, len(merged))
	for _, item := range merged {
		name, value, ok := strings.Cut(item, "=")
		if ok {
			values[name] = value
		}
	}
	if values["APP_TOKEN"] != "secret" || values["EMPTY_VALUE"] != "" || values["UNCHANGED"] != "yes" {
		t.Fatalf("unexpected merged environment: %#v", values)
	}

	for _, values := range []map[string]string{
		{"PATH": "unsafe"},
		{"DYLD_INSERT_LIBRARIES": "/tmp/inject.dylib"},
		{"BASH_FUNC_bad": "() { :; }"},
		{"BAD-NAME": "value"},
		{"Token": "one", "TOKEN": "two"},
	} {
		if err := ValidateWorkspaceEnvironment(values); err == nil {
			t.Fatalf("expected environment to be rejected: %#v", values)
		}
	}
}

func TestWorkspaceSafeEnvironmentIncludesConfiguredValues(t *testing.T) {
	previous := WorkspaceEnvironmentSnapshot()
	t.Cleanup(func() { _ = SetWorkspaceEnvironment(previous) })
	if err := SetWorkspaceEnvironment(map[string]string{"LOCAL_ENV_TEST": "available", "LANG": "C.UTF-8"}); err != nil {
		t.Fatal(err)
	}
	env, err := WorkspaceSafeEnvironment(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string, len(env))
	for _, item := range env {
		name, value, ok := strings.Cut(item, "=")
		if ok {
			values[name] = value
		}
	}
	if values["LOCAL_ENV_TEST"] != "available" || values["LANG"] != "C.UTF-8" {
		t.Fatalf("configured values missing from safe environment: %#v", values)
	}
}

func TestSandboxPipeCaptureIsBoundedAndDrained(t *testing.T) {
	input := strings.Repeat("x", sandboxOutputMaxBytes+4096)
	data, err := readWithTimeout(strings.NewReader(input), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > sandboxOutputMaxBytes+128 {
		t.Fatalf("captured %d bytes, expected bounded output", len(data))
	}
	if !strings.Contains(string(data), "sandbox output truncated") {
		t.Fatal("bounded capture omitted truncation marker")
	}
}

func pathIsWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
