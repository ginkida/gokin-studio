package studio

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfigYAML writes raw YAML to the config path for the current test's
// config dir (which must already be overridden via GOKIN_CONFIG_DIR).
func writeConfigYAML(t *testing.T, yaml string) {
	t.Helper()
	dir := configDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("WriteFile config.yaml: %v", err)
	}
}

// withTempConfigDir overrides GOKIN_CONFIG_DIR for the duration of the test.
func withTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := os.Getenv("GOKIN_CONFIG_DIR")
	_ = os.Setenv("GOKIN_CONFIG_DIR", dir)
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", prev) })
	return dir
}

// TestLoadConfig_MissingFile verifies that LoadConfig returns defaults (not an
// error) when no config.yaml exists yet — the first-run case.
func TestLoadConfig_MissingFile(t *testing.T) {
	withTempConfigDir(t)
	cfg := LoadConfig()
	if cfg == nil {
		t.Fatal("expected non-nil config for missing file, got nil")
	}
	if cfg.Settings.DefaultProvider == "" {
		t.Error("expected non-empty DefaultProvider from defaults")
	}
	if cfg.Settings.Theme == "" {
		t.Error("expected non-empty Theme from defaults")
	}
}

// TestLoadConfig_InvalidYAML verifies that a corrupt config.yaml returns
// defaults (not a panic or error) so the app can still start.
func TestLoadConfig_InvalidYAML(t *testing.T) {
	withTempConfigDir(t)
	writeConfigYAML(t, "{{{{not valid yaml at all}}}}")
	cfg := LoadConfig()
	if cfg == nil {
		t.Fatal("expected non-nil defaults for invalid YAML, got nil")
	}
	// Should fall back to defaults, not retain garbage values.
	defaults := defaultConfig()
	if cfg.Settings.DefaultProvider != defaults.Settings.DefaultProvider {
		t.Errorf("invalid YAML: DefaultProvider = %q, want %q",
			cfg.Settings.DefaultProvider, defaults.Settings.DefaultProvider)
	}
}

// TestLoadConfig_ValidRoundTrip verifies that a written config is read back
// correctly, including API keys, project list, and custom settings.
func TestLoadConfig_ValidRoundTrip(t *testing.T) {
	withTempConfigDir(t)
	writeConfigYAML(t, `
settings:
  theme: light
  default_provider: kimi
  default_model: kimi-for-coding
  glm_key: test-glm-key
  kimi_key: test-kimi-key
projects:
  - id: proj-1
    name: MyProject
    directory: /tmp/proj
    provider: kimi
    model: kimi-for-coding
`)
	cfg := LoadConfig()
	if cfg.Settings.Theme != "light" {
		t.Errorf("Theme = %q, want 'light'", cfg.Settings.Theme)
	}
	if cfg.Settings.DefaultProvider != "kimi" {
		t.Errorf("DefaultProvider = %q, want 'kimi'", cfg.Settings.DefaultProvider)
	}
	if cfg.Settings.GLMKey != "test-glm-key" {
		t.Errorf("GLMKey = %q, want 'test-glm-key'", cfg.Settings.GLMKey)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Name != "MyProject" {
		t.Errorf("Projects = %v, want [{Name:MyProject}]", cfg.Projects)
	}
}

// TestLoadConfig_EmptyFieldsFallToDefaults verifies that a config file with
// fields explicitly set to empty string has those fields re-filled from defaults.
// This happens when an older config or manually-edited file sets fields to "".
// yaml.Unmarshal will set a field to "" when the YAML contains an explicit
// empty value, overriding the defaultConfig() pre-fill. LoadConfig then re-fills
// them from defaults.
func TestLoadConfig_EmptyFieldsFallToDefaults(t *testing.T) {
	withTempConfigDir(t)
	// Explicitly set all four fieldsToDefault to empty string so they override
	// the defaultConfig() pre-initialization and hit the "" checks in LoadConfig.
	writeConfigYAML(t, `
settings:
  default_provider: ""
  default_model: ""
  theme: ""
  ollama_url: ""
  glm_key: sk-abc
`)
	cfg := LoadConfig()
	defaults := defaultConfig()
	if cfg.Settings.DefaultProvider != defaults.Settings.DefaultProvider {
		t.Errorf("DefaultProvider = %q, want %q", cfg.Settings.DefaultProvider, defaults.Settings.DefaultProvider)
	}
	if cfg.Settings.DefaultModel != defaults.Settings.DefaultModel {
		t.Errorf("DefaultModel = %q, want %q", cfg.Settings.DefaultModel, defaults.Settings.DefaultModel)
	}
	if cfg.Settings.Theme != defaults.Settings.Theme {
		t.Errorf("Theme = %q, want %q", cfg.Settings.Theme, defaults.Settings.Theme)
	}
	if cfg.Settings.OllamaURL != defaults.Settings.OllamaURL {
		t.Errorf("OllamaURL = %q, want %q", cfg.Settings.OllamaURL, defaults.Settings.OllamaURL)
	}
	// The key must be preserved even though other fields were defaulted.
	if cfg.Settings.GLMKey != "sk-abc" {
		t.Errorf("GLMKey = %q, want 'sk-abc'", cfg.Settings.GLMKey)
	}
}

// TestLoadConfig_DeprecatedModelMigration verifies that old GLM model names
// (glm-4-plus, glm-4-air, glm-4-flash, glm-4-long) are transparently
// upgraded to the current default model (glm-5.2) on load.
func TestLoadConfig_DeprecatedModelMigration(t *testing.T) {
	withTempConfigDir(t)
	for _, oldModel := range []string{"glm-4-plus", "glm-4-air", "glm-4-flash", "glm-4-long"} {
		writeConfigYAML(t, `
settings:
  default_provider: glm
  default_model: `+oldModel+`
`)
		cfg := LoadConfig()
		if cfg.Settings.DefaultModel != "glm-5.2" {
			t.Errorf("deprecated model %q not migrated: DefaultModel = %q, want 'glm-5.2'",
				oldModel, cfg.Settings.DefaultModel)
		}
	}
}

// TestLoadConfig_RemovedProviderMigration verifies that providers that are
// no longer supported (anthropic, gemini) are migrated to glm/glm-5.2 so
// the app starts cleanly on an old config. iter 940+ note: DeepSeek used
// to be on the removed list but came BACK with V4 — see TestLoadConfig_
// DeepSeekRevived below.
func TestLoadConfig_RemovedProviderMigration(t *testing.T) {
	withTempConfigDir(t)
	for _, removed := range []string{"anthropic", "gemini"} {
		writeConfigYAML(t, `
settings:
  default_provider: `+removed+`
  default_model: some-model
`)
		cfg := LoadConfig()
		if cfg.Settings.DefaultProvider != "glm" {
			t.Errorf("removed provider %q: DefaultProvider = %q, want 'glm'",
				removed, cfg.Settings.DefaultProvider)
		}
		if cfg.Settings.DefaultModel != "glm-5.2" {
			t.Errorf("removed provider %q: DefaultModel = %q, want 'glm-5.2'",
				removed, cfg.Settings.DefaultModel)
		}
	}
}

// TestLoadConfig_DeepSeekRevived verifies that iter 940+ brought DeepSeek
// back as a supported provider AND auto-migrates the deprecated legacy
// model names (deepseek-chat / deepseek-reasoner) to V4. Closes the loop
// for users on old configs from when DeepSeek was removed (then re-added).
func TestLoadConfig_DeepSeekRevived(t *testing.T) {
	withTempConfigDir(t)

	// Case 1: DefaultProvider=deepseek should SURVIVE (not migrated to glm).
	writeConfigYAML(t, `
settings:
  default_provider: deepseek
  default_model: deepseek-v4-pro
`)
	cfg := LoadConfig()
	if cfg.Settings.DefaultProvider != "deepseek" {
		t.Errorf("deepseek provider should survive (re-enabled in iter 940+); got %q", cfg.Settings.DefaultProvider)
	}
	if cfg.Settings.DefaultModel != "deepseek-v4-pro" {
		t.Errorf("deepseek-v4-pro should be preserved; got %q", cfg.Settings.DefaultModel)
	}

	// Case 2: legacy deepseek-chat → deepseek-v4-flash (non-thinking).
	withTempConfigDir(t)
	writeConfigYAML(t, `
settings:
  default_provider: deepseek
  default_model: deepseek-chat
`)
	cfg = LoadConfig()
	if cfg.Settings.DefaultModel != "deepseek-v4-flash" {
		t.Errorf("legacy deepseek-chat should migrate to deepseek-v4-flash; got %q", cfg.Settings.DefaultModel)
	}

	// Case 3: legacy deepseek-reasoner → deepseek-v4-pro (thinking).
	withTempConfigDir(t)
	writeConfigYAML(t, `
settings:
  default_provider: deepseek
  default_model: deepseek-reasoner
`)
	cfg = LoadConfig()
	if cfg.Settings.DefaultModel != "deepseek-v4-pro" {
		t.Errorf("legacy deepseek-reasoner should migrate to deepseek-v4-pro; got %q", cfg.Settings.DefaultModel)
	}
}

// TestLoadConfig_ProjectProviderMigration verifies three project-level
// migration cases:
//   - empty provider → glm/glm-5.2
//   - removed provider (e.g. anthropic) → glm/glm-5.2
//   - empty model with valid provider → filled from settings default model
//   - deprecated model name → upgraded to glm-5.2
func TestLoadConfig_ProjectProviderMigration(t *testing.T) {
	withTempConfigDir(t)
	writeConfigYAML(t, `
settings:
  default_provider: glm
  default_model: glm-5.2
projects:
  - id: p1
    name: EmptyProvider
    directory: /tmp/p1
    provider: ""
    model: ""
  - id: p2
    name: RemovedProvider
    directory: /tmp/p2
    provider: anthropic
    model: claude-3
  - id: p3
    name: EmptyModel
    directory: /tmp/p3
    provider: glm
    model: ""
  - id: p4
    name: DeprecatedModel
    directory: /tmp/p4
    provider: glm
    model: glm-4-flash
`)
	cfg := LoadConfig()

	find := func(id string) *ProjectConfig {
		for i := range cfg.Projects {
			if cfg.Projects[i].ID == id {
				return &cfg.Projects[i]
			}
		}
		return nil
	}

	p1 := find("p1")
	if p1 == nil {
		t.Fatal("project p1 not found")
	}
	if p1.Provider != "glm" || p1.Model != "glm-5.2" {
		t.Errorf("p1 (empty provider): got (%q, %q), want (glm, glm-5.2)", p1.Provider, p1.Model)
	}

	p2 := find("p2")
	if p2 == nil {
		t.Fatal("project p2 not found")
	}
	if p2.Provider != "glm" || p2.Model != "glm-5.2" {
		t.Errorf("p2 (removed provider anthropic): got (%q, %q), want (glm, glm-5.2)", p2.Provider, p2.Model)
	}

	p3 := find("p3")
	if p3 == nil {
		t.Fatal("project p3 not found")
	}
	if p3.Model != "glm-5.2" {
		t.Errorf("p3 (empty model): got %q, want 'glm-5.2'", p3.Model)
	}

	p4 := find("p4")
	if p4 == nil {
		t.Fatal("project p4 not found")
	}
	if p4.Model != "glm-5.2" {
		t.Errorf("p4 (deprecated model glm-4-flash): got %q, want 'glm-5.2'", p4.Model)
	}
}

// TestSave_MkdirAllError verifies that Save returns a non-nil error when the
// config directory path is occupied by a regular file (so os.MkdirAll fails).
func TestSave_MkdirAllError(t *testing.T) {
	// Write a regular file at the location that GOKIN_CONFIG_DIR will point to,
	// then point the env var at it so configDir() returns a path that is a file.
	f, err := os.CreateTemp("", "gokin-config-as-file-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	f.Close()
	defer os.Remove(f.Name())

	prev := os.Getenv("GOKIN_CONFIG_DIR")
	_ = os.Setenv("GOKIN_CONFIG_DIR", f.Name()) // file, not directory
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", prev) })

	cfg := &StudioConfig{}
	if err := cfg.Save(); err == nil {
		t.Error("expected error from Save when config dir is a regular file, got nil")
	}
}

// TestSave_RoundTrip verifies that StudioConfig.Save writes a valid YAML file
// that LoadConfig can read back with identical content. Exercises the success
// path of Save (MkdirAll + yaml.Marshal + atomicWriteFile).
func TestSave_RoundTrip(t *testing.T) {
	withTempConfigDir(t)
	cfg := &StudioConfig{
		Settings: Settings{
			Theme:           "light",
			DefaultProvider: "minimax",
			DefaultModel:    "minimax-text",
			GLMKey:          "sk-glm",
			KimiKey:         "sk-kimi",
		},
		Projects: []ProjectConfig{
			{ID: "p-rt", Name: "RoundTrip", Directory: "/tmp/rt", Provider: "minimax", Model: "minimax-text"},
		},
	}

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded := LoadConfig()
	if loaded.Settings.Theme != "light" {
		t.Errorf("Theme = %q, want 'light'", loaded.Settings.Theme)
	}
	if loaded.Settings.DefaultProvider != "minimax" {
		t.Errorf("DefaultProvider = %q, want 'minimax'", loaded.Settings.DefaultProvider)
	}
	if loaded.Settings.GLMKey != "sk-glm" {
		t.Errorf("GLMKey = %q, want 'sk-glm'", loaded.Settings.GLMKey)
	}
	if len(loaded.Projects) != 1 || loaded.Projects[0].ID != "p-rt" {
		t.Errorf("Projects = %v, want [{ID:p-rt}]", loaded.Projects)
	}
}
