package studio

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// StudioConfig is the top-level configuration saved to disk.
type StudioConfig struct {
	Projects []ProjectConfig `yaml:"projects" json:"projects"`
	Settings Settings        `yaml:"settings" json:"settings"`
}

// ProjectConfig is the persisted state of a single project.
type ProjectConfig struct {
	ID           string  `yaml:"id" json:"id"`
	Name         string  `yaml:"name" json:"name"`
	Directory    string  `yaml:"directory" json:"directory"`
	Provider     string  `yaml:"provider" json:"provider"`
	Model        string  `yaml:"model" json:"model"`
	SystemPrompt string  `yaml:"system_prompt,omitempty" json:"systemPrompt,omitempty"`
	Temperature  float32 `yaml:"temperature,omitempty" json:"temperature,omitempty"`
	MaxTokens    int     `yaml:"max_tokens,omitempty" json:"maxTokens,omitempty"`
	// Extended thinking control. "" = auto (provider default), "enabled" = on,
	// "disabled" = off. ThinkingBudget is the max reasoning tokens (0 = use
	// provider default of 4096 when thinking is enabled).
	ThinkingMode   string `yaml:"thinking_mode,omitempty" json:"thinkingMode,omitempty"`
	ThinkingBudget int32  `yaml:"thinking_budget,omitempty" json:"thinkingBudget,omitempty"`
	// PermissionMode controls how cautious the agent is about changes.
	// "" / "auto" = proceed without asking; "ask" = confirm via the ask_user
	// tool before file/git/destructive changes (soft enforcement: a directive
	// appended to the system prompt, since the agent loop has no hard gate).
	PermissionMode string `yaml:"permission_mode,omitempty" json:"permissionMode,omitempty"`
	// BudgetUSD is the user-set monthly spend cap. The UI uses it to render
	// progress vs. accumulated session usage and warn at 80%/100%. 0 = no
	// budget set (no warnings). Capped at $100,000 to defend against typos.
	BudgetUSD float64 `yaml:"budget_usd,omitempty" json:"budgetUSD,omitempty"`
	// EnforceBudget, when true, hard-stops new agent turns once cumulative
	// cost reaches BudgetUSD. Without this flag, exceeding the budget only
	// triggers warning toasts (iter 610+) — a user who walked away during a
	// long agent run could still burn far past the cap. With it, SendMessage
	// returns a chat:error and the run is aborted. Opt-in (default false) so
	// existing users aren't surprised by sudden blocks. Requires BudgetUSD > 0.
	EnforceBudget bool `yaml:"enforce_budget,omitempty" json:"enforceBudget,omitempty"`
	// Pinned, when true, anchors this project to the top of the sidebar
	// regardless of LastUsedAt. Useful when a primary project keeps drifting
	// down as the user briefly touches sibling projects. Pinned projects sort
	// among themselves by LastUsedAt desc; unpinned follow with the same rule.
	Pinned bool `yaml:"pinned,omitempty" json:"pinned,omitempty"`
	// Unix milliseconds of the last time the project's agent ran. Drives
	// the sidebar's "recent first" ordering. Omitted for never-used projects.
	LastUsedAt int64 `yaml:"last_used_at,omitempty" json:"lastUsedAt,omitempty"`
}

// Settings holds global studio preferences.
type Settings struct {
	Theme           string `yaml:"theme" json:"theme"`
	DefaultProvider string `yaml:"default_provider" json:"defaultProvider"`
	DefaultModel    string `yaml:"default_model" json:"defaultModel"`
	GLMKey          string `yaml:"glm_key,omitempty" json:"glmKey,omitempty"`
	MiniMaxKey      string `yaml:"minimax_key,omitempty" json:"minimaxKey,omitempty"`
	KimiKey         string `yaml:"kimi_key,omitempty" json:"kimiKey,omitempty"`
	DeepSeekKey     string `yaml:"deepseek_key,omitempty" json:"deepseekKey,omitempty"`
	OllamaURL       string `yaml:"ollama_url,omitempty" json:"ollamaUrl,omitempty"`
	// DefaultThinkingMode is applied to new projects created via AddProject.
	// "" = auto (provider default), "enabled", "disabled".
	DefaultThinkingMode   string `yaml:"default_thinking_mode,omitempty" json:"defaultThinkingMode,omitempty"`
	DefaultThinkingBudget int32  `yaml:"default_thinking_budget,omitempty" json:"defaultThinkingBudget,omitempty"`
	// DefaultBudgetUSD is applied to new projects created via AddProject.
	// 0 = no budget (no warnings). Same $100,000 cap as the per-project field.
	DefaultBudgetUSD float64 `yaml:"default_budget_usd,omitempty" json:"defaultBudgetUSD,omitempty"`
	// AutoCleanupDisabled, when true, prevents the once-per-24h background
	// cleanup pass on startup (iter 790+). Default is false (cleanup runs).
	// Users who want full control should toggle this on and run the manual
	// "Clean up" button from the Diagnostics modal.
	AutoCleanupDisabled bool `yaml:"auto_cleanup_disabled,omitempty" json:"autoCleanupDisabled,omitempty"`
	// AutoBackupEnabled, when true, runs a once-per-24h tar.gz snapshot of
	// the entire config dir into <configDir>/backups/ on startup (iter 840+).
	// Default is false — opt-IN because daily backups consume disk space
	// (10-70 MB total at 7-day retention). When enabled, oldest backups
	// beyond AutoBackupRetention are pruned automatically.
	AutoBackupEnabled bool `yaml:"auto_backup_enabled,omitempty" json:"autoBackupEnabled,omitempty"`
}

func configDir() string {
	// Allow tests and advanced users to override the config location without
	// recompiling. Takes precedence over the default ~/.config/gokin-studio.
	if override := os.Getenv("GOKIN_CONFIG_DIR"); override != "" {
		return override
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gokin-studio")
}

func configPath() string {
	return filepath.Join(configDir(), "config.yaml")
}

// LoadConfig reads the config from disk or returns defaults.
func LoadConfig() *StudioConfig {
	cfg := defaultConfig()
	data, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "gokin-studio: invalid config, using defaults: %v\n", err)
		return defaultConfig()
	}

	// Ensure defaults for fields that may be empty in old config files.
	defaults := defaultConfig()
	if cfg.Settings.DefaultProvider == "" {
		cfg.Settings.DefaultProvider = defaults.Settings.DefaultProvider
	}
	if cfg.Settings.DefaultModel == "" {
		cfg.Settings.DefaultModel = defaults.Settings.DefaultModel
	}
	if cfg.Settings.Theme == "" {
		cfg.Settings.Theme = defaults.Settings.Theme
	}
	if cfg.Settings.OllamaURL == "" {
		cfg.Settings.OllamaURL = defaults.Settings.OllamaURL
	}

	// Migrate deprecated models to current versions.
	// DeepSeek V4 series replaces v3 / chat / reasoner naming (legacy
	// aliases are dropped 2026-07-24 by DeepSeek; we move users to the
	// V4 lineup proactively). deepseek-reasoner → v4-pro (thinking),
	// deepseek-chat → v4-flash (non-thinking).
	deprecatedModels := map[string]string{
		"glm-4-plus":        "glm-5.2",
		"glm-4-air":         "glm-5.2",
		"glm-4-flash":       "glm-5.2",
		"glm-4-long":        "glm-5.2",
		"deepseek-chat":     "deepseek-v4-flash",
		"deepseek-reasoner": "deepseek-v4-pro",
	}
	if replacement, ok := deprecatedModels[cfg.Settings.DefaultModel]; ok {
		cfg.Settings.DefaultModel = replacement
	}

	// Providers no longer supported — migrate to GLM. iter 940+ note:
	// DeepSeek is BACK on this list as a supported provider as of V4,
	// so it's removed from removedProviders. Anthropic + Gemini stay
	// removed.
	removedProviders := map[string]bool{
		"anthropic": true,
		"gemini":    true,
	}

	// Migrate settings default provider if it was removed.
	if removedProviders[cfg.Settings.DefaultProvider] {
		cfg.Settings.DefaultProvider = "glm"
		cfg.Settings.DefaultModel = "glm-5.2"
	}

	// Migrate project-level provider/model: projects that still reference
	// empty or removed providers get the current default.
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		if p.Provider == "" || removedProviders[p.Provider] {
			p.Provider = "glm"
			p.Model = "glm-5.2"
		}
		if p.Model == "" {
			p.Model = cfg.Settings.DefaultModel
		}
		if replacement, ok := deprecatedModels[p.Model]; ok {
			p.Model = replacement
		}
	}

	return cfg
}

// Save writes the config to disk with restrictive permissions (0600).
// Atomic via the same write-temp-then-rename pattern history files use, so a
// crash mid-write doesn't corrupt the user's project list / API keys.
func (c *StudioConfig) Save() error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return atomicWriteFile(configPath(), data, 0o600)
}

func defaultConfig() *StudioConfig {
	return &StudioConfig{
		Settings: Settings{
			Theme:           "dark",
			DefaultProvider: "glm",
			DefaultModel:    "glm-5.2",
			OllamaURL:       "http://localhost:11434",
		},
	}
}
