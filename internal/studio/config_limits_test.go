package studio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadConfigQuarantinesInvalidBytes(t *testing.T) {
	dir := withTempConfigDir(t)
	original := []byte("{{broken yaml")
	if err := os.WriteFile(configPath(), original, 0600); err != nil {
		t.Fatal(err)
	}
	_ = LoadConfig()
	if _, err := os.Stat(configPath()); !os.IsNotExist(err) {
		t.Fatalf("invalid active config was not quarantined: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "config.yaml.corrupt-*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("quarantine files = %v, err=%v", matches, err)
	}
	got, err := os.ReadFile(matches[0])
	if err != nil || string(got) != string(original) {
		t.Fatalf("quarantine did not preserve bytes: %q, %v", got, err)
	}
}

func TestLoadConfigRejectsOversizedAndSymlinkedFiles(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		withTempConfigDir(t)
		f, err := os.OpenFile(configPath(), os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(StudioConfigMaxBytes + 1); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		cfg := LoadConfig()
		if len(cfg.Projects) != 0 {
			t.Fatal("oversized config was loaded")
		}
		matches, _ := filepath.Glob(configPath() + ".corrupt-*")
		if len(matches) != 1 {
			t.Fatalf("oversized config not quarantined: %v", matches)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dir := withTempConfigDir(t)
		outside := filepath.Join(t.TempDir(), "outside.yaml")
		original := []byte("settings:\n  theme: light\n  glm_key: do-not-read\n")
		if err := os.WriteFile(outside, original, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, "config.yaml")); err != nil {
			t.Fatal(err)
		}
		cfg := LoadConfig()
		if cfg.Settings.Theme != "dark" || cfg.Settings.GLMKey != "" {
			t.Fatalf("symlinked config was loaded: %+v", cfg.Settings)
		}
		got, err := os.ReadFile(outside)
		if err != nil || string(got) != string(original) {
			t.Fatalf("symlink target changed: %q, %v", got, err)
		}
	})
}

func TestStudioConfigSaveEnforcesBounds(t *testing.T) {
	withTempConfigDir(t)
	tooMany := &StudioConfig{Projects: make([]ProjectConfig, StudioConfigMaxProjects+1)}
	if err := tooMany.Save(); err == nil || !strings.Contains(err.Error(), "too many projects") {
		t.Fatalf("expected project limit error, got %v", err)
	}
	tooLarge := &StudioConfig{Projects: []ProjectConfig{{
		ID: "p", Name: "P", Directory: "/tmp", SystemPrompt: strings.Repeat("x", StudioConfigMaxBytes),
	}}}
	if err := tooLarge.Save(); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected byte limit error, got %v", err)
	}
	if _, err := os.Stat(configPath()); !os.IsNotExist(err) {
		t.Fatalf("rejected config was written: %v", err)
	}
}

func TestLoadConfigRejectsExcessiveProjectCount(t *testing.T) {
	withTempConfigDir(t)
	cfg := StudioConfig{Projects: make([]ProjectConfig, StudioConfigMaxProjects+1)}
	for i := range cfg.Projects {
		cfg.Projects[i] = ProjectConfig{ID: "p", Name: "P", Directory: "/tmp"}
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > StudioConfigMaxBytes {
		t.Fatalf("test fixture unexpectedly exceeds byte limit: %d", len(data))
	}
	if err := os.WriteFile(configPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded := LoadConfig()
	if len(loaded.Projects) != 0 {
		t.Fatalf("loaded %d excessive projects", len(loaded.Projects))
	}
	matches, _ := filepath.Glob(configPath() + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("excessive-project config not quarantined: %v", matches)
	}
}

func TestLoadConfigMigratesAcceptEditsSpelling(t *testing.T) {
	withTempConfigDir(t)
	dir := t.TempDir()
	data := []byte(fmt.Sprintf("projects:\n  - id: p\n    name: P\n    directory: %q\n    provider: glm\n    model: glm-5.2\n    permission_mode: acceptEdits\nsettings:\n  theme: dark\n  default_provider: glm\n  default_model: glm-5.2\n", dir))
	if err := os.WriteFile(configPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := LoadConfig()
	if len(loaded.Projects) != 1 || loaded.Projects[0].PermissionMode != "accept_edits" {
		t.Fatalf("Accept edits migration = %#v", loaded.Projects)
	}
}

func TestLoadConfigRepairsSemanticProjectInvariants(t *testing.T) {
	withTempConfigDir(t)
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeConfigYAML(t, fmt.Sprintf(`
settings:
  theme: ultraviolet
  default_thinking_mode: impossible
projects:
  - id: duplicate
    name: ""
    directory: %q
    thinking_mode: impossible
    thinking_budget: -1
    permission_mode: bypass
    budget_usd: 0
    enforce_budget: true
  - id: duplicate
    name: Second
    directory: %q
  - id: empty-dir
    name: Unsafe
    directory: ""
`, dirA, dirB))
	cfg := LoadConfig()
	if len(cfg.Projects) != 2 {
		t.Fatalf("normalized projects = %d, want 2", len(cfg.Projects))
	}
	if cfg.Projects[0].ID == "" || cfg.Projects[1].ID == "" || cfg.Projects[0].ID == cfg.Projects[1].ID {
		t.Fatalf("project IDs not repaired: %q, %q", cfg.Projects[0].ID, cfg.Projects[1].ID)
	}
	if cfg.Projects[0].Name == "" || cfg.Projects[0].ThinkingMode != "" ||
		cfg.Projects[0].ThinkingBudget != 0 || cfg.Projects[0].PermissionMode != "" || cfg.Projects[0].EnforceBudget {
		t.Fatalf("project invariants not repaired: %+v", cfg.Projects[0])
	}
	if cfg.Settings.Theme != "dark" || cfg.Settings.DefaultThinkingMode != "" {
		t.Fatalf("settings invariants not repaired: %+v", cfg.Settings)
	}
}
