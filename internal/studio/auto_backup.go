package studio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// AutoBackupDirName is the subdirectory inside configDir where periodic
	// auto-backups land. Excluded from manual archive walks so backups don't
	// recursively contain past backups.
	AutoBackupDirName = "backups"

	// AutoBackupRetention is the number of daily backups to keep before the
	// oldest gets pruned. ~70 MB worst case at 10 MB/backup is reasonable
	// for a release safety net.
	AutoBackupRetention = 7

	// autoBackupSentinelName is the filename whose mtime tracks last run.
	autoBackupSentinelName = ".last-auto-backup"

	// autoBackupThrottleHours: same 24h cadence as auto-cleanup. Daily
	// backup is the right granularity — more frequent wastes disk, less
	// frequent loses too much on crash.
	autoBackupThrottleHours = 24

	// autoBackupFilenamePrefix is the prefix for tar.gz files we create.
	// Used both when writing and when pruning by filename pattern.
	autoBackupFilenamePrefix = "auto-backup-"
)

// autoBackupDir returns the absolute path to the auto-backup directory.
func autoBackupDir() string {
	return filepath.Join(configDir(), AutoBackupDirName)
}

// autoBackupSentinelPath is the throttle-gate file. mtime older than
// throttle → run; missing → run; otherwise → skip.
func autoBackupSentinelPath() string {
	return filepath.Join(configDir(), autoBackupSentinelName)
}

// shouldRunAutoBackup is the iter 790+ pattern — sentinel mtime gate so
// we don't burn disk on every startup. Fail-open if stat hiccups.
func shouldRunAutoBackup() bool {
	info, err := os.Stat(autoBackupSentinelPath())
	if err != nil {
		return true
	}
	return time.Since(info.ModTime()) > autoBackupThrottleHours*time.Hour
}

// touchAutoBackupSentinel records "we just ran" so the next 24h skip the
// expensive walk. Mirrors touchAutoCleanupSentinel from iter 790+.
func touchAutoBackupSentinel() {
	path := autoBackupSentinelPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return
	}
	if f, err := os.Create(path); err == nil {
		_ = f.Close()
	}
}

// pruneOldAutoBackups removes auto-backup files beyond the retention limit.
// See pruneOldAutoBackupsImpl for the heavy lifting; this thin wrapper
// keeps the original 0-arg signature for iter 840+ callers.
func pruneOldAutoBackups() (removed int, freed int64) {
	return pruneOldAutoBackupsImpl(false)
}

// pruneOldAutoBackupsImpl is the inner implementation that supports dry-run.
// When dryRun is true, returns the COUNTS that would be removed but does
// not touch disk — used by iter 930+ CleanupOldData's preview path so
// users see what's pending before confirming the cleanup.
//
// Sorts by mtime (newest first), keeps the first AutoBackupRetention,
// reports the rest. Per-file errors during deletion don't abort the sweep;
// in dry-run mode there's no deletion so no error path.
func pruneOldAutoBackupsImpl(dryRun bool) (removed int, freed int64) {
	dir := autoBackupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}

	type entry struct {
		path  string
		mtime time.Time
		size  int64
	}
	var files []entry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, autoBackupFilenamePrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, entry{
			path:  filepath.Join(dir, name),
			mtime: info.ModTime(),
			size:  info.Size(),
		})
	}
	if len(files) <= AutoBackupRetention {
		return 0, 0
	}
	// Newest first.
	sort.Slice(files, func(i, j int) bool {
		return files[i].mtime.After(files[j].mtime)
	})
	for _, f := range files[AutoBackupRetention:] {
		if dryRun {
			removed++
			freed += f.size
			continue
		}
		if err := os.Remove(f.path); err == nil {
			removed++
			freed += f.size
		}
	}
	return removed, freed
}

// AutoBackupResult summarises a single auto-backup pass for tests + logs.
// Fields are populated even on no-op skip (Skipped=true) so callers can
// surface "nothing to do" telemetry.
type AutoBackupResult struct {
	Skipped       bool   `json:"skipped"`
	SkipReason    string `json:"skipReason,omitempty"`
	BackupPath    string `json:"backupPath,omitempty"`
	FilesIncluded int    `json:"filesIncluded,omitempty"`
	Size          int64  `json:"size,omitempty"`
	Pruned        int    `json:"pruned,omitempty"`
}

// RunAutoBackupIfDue creates a daily snapshot of configDir under
// <configDir>/backups/auto-backup-<YYYY-MM-DD>.tar.gz IF the user has
// opted in (Settings.AutoBackupEnabled) AND the 24h throttle has elapsed.
// After writing, prunes the oldest backups beyond AutoBackupRetention.
//
// Intended to be called once at Startup from a goroutine — slow file walks
// don't block UI bring-up, and errors are swallowed into the event log
// rather than propagated. Returns a structured AutoBackupResult so tests
// can assert behaviour deterministically.
func (s *Studio) RunAutoBackupIfDue() (*AutoBackupResult, error) {
	s.mu.RLock()
	enabled := s.config != nil && s.config.Settings.AutoBackupEnabled
	s.mu.RUnlock()
	if !enabled {
		return &AutoBackupResult{Skipped: true, SkipReason: "AutoBackupEnabled is false"}, nil
	}
	if !shouldRunAutoBackup() {
		return &AutoBackupResult{Skipped: true, SkipReason: "throttle: <24h since last run"}, nil
	}

	cfgDir := configDir()
	if _, err := os.Stat(cfgDir); err != nil {
		return &AutoBackupResult{Skipped: true, SkipReason: "config dir does not exist"}, nil
	}

	backupDir := autoBackupDir()
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		s.logf("warn", "backup", "auto-backup: cannot create dir: %v", err)
		return nil, fmt.Errorf("create backup dir: %w", err)
	}

	stamp := time.Now().Format("2006-01-02")
	fname := autoBackupFilenamePrefix + stamp + ".tar.gz"
	// If the same day's backup already exists, add a HHMMSS suffix so we
	// don't overwrite (catches the rare case where user manually touched
	// the sentinel and we run twice in a day).
	target := filepath.Join(backupDir, fname)
	if _, err := os.Stat(target); err == nil {
		fname = autoBackupFilenamePrefix + time.Now().Format("2006-01-02-150405") + ".tar.gz"
		target = filepath.Join(backupDir, fname)
	}

	// Write directly to file (skips the base64 step iter 750+ Export uses).
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		s.logf("warn", "backup", "auto-backup: cannot open target: %v", err)
		return nil, fmt.Errorf("open backup target: %w", err)
	}
	filesCount, walkErr := writeConfigArchive(f, cfgDir)
	closeErr := f.Close()
	if walkErr != nil {
		_ = os.Remove(target) // leave nothing half-written behind
		s.logf("warn", "backup", "auto-backup: walk failed: %v", walkErr)
		return nil, walkErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		s.logf("warn", "backup", "auto-backup: close failed: %v", closeErr)
		return nil, closeErr
	}

	stat, _ := os.Stat(target)
	size := int64(0)
	if stat != nil {
		size = stat.Size()
	}
	touchAutoBackupSentinel()
	pruned, freed := pruneOldAutoBackups()

	s.logf("info", "backup",
		"auto-backup: wrote %s (%d files, %s); pruned %d old (freed %s)",
		fname, filesCount, humanBytes(size), pruned, humanBytes(freed))

	return &AutoBackupResult{
		Skipped:       false,
		BackupPath:    target,
		FilesIncluded: filesCount,
		Size:          size,
		Pruned:        pruned,
	}, nil
}

// --- iter 850+: list / delete / restore management for auto-backups ---

// AutoBackupFile describes a single tar.gz file from the iter 840+
// auto-backup directory. Mirrors PreImportBackup but separate type so the
// Wails JSON shape doesn't blur the two — auto-backups are user-content
// snapshots, pre-import backups are rollback points from a prior Import.
type AutoBackupFile struct {
	Filename    string `json:"filename"`    // basename, e.g. "auto-backup-2025-05-16.tar.gz"
	Path        string `json:"path"`        // absolute path
	CreatedAtMs int64  `json:"createdAtMs"` // mtime in ms
	Size        int64  `json:"size"`        // file size in bytes
}

// ListAutoBackups returns every auto-backup file in <configDir>/backups/,
// sorted newest-first. Returns an empty list (no error) when the dir
// doesn't exist yet — typical fresh-install state.
func (s *Studio) ListAutoBackups() ([]AutoBackupFile, error) {
	dir := autoBackupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []AutoBackupFile{}, nil
		}
		return nil, fmt.Errorf("cannot read backups dir: %w", err)
	}
	out := []AutoBackupFile{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, autoBackupFilenamePrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, AutoBackupFile{
			Filename:    name,
			Path:        filepath.Join(dir, name),
			CreatedAtMs: info.ModTime().UnixMilli(),
			Size:        info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAtMs > out[j].CreatedAtMs
	})
	return out, nil
}

// validateAutoBackupFilename rejects anything that isn't a well-formed
// auto-backup tar.gz filename. Defends DeleteAutoBackup/RestoreAutoBackup
// from path traversal — frontend passes a basename string, NOT a path.
// Mirror of validateBackupName from iter 810+/830+.
func validateAutoBackupFilename(name string) error {
	if name == "" {
		return errors.New("backup filename is empty")
	}
	if !strings.HasPrefix(name, autoBackupFilenamePrefix) {
		return fmt.Errorf("backup filename must start with %q", autoBackupFilenamePrefix)
	}
	if !strings.HasSuffix(name, ".tar.gz") {
		return errors.New("backup filename must end with .tar.gz")
	}
	if filepath.Base(name) != name {
		return errors.New("backup filename must be a plain basename (no path separators)")
	}
	if strings.Contains(name, "..") {
		return errors.New("backup filename must not contain ..")
	}
	return nil
}

// DeleteAutoBackup removes one specific auto-backup file by basename.
// Validates the filename to defend against path traversal. Symlinks are
// rejected (refused to follow).
func (s *Studio) DeleteAutoBackup(filename string) error {
	if err := validateAutoBackupFilename(filename); err != nil {
		return err
	}
	full := filepath.Join(autoBackupDir(), filename)
	info, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("backup not found")
		}
		return fmt.Errorf("cannot stat backup: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to follow symlink at backup path")
	}
	if info.IsDir() {
		return errors.New("backup path is not a file")
	}
	if err := os.Remove(full); err != nil {
		return fmt.Errorf("could not remove backup: %w", err)
	}
	s.logf("info", "backup", "deleted auto-backup %q", filename)
	return nil
}

// RestoreAutoBackup extracts an auto-backup tar.gz over the current config
// directory. Reuses extractArchiveToConfigDir so the atomic-swap +
// pre-import safety backup semantics are identical to iter 750+ Import.
// In-memory state is NOT reloaded → RestartRequired in result.
func (s *Studio) RestoreAutoBackup(filename string) (*ImportResult, error) {
	if err := validateAutoBackupFilename(filename); err != nil {
		return nil, err
	}
	full := filepath.Join(autoBackupDir(), filename)
	info, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("backup not found")
		}
		return nil, fmt.Errorf("cannot stat backup: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("refusing to follow symlink at backup path")
	}
	if info.IsDir() {
		return nil, errors.New("backup path is not a file")
	}
	if info.Size() > ImportArchiveMaxBytes {
		return nil, fmt.Errorf("backup too large (%d bytes, max %d)", info.Size(), ImportArchiveMaxBytes)
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, fmt.Errorf("cannot open backup: %w", err)
	}
	defer f.Close()
	// iter 860+: pass preRestorePrefix so the safety snapshot is named
	// .gokin-studio.pre-restore-* (semantically correct — restore created
	// it, not import) instead of mislabelled .gokin-studio.pre-import-*.
	// Both prefixes register in snapshotPrefixes (iter 830+), so the safety
	// snapshot still shows up in ListPreImportBackups + auto-cleanup either
	// way; this just stops the audit log and listing tooltips from lying.
	result, err := s.extractArchiveToConfigDir(f, "restore", preRestorePrefix)
	if err != nil {
		return nil, err
	}
	s.logf("info", "backup", "restored from auto-backup %q (safety backup at %s)", filename, result.PreBackupPath)
	return result, nil
}
