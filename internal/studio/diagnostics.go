package studio

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Version is the current release version of Gokin Studio.
// Bumped on each release. Surfaced in About panel and diagnostics exports.
const Version = "2.1.2"

// BuildInfo describes the running binary: version + runtime environment.
// Used by the About panel and "Copy diagnostics" reports so support requests
// always include the basics without the user having to paste them manually.
type BuildInfo struct {
	Version   string `json:"version"`
	GoVersion string `json:"goVersion"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	NumCPU    int    `json:"numCPU"`
}

// HealthCheck is one diagnostic finding. Status is "ok", "warn", or "error".
// Categories group related checks ("config", "providers", "projects", "data").
type HealthCheck struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
}

// DiagnosticsInfo is the full diagnostic report. Includes build info, config
// dir health, totals across projects/sessions/history bytes, and the set of
// health checks. Designed to be one-shot copyable into a bug report.
type DiagnosticsInfo struct {
	Build         BuildInfo     `json:"build"`
	ConfigDir     string        `json:"configDir"`
	ConfigDirOK   bool          `json:"configDirOK"`
	ConfigDirSize int64         `json:"configDirSize"`
	TotalProjects int           `json:"totalProjects"`
	TotalSessions int           `json:"totalSessions"`
	HistoryBytes  int64         `json:"historyBytes"`
	StaleReplays  int           `json:"staleReplays"`
	Checks        []HealthCheck `json:"checks"`
	GeneratedAtMs int64         `json:"generatedAtMs"`
}

// GetBuildInfo returns the build metadata exposed to the frontend.
// Pure function — safe to call without locks.
func (s *Studio) GetBuildInfo() *BuildInfo {
	return &BuildInfo{
		Version:   Version,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		NumCPU:    runtime.NumCPU(),
	}
}

// GetDiagnostics runs all health checks and returns a complete report.
// Read-only — never modifies disk or in-memory state.
func (s *Studio) GetDiagnostics() *DiagnosticsInfo {
	info := &DiagnosticsInfo{
		Build:         *s.GetBuildInfo(),
		ConfigDir:     configDir(),
		GeneratedAtMs: time.Now().UnixMilli(),
	}

	// Config directory: existence + writability + total size.
	info.ConfigDirOK, info.ConfigDirSize = inspectConfigDir(info.ConfigDir)

	s.mu.RLock()
	info.TotalProjects = len(s.projects)
	projectsSnap := make([]*Project, 0, len(s.projects))
	for _, p := range s.projects {
		projectsSnap = append(projectsSnap, p)
	}
	s.mu.RUnlock()

	// Tally sessions across projects (under each project's own lock).
	for _, p := range projectsSnap {
		p.mu.RLock()
		info.TotalSessions += len(p.sessions)
		p.mu.RUnlock()
	}

	// History dir size + stale replay count (older than 7 days).
	histDir := filepath.Join(info.ConfigDir, "history")
	info.HistoryBytes, info.StaleReplays = inspectHistoryDir(histDir)

	// Health checks.
	info.Checks = s.runHealthChecks(projectsSnap)

	return info
}

// runHealthChecks performs all health checks under the studio lock,
// returns a flat list of findings grouped by Category.
func (s *Studio) runHealthChecks(projects []*Project) []HealthCheck {
	checks := []HealthCheck{}

	// Config directory writability.
	cdir := configDir()
	checks = append(checks, checkConfigDirWritable(cdir))

	// API keys: warn if a configured project uses a provider whose key is missing.
	s.mu.RLock()
	settings := s.config.Settings
	s.mu.RUnlock()
	checks = append(checks, checkAPIKeys(projects, settings)...)

	// Project directories: each project's working dir must exist.
	checks = append(checks, checkProjectDirs(projects)...)

	// Stale replay logs (interrupted sessions older than 7 days are
	// almost certainly abandoned — note for cleanup).
	histDir := filepath.Join(cdir, "history")
	if stale := countStaleReplays(histDir, 7*24*time.Hour); stale > 0 {
		checks = append(checks, HealthCheck{
			Category: "data",
			Name:     "Stale replay logs",
			Status:   "warn",
			Message:  fmt.Sprintf("%d replay file(s) older than 7 days", stale),
			Detail:   "Old replay logs from interrupted sessions. Safe to leave; they auto-clean when a session completes normally.",
		})
	} else {
		checks = append(checks, HealthCheck{
			Category: "data",
			Name:     "Stale replay logs",
			Status:   "ok",
			Message:  "No stale replay files",
		})
	}

	return checks
}

// inspectConfigDir returns (writable, totalSize). Writable=false if the
// directory doesn't exist OR a sentinel write fails. Size walks recursively.
func inspectConfigDir(dir string) (bool, int64) {
	stat, err := os.Stat(dir)
	if err != nil || !stat.IsDir() {
		return false, 0
	}
	// Write probe: create + remove a sentinel file.
	probe := filepath.Join(dir, ".gokin-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return false, dirSize(dir)
	}
	_ = os.Remove(probe)
	return true, dirSize(dir)
}

// dirSize walks the directory tree summing regular file sizes. Symlinks and
// errors are silently skipped — best-effort report, not a security audit.
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// inspectHistoryDir returns (totalSize, staleReplayCount). Stale = older than
// 7 days replay file (*.replay.jsonl).
func inspectHistoryDir(dir string) (int64, int) {
	var total int64
	stale := 0
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		if strings.HasSuffix(path, ".replay.jsonl") && info.ModTime().Before(cutoff) {
			stale++
		}
		return nil
	})
	return total, stale
}

// countStaleReplays returns the number of replay files older than maxAge.
func countStaleReplays(dir string, maxAge time.Duration) int {
	count := 0
	cutoff := time.Now().Add(-maxAge)
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".replay.jsonl") {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) {
			count++
		}
		return nil
	})
	return count
}

// checkConfigDirWritable returns a HealthCheck for whether the config dir
// is writable. Distinguishes "doesn't exist" (error) from "exists but
// not writable" (error with hint).
func checkConfigDirWritable(dir string) HealthCheck {
	stat, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return HealthCheck{
				Category: "config",
				Name:     "Config directory",
				Status:   "warn",
				Message:  "Config directory does not exist (will be created on first save)",
				Detail:   dir,
			}
		}
		return HealthCheck{
			Category: "config",
			Name:     "Config directory",
			Status:   "error",
			Message:  fmt.Sprintf("Cannot stat config directory: %v", err),
			Detail:   dir,
		}
	}
	if !stat.IsDir() {
		return HealthCheck{
			Category: "config",
			Name:     "Config directory",
			Status:   "error",
			Message:  "Config path exists but is not a directory",
			Detail:   dir,
		}
	}
	probe := filepath.Join(dir, ".gokin-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return HealthCheck{
			Category: "config",
			Name:     "Config directory",
			Status:   "error",
			Message:  "Config directory is not writable",
			Detail:   fmt.Sprintf("%s — %v", dir, err),
		}
	}
	_ = os.Remove(probe)
	return HealthCheck{
		Category: "config",
		Name:     "Config directory",
		Status:   "ok",
		Message:  "Writable",
		Detail:   dir,
	}
}

// checkAPIKeys warns when a supported project's provider has no key configured.
func checkAPIKeys(projects []*Project, settings Settings) []HealthCheck {
	// providerInUse[provider] = true if at least one project uses it.
	providerInUse := map[string]bool{}
	for _, p := range projects {
		p.mu.RLock()
		if p.Provider != "" {
			providerInUse[strings.ToLower(p.Provider)] = true
		}
		p.mu.RUnlock()
	}

	checks := []HealthCheck{}
	providerLabel := map[string]string{
		"glm":  "GLM",
		"kimi": "Kimi",
	}

	// Stable iteration order so the report is reproducible.
	for _, prov := range []string{"glm", "kimi"} {
		if !providerInUse[prov] {
			continue
		}
		label := providerLabel[prov]
		// iter 780+: use ResolveProviderKey so env-var fallbacks (matching
		// initClient's actual lookup order) are recognised here too.
		// Previously env-only users saw a false "API key missing" error.
		key, src := ResolveProviderKey(prov, settings)
		if key == "" {
			checks = append(checks, HealthCheck{
				Category: "providers",
				Name:     label + " API key",
				Status:   "error",
				Message:  "API key missing — projects using " + label + " cannot send messages",
			})
		} else {
			msg := "API key set"
			if src == KeySourceEnv {
				msg = "API key set (from $" + envVarForProvider(prov) + ")"
			}
			checks = append(checks, HealthCheck{
				Category: "providers",
				Name:     label + " API key",
				Status:   "ok",
				Message:  msg,
			})
		}
	}
	return checks
}

// checkProjectDirs flags any project whose working directory has been
// deleted or made inaccessible since the project was added.
func checkProjectDirs(projects []*Project) []HealthCheck {
	checks := []HealthCheck{}
	missing := []string{}
	for _, p := range projects {
		p.mu.RLock()
		dir := p.Directory
		name := p.Name
		p.mu.RUnlock()
		if dir == "" {
			continue
		}
		if stat, err := os.Stat(dir); err != nil || !stat.IsDir() {
			missing = append(missing, fmt.Sprintf("%s (%s)", name, dir))
		}
	}
	if len(missing) > 0 {
		checks = append(checks, HealthCheck{
			Category: "projects",
			Name:     "Project directories",
			Status:   "error",
			Message:  fmt.Sprintf("%d project(s) reference missing directories", len(missing)),
			Detail:   strings.Join(missing, "\n"),
		})
	} else if len(projects) > 0 {
		checks = append(checks, HealthCheck{
			Category: "projects",
			Name:     "Project directories",
			Status:   "ok",
			Message:  "All project directories exist",
		})
	}
	return checks
}

// DiagnosticsReport renders DiagnosticsInfo as a plain-text report
// suitable for copy/paste into a bug report or support ticket.
func (s *Studio) DiagnosticsReport() string {
	d := s.GetDiagnostics()
	var sb strings.Builder
	sb.WriteString("Gokin Studio Diagnostics\n")
	sb.WriteString("========================\n\n")
	fmt.Fprintf(&sb, "Version: %s\n", d.Build.Version)
	fmt.Fprintf(&sb, "Go:      %s\n", d.Build.GoVersion)
	fmt.Fprintf(&sb, "OS:      %s/%s (%d CPUs)\n", d.Build.OS, d.Build.Arch, d.Build.NumCPU)
	fmt.Fprintf(&sb, "Time:    %s\n", time.UnixMilli(d.GeneratedAtMs).Format(time.RFC3339))
	sb.WriteString("\n")
	sb.WriteString("Storage\n-------\n")
	fmt.Fprintf(&sb, "Config dir: %s (writable: %v, %s)\n", d.ConfigDir, d.ConfigDirOK, humanBytes(d.ConfigDirSize))
	fmt.Fprintf(&sb, "History:    %s\n", humanBytes(d.HistoryBytes))
	fmt.Fprintf(&sb, "Projects:   %d\n", d.TotalProjects)
	fmt.Fprintf(&sb, "Sessions:   %d\n", d.TotalSessions)
	sb.WriteString("\nHealth checks\n-------------\n")
	for _, c := range d.Checks {
		marker := "[OK]   "
		switch c.Status {
		case "warn":
			marker = "[WARN] "
		case "error":
			marker = "[ERR]  "
		}
		fmt.Fprintf(&sb, "%s%s — %s\n", marker, c.Name, c.Message)
		if c.Detail != "" {
			for line := range strings.SplitSeq(c.Detail, "\n") {
				sb.WriteString("        " + line + "\n")
			}
		}
	}
	return sb.String()
}

// humanBytes renders a byte count as a short human-readable string.
// Used in DiagnosticsReport; keeps output skimmable.
func humanBytes(n int64) string {
	const (
		kb = 1024
		mb = 1024 * 1024
		gb = 1024 * 1024 * 1024
	)
	switch {
	case n < kb:
		return fmt.Sprintf("%d B", n)
	case n < mb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	case n < gb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	default:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(gb))
	}
}
