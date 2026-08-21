package studio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	autoBackupTempPrefix     = ".auto-backup-writing-"
	autoBackupTempSuffix     = ".tmp"
	autoBackupTempGrace      = time.Hour
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

var (
	// All mutating backup operations share one lifecycle lock. Startup launches
	// auto-cleanup and auto-backup independently, and Wails can invoke exported
	// methods concurrently; without this lock two writers can truncate the same
	// daily filename or retention can race a restore/delete.
	autoBackupMu            sync.Mutex
	autoBackupRemoveFile    = os.Remove
	autoBackupOpenFile      = os.Open
	autoBackupArchiveWriter = writeConfigArchive
	autoBackupNow           = time.Now
)

func autoBackupFilename(now time.Time, attempt int) string {
	if attempt == 0 {
		return autoBackupFilenamePrefix + now.Format("2006-01-02") + ".tar.gz"
	}
	base := autoBackupFilenamePrefix + now.Format("2006-01-02-150405")
	if attempt == 1 {
		return base + ".tar.gz"
	}
	return fmt.Sprintf("%s-%d.tar.gz", base, attempt)
}

// publishAutoBackup atomically makes a fully-written temporary archive visible
// without ever replacing an existing backup. publishNewFile has OS-native
// create-if-absent semantics; both paths live in the same directory/filesystem.
// The hidden source is removed by the caller's defer after successful publish.
func publishAutoBackup(tempPath, backupDir string, now time.Time) (string, string, error) {
	const maxAttempts = 128
	for attempt := range maxAttempts {
		filename := autoBackupFilename(now, attempt)
		target := filepath.Join(backupDir, filename)
		if err := publishNewFile(tempPath, target); err == nil {
			return filename, target, nil
		} else if os.IsExist(err) {
			continue
		} else {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("could not allocate a unique auto-backup name after %d attempts", maxAttempts)
}

// pruneOldAutoBackupsImpl is the inner implementation that supports dry-run.
// When dryRun is true, returns the COUNTS that would be removed but does
// not touch disk — used by iter 930+ CleanupOldData's preview path so
// users see what's pending before confirming the cleanup.
//
// Sorts by mtime (newest first), keeps the first AutoBackupRetention,
// reports the rest. Per-file errors during deletion don't abort the sweep;
// callers that need the details use pruneOldAutoBackupsDetailed.
func pruneOldAutoBackupsImpl(dryRun bool) (removed int, freed int64) {
	removed, freed, _ = pruneOldAutoBackupsDetailed(dryRun)
	return removed, freed
}

// pruneOldAutoBackupsDetailed is the cleanup-facing form. The historical
// two-result wrappers intentionally remain stable for the auto-backup writer,
// while manual/background Cleanup can surface individual inspection/removal
// failures instead of silently reporting "nothing to remove".
func pruneOldAutoBackupsDetailed(dryRun bool) (removed int, freed int64, cleanupErrors []string) {
	autoBackupMu.Lock()
	defer autoBackupMu.Unlock()
	return pruneOldAutoBackupsDetailedLocked(dryRun)
}

func pruneOldAutoBackupsDetailedLocked(dryRun bool) (removed int, freed int64, cleanupErrors []string) {
	dir := autoBackupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			cleanupErrors = append(cleanupErrors, fmt.Sprintf("inspect auto-backups %s: %v", dir, err))
		}
		return 0, 0, cleanupErrors
	}

	type entry struct {
		path  string
		mtime time.Time
		size  int64
	}
	var files []entry
	tempCutoff := time.Now().Add(-autoBackupTempGrace)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, autoBackupTempPrefix) && strings.HasSuffix(name, autoBackupTempSuffix) {
			info, infoErr := e.Info()
			if infoErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Sprintf(
					"inspect incomplete auto-backup %s: %v", filepath.Join(dir, name), infoErr))
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				continue
			}
			// Another app process may still be finishing a temp file. The in-process
			// mutex excludes our own writer; an hour grace avoids touching a live
			// cross-process writer while still collecting crash leftovers.
			if info.ModTime().After(tempCutoff) {
				continue
			}
			if dryRun {
				removed++
				freed += info.Size()
				continue
			}
			path := filepath.Join(dir, name)
			if removeErr := autoBackupRemoveFile(path); removeErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Sprintf("remove incomplete auto-backup %s: %v", path, removeErr))
				continue
			}
			removed++
			freed += info.Size()
			continue
		}
		if validateAutoBackupFilename(name) != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Sprintf(
				"inspect auto-backup %s: %v", filepath.Join(dir, name), err))
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, entry{
			path:  filepath.Join(dir, name),
			mtime: info.ModTime(),
			size:  info.Size(),
		})
	}
	if len(files) <= AutoBackupRetention {
		return removed, freed, cleanupErrors
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
		if err := autoBackupRemoveFile(f.path); err == nil {
			removed++
			freed += f.size
		} else {
			cleanupErrors = append(cleanupErrors, fmt.Sprintf("remove auto-backup %s: %v", f.path, err))
		}
	}
	return removed, freed, cleanupErrors
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
	configDataMu.RLock()
	defer configDataMu.RUnlock()
	autoBackupMu.Lock()
	defer autoBackupMu.Unlock()

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

	backupTime := autoBackupNow()

	// Publish atomically. A hidden unique temp file is invisible to List,
	// retention, Delete and Restore; only a fully written + fsynced tarball gets
	// its final auto-backup-*.tar.gz name. This also leaves the previous backup
	// intact on disk-full, walk, close, or process-interruption failures.
	f, err := os.CreateTemp(backupDir, autoBackupTempPrefix+"*"+autoBackupTempSuffix)
	if err != nil {
		s.logf("warn", "backup", "auto-backup: cannot open target: %v", err)
		return nil, fmt.Errorf("open backup target: %w", err)
	}
	tempPath := f.Name()
	defer func() { _ = os.Remove(tempPath) }()
	fileClosed := false
	defer func() {
		if !fileClosed {
			_ = f.Close()
		}
	}()
	filesCount, walkErr := autoBackupArchiveWriter(f, cfgDir)
	syncErr := f.Sync()
	closeErr := f.Close()
	fileClosed = true
	if walkErr != nil {
		s.logf("warn", "backup", "auto-backup: walk failed: %v", walkErr)
		return nil, walkErr
	}
	if syncErr != nil {
		s.logf("warn", "backup", "auto-backup: sync failed: %v", syncErr)
		return nil, syncErr
	}
	if closeErr != nil {
		s.logf("warn", "backup", "auto-backup: close failed: %v", closeErr)
		return nil, closeErr
	}
	fname, target, err := publishAutoBackup(tempPath, backupDir, backupTime)
	if err != nil {
		s.logf("warn", "backup", "auto-backup: publish failed: %v", err)
		return nil, fmt.Errorf("publish backup: %w", err)
	}

	stat, _ := os.Stat(target)
	size := int64(0)
	if stat != nil {
		size = stat.Size()
	}
	touchAutoBackupSentinel()
	pruned, freed, pruneErrors := pruneOldAutoBackupsDetailedLocked(false)
	if len(pruneErrors) > 0 {
		s.logf("warn", "backup", "auto-backup retention completed with %d warning(s): %s", len(pruneErrors), pruneErrors[0])
	}

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
	configDataMu.RLock()
	defer configDataMu.RUnlock()

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
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := e.Name()
		if validateAutoBackupFilename(name) != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
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
	configDataMu.RLock()
	defer configDataMu.RUnlock()
	autoBackupMu.Lock()
	defer autoBackupMu.Unlock()
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
	if !info.Mode().IsRegular() {
		return errors.New("backup path is not a regular file")
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
	configDataMu.Lock()
	defer configDataMu.Unlock()
	autoBackupMu.Lock()
	defer autoBackupMu.Unlock()
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
	if !info.Mode().IsRegular() {
		return nil, errors.New("backup path is not a regular file")
	}
	if info.Size() > ImportArchiveMaxBytes {
		return nil, fmt.Errorf("backup too large (%d bytes, max %d)", info.Size(), ImportArchiveMaxBytes)
	}
	f, err := autoBackupOpenFile(full)
	if err != nil {
		return nil, fmt.Errorf("cannot open backup: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("cannot verify opened backup: %w", err)
	}
	if !opened.Mode().IsRegular() || !sameOpenedFile(info, opened) {
		return nil, errors.New("backup changed while opening")
	}
	if opened.Size() > ImportArchiveMaxBytes {
		return nil, fmt.Errorf("backup too large (%d bytes, max %d)", opened.Size(), ImportArchiveMaxBytes)
	}
	// iter 860+: pass preRestorePrefix so the safety snapshot is named
	// .gokin-studio.pre-restore-* (semantically correct — restore created
	// it, not import) instead of mislabelled .gokin-studio.pre-import-*.
	// Both prefixes register in snapshotPrefixes (iter 830+), so the safety
	// snapshot still shows up in ListPreImportBackups + auto-cleanup either
	// way; this just stops the audit log and listing tooltips from lying.
	// Pin the readable extent to the verified open-file size. A concurrent
	// append to the same inode cannot turn a validated backup into an unbounded
	// compressed input after the check above.
	reader := io.NewSectionReader(f, 0, opened.Size())
	result, err := s.extractArchiveToConfigDir(reader, "restore", preRestorePrefix)
	if err != nil {
		return nil, err
	}
	s.logf("info", "backup", "restored from auto-backup %q (safety backup at %s)", filename, result.PreBackupPath)
	return result, nil
}
