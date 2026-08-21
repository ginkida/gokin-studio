package studio

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	StudioConfigMaxBytes       = 4 << 20
	StudioConfigMaxProjects    = 500
	GlobalInstructionsMaxBytes = 64 << 10
)

// StudioConfig is the top-level configuration saved to disk.
type StudioConfig struct {
	Projects []ProjectConfig `yaml:"projects" json:"projects"`
	// Groups bundle related projects. They live inside the config file so they
	// inherit its atomic 0600 save and need no new file or lock.
	Groups   []ProjectGroupConfig `yaml:"groups,omitempty" json:"groups,omitempty"`
	Settings Settings             `yaml:"settings" json:"settings"`
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
	// "disabled" = off. ThinkingBudget is the backwards-compatible reasoning
	// control (0 = model default; native effort models map it to named levels).
	ThinkingMode   string `yaml:"thinking_mode,omitempty" json:"thinkingMode,omitempty"`
	ThinkingBudget int32  `yaml:"thinking_budget,omitempty" json:"thinkingBudget,omitempty"`
	// PermissionMode controls how cautious the agent is about changes.
	// "" / "auto" = reviewed Auto; "accept_edits" = automatically approve
	// bounded file edits; "manual" = ask; "skip" = bypass ordinary approvals.
	// Legacy "ask" is migrated to "manual".
	PermissionMode string `yaml:"permission_mode,omitempty" json:"permissionMode,omitempty"`
	// Description and Capabilities are how OTHER projects' agents recognise
	// this one. They are the replacement for ask_agent's fixed role enum, which
	// no real project could ever match. Both are user-owned text: they ride
	// into another project's prompt, so nothing generates them automatically.
	Description  string   `yaml:"description,omitempty" json:"description,omitempty"`
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	// DelegationPolicy controls who may delegate INTO this project:
	// "any" (default, today's reachability) | "group" | "off".
	DelegationPolicy string `yaml:"delegation_policy,omitempty" json:"delegationPolicy,omitempty"`
	// ComputerUseEnabled exposes OS-level screen tools to this project. It is
	// opt-in and computer_* calls remain runtime-gated even in auto mode.
	ComputerUseEnabled  bool     `yaml:"computer_use_enabled,omitempty" json:"computerUseEnabled,omitempty"`
	ComputerAllowedApps []string `yaml:"computer_allowed_apps,omitempty" json:"computerAllowedApps,omitempty"`
	ComputerBlockedApps []string `yaml:"computer_blocked_apps,omitempty" json:"computerBlockedApps,omitempty"`
	// ToolPermissions are explicit project-scoped "Always allow" grants for a
	// small allowlist of ordinary local tools. Arguments and file contents are
	// never persisted, and hard-gated variants are reclassified on every call.
	ToolPermissions []ToolPermissionRule `yaml:"tool_permissions,omitempty" json:"toolPermissions,omitempty"`
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
	KimiKey         string `yaml:"kimi_key,omitempty" json:"kimiKey,omitempty"`
	// GlobalInstructions are user-authored preferences applied to every GLM
	// and Kimi project. Project-specific instructions are placed after them
	// and therefore remain the more specific override.
	GlobalInstructions string `yaml:"global_instructions,omitempty" json:"globalInstructions,omitempty"`
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
	// QuickEntryEnabled registers QuickEntryShortcut as a native global
	// shortcut while the desktop app is running. It is opt-in so Gokin never
	// steals a system shortcut without the user's explicit choice.
	QuickEntryEnabled  bool   `yaml:"quick_entry_enabled,omitempty" json:"quickEntryEnabled,omitempty"`
	QuickEntryShortcut string `yaml:"quick_entry_shortcut,omitempty" json:"quickEntryShortcut,omitempty"`
	// VoiceShortcutEnabled is separate from QuickEntryEnabled: a user may
	// dictate through Caps Lock without also reserving the text-entry gesture,
	// or keep Quick Entry enabled without granting speech/accessibility access.
	VoiceShortcutEnabled bool   `yaml:"voice_shortcut_enabled,omitempty" json:"voiceShortcutEnabled,omitempty"`
	VoiceShortcut        string `yaml:"voice_shortcut,omitempty" json:"voiceShortcut,omitempty"`
	// KeepAwakeEnabled holds an OS sleep-inhibition lease while any GLM/Kimi
	// run is active or at least one local scheduled task is enabled. Opt-in
	// because an always-enabled schedule can increase laptop battery use.
	KeepAwakeEnabled bool `yaml:"keep_awake_enabled,omitempty" json:"keepAwakeEnabled,omitempty"`
	// AutoUpdateCheckDisabled opts out of the once-per-24h release check.
	// Checks are notify-only: Studio never downloads or installs an artifact in
	// the background, because community builds are not code-signed yet.
	AutoUpdateCheckDisabled bool `yaml:"auto_update_check_disabled,omitempty" json:"autoUpdateCheckDisabled,omitempty"`
	// AutoArchivePRAfterClose hides a finished local chat after its associated
	// GitHub pull request reaches MERGED or CLOSED. Disabled by default; dirty,
	// running, unavailable, and last-active sessions always remain visible.
	AutoArchivePRAfterClose bool `yaml:"auto_archive_pr_after_close,omitempty" json:"autoArchivePRAfterClose,omitempty"`
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
	data, err := readRegularFileLimited(configPath(), StudioConfigMaxBytes)
	if err != nil {
		if !os.IsNotExist(err) {
			// quarantineInvalidConfig only renames a real regular file, so an
			// unloadable one (oversized, wrong type) is set aside with its
			// bytes preserved, while the common dotfiles setup — config.yaml
			// symlinked into a repo, which readRegularFileLimited now refuses
			// on purpose — is left untouched. Unlinking that symlink would
			// silently detach the user's config and start with no projects and
			// no API keys.
			quarantined := quarantineInvalidConfig()
			fmt.Fprintf(os.Stderr, "gokin-studio: config unreadable, using defaults: %v%s\n", err, quarantined)
		}
		return defaultConfig()
	}
	cfg, err := parseStudioConfig(data)
	if err != nil {
		quarantined := quarantineInvalidConfig()
		fmt.Fprintf(os.Stderr, "gokin-studio: invalid config, using defaults: %v%s\n", err, quarantined)
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
	if cfg.Settings.QuickEntryShortcut == "" {
		cfg.Settings.QuickEntryShortcut = defaults.Settings.QuickEntryShortcut
	} else if normalized, err := normalizeQuickEntryShortcut(cfg.Settings.QuickEntryShortcut); err == nil {
		cfg.Settings.QuickEntryShortcut = normalized
	} else {
		cfg.Settings.QuickEntryShortcut = defaults.Settings.QuickEntryShortcut
	}
	if cfg.Settings.VoiceShortcut == "" {
		cfg.Settings.VoiceShortcut = defaults.Settings.VoiceShortcut
	} else if normalized, err := normalizeVoiceShortcut(cfg.Settings.VoiceShortcut); err == nil {
		cfg.Settings.VoiceShortcut = normalized
	} else {
		cfg.Settings.VoiceShortcut = defaults.Settings.VoiceShortcut
	}
	if cfg.Settings.Theme != "dark" && cfg.Settings.Theme != "light" && cfg.Settings.Theme != "system" {
		cfg.Settings.Theme = defaults.Settings.Theme
	}
	if cfg.Settings.DefaultThinkingMode != "" && cfg.Settings.DefaultThinkingMode != "enabled" && cfg.Settings.DefaultThinkingMode != "disabled" {
		cfg.Settings.DefaultThinkingMode = ""
	}
	if cfg.Settings.DefaultThinkingBudget < 0 || cfg.Settings.DefaultThinkingBudget > 1_000_000 {
		cfg.Settings.DefaultThinkingBudget = 0
	}
	cfg.Settings.GLMKey = strings.TrimSpace(cfg.Settings.GLMKey)
	cfg.Settings.KimiKey = strings.TrimSpace(cfg.Settings.KimiKey)
	cfg.Settings.GlobalInstructions = truncateUTF8(strings.TrimSpace(cfg.Settings.GlobalInstructions), GlobalInstructionsMaxBytes)

	// Repair identifiers from hand-edited/legacy configs before the projects
	// enter the runtime map. Empty/duplicate IDs otherwise overwrite each other
	// during Startup. Entries without a directory are unusable and unsafe
	// (filepath.Abs("") means the process cwd), so omit them.
	seenIDs := make(map[string]struct{}, len(cfg.Projects))
	normalized := make([]ProjectConfig, 0, len(cfg.Projects))
	for _, project := range cfg.Projects {
		project.Directory = strings.TrimSpace(project.Directory)
		if project.Directory == "" {
			continue
		}
		project.ID = strings.TrimSpace(project.ID)
		if project.ID == "" {
			project.ID = GenerateID()
		}
		if _, duplicate := seenIDs[project.ID]; duplicate {
			for {
				project.ID = GenerateID()
				if _, exists := seenIDs[project.ID]; !exists {
					break
				}
			}
		}
		seenIDs[project.ID] = struct{}{}
		if strings.TrimSpace(project.Name) == "" {
			project.Name = filepath.Base(project.Directory)
			if project.Name == "." || project.Name == string(filepath.Separator) || project.Name == "" {
				project.Name = "Project"
			}
		}
		normalized = append(normalized, project)
	}
	cfg.Projects = normalized

	cfg.Settings.DefaultProvider, cfg.Settings.DefaultModel = normalizeStudioProviderModel(
		cfg.Settings.DefaultProvider,
		cfg.Settings.DefaultModel,
	)

	// Migrate project-level provider/model: projects that still reference
	// empty or removed providers get the current default.
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		p.Name = truncateUTF8(strings.TrimSpace(p.Name), 60)
		p.Directory = strings.TrimSpace(p.Directory)
		p.SystemPrompt = truncateUTF8(p.SystemPrompt, 64<<10)
		if p.Temperature < 0 || p.Temperature > 2 || math.IsNaN(float64(p.Temperature)) || math.IsInf(float64(p.Temperature), 0) {
			p.Temperature = 0
		}
		if p.BudgetUSD < 0 || p.BudgetUSD > 100_000 || math.IsNaN(p.BudgetUSD) || math.IsInf(p.BudgetUSD, 0) {
			p.BudgetUSD = 0
		}
		if p.LastUsedAt < 0 {
			p.LastUsedAt = 0
		}
		if p.ThinkingMode != "" && p.ThinkingMode != "enabled" && p.ThinkingMode != "disabled" {
			p.ThinkingMode = ""
		}
		if p.ThinkingBudget < 0 || p.ThinkingBudget > 1_000_000 {
			p.ThinkingBudget = 0
		}
		if p.PermissionMode == "ask" {
			p.PermissionMode = "manual"
		}
		if p.PermissionMode == "acceptEdits" || p.PermissionMode == "accept-edits" {
			p.PermissionMode = "accept_edits"
		}
		if p.PermissionMode != "" && p.PermissionMode != "auto" && p.PermissionMode != "accept_edits" &&
			p.PermissionMode != "manual" && p.PermissionMode != "skip" {
			p.PermissionMode = ""
		}
		p.ComputerAllowedApps = sanitizeComputerAppIDs(p.ComputerAllowedApps)
		p.ComputerBlockedApps = sanitizeComputerAppIDs(p.ComputerBlockedApps)
		p.ToolPermissions = sanitizeToolPermissionRules(p.ToolPermissions)
		if len(p.ComputerBlockedApps) > 0 && len(p.ComputerAllowedApps) > 0 {
			blocked := make(map[string]bool, len(p.ComputerBlockedApps))
			for _, id := range p.ComputerBlockedApps {
				blocked[id] = true
			}
			allowed := p.ComputerAllowedApps[:0]
			for _, id := range p.ComputerAllowedApps {
				if !blocked[id] {
					allowed = append(allowed, id)
				}
			}
			p.ComputerAllowedApps = allowed
		}
		if p.BudgetUSD == 0 {
			p.EnforceBudget = false
		}
		p.Provider, p.Model = normalizeStudioProviderModel(p.Provider, p.Model)
		if p.MaxTokens < 0 || p.MaxTokens > int(maxOutputTokens(p.Provider, p.Model)) {
			p.MaxTokens = 0
		}
	}
	if cfg.Settings.DefaultBudgetUSD < 0 || cfg.Settings.DefaultBudgetUSD > 100_000 ||
		math.IsNaN(cfg.Settings.DefaultBudgetUSD) || math.IsInf(cfg.Settings.DefaultBudgetUSD, 0) {
		cfg.Settings.DefaultBudgetUSD = 0
	}

	return cfg
}

// parseStudioConfig is the shared syntax and structural-boundary parser used
// by startup and restore preflight. Runtime normalization remains in LoadConfig
// so validation never mutates imported bytes or generates replacement IDs.
func parseStudioConfig(data []byte) (*StudioConfig, error) {
	cfg := defaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if len(cfg.Projects) > StudioConfigMaxProjects {
		return nil, fmt.Errorf("config has too many projects (%d, maximum %d)", len(cfg.Projects), StudioConfigMaxProjects)
	}
	return cfg, nil
}

func validateStudioConfigFile(path string) error {
	data, err := readRegularFileLimited(path, StudioConfigMaxBytes)
	if err != nil {
		return err
	}
	_, err = parseStudioConfig(data)
	return err
}

func quarantineInvalidConfig() string {
	path := configPath()
	info, err := os.Lstat(path)
	// Only a real, regular file may be quarantined. A symlink points at
	// something the user owns elsewhere (a dotfiles repo, typically); renaming
	// the link would unlink their config without touching — or explaining —
	// the actual file.
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	dst := path + ".corrupt-" + time.Now().Format("20060102-150405.000")
	if err := os.Rename(path, dst); err != nil {
		return ""
	}
	return "; quarantined as " + filepath.Base(dst)
}

// Save writes the config to disk with restrictive permissions (0600).
// Atomic via the same write-temp-then-rename pattern history files use, so a
// crash mid-write doesn't corrupt the user's project list / API keys.
func (c *StudioConfig) Save() error {
	if c == nil {
		return fmt.Errorf("cannot save nil config")
	}
	if len(c.Projects) > StudioConfigMaxProjects {
		return fmt.Errorf("too many projects (%d, maximum %d)", len(c.Projects), StudioConfigMaxProjects)
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if len(data) > StudioConfigMaxBytes {
		return fmt.Errorf("config is too large (%d bytes, maximum %d)", len(data), StudioConfigMaxBytes)
	}
	return atomicWriteFile(configPath(), data, 0o600)
}

func defaultConfig() *StudioConfig {
	return &StudioConfig{
		Settings: Settings{
			Theme:              "dark",
			DefaultProvider:    defaultStudioProvider,
			DefaultModel:       defaultStudioModel,
			QuickEntryShortcut: defaultQuickEntryShortcut(),
			VoiceShortcut:      defaultVoiceShortcut(),
		},
	}
}
