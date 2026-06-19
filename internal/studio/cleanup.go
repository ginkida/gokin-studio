package studio

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CleanupResult summarises a CleanupOldData run. DryRun=true means nothing
// was deleted on disk; the counts and BytesFreed reflect what WOULD have
// been removed if dry_run were false. Errors lists per-file failures
// (continues on most errors so a single permission issue doesn't stop the
// whole sweep).
type CleanupResult struct {
	StaleReplaysRemoved  int `json:"staleReplaysRemoved"`
	PreImportDirsRemoved int `json:"preImportDirsRemoved"`
	StagingDirsRemoved   int `json:"stagingDirsRemoved"`
	// iter 930+: excess auto-backup files beyond AutoBackupRetention (7).
	// Enforced here regardless of Settings.AutoBackupEnabled so disabling
	// auto-backup after accumulating 7 backups still gets retention
	// honoured during the next manual cleanup or auto-cleanup pass.
	AutoBackupsRemoved int      `json:"autoBackupsRemoved"`
	BytesFreed         int64    `json:"bytesFreed"`
	DryRun             bool     `json:"dryRun"`
	Errors             []string `json:"errors,omitempty"`
}

// CleanupParams controls what CleanupOldData touches. Zero/negative ages
// stagingGraceWindow is how recently a .gokin-studio.import-staging-* dir must
// have been touched to be treated as a possible in-progress import (and thus
// skipped by cleanup). An import completes in seconds; an hour is a generous
// margin that still reaps genuinely-orphaned staging dirs on the next pass.
const stagingGraceWindow = time.Hour

// disable the corresponding category (e.g. ReplayAgeDays=0 means skip
// replays entirely). DryRun=true previews without modifying anything.
type CleanupParams struct {
	ReplayAgeDays int  `json:"replayAgeDays"` // delete *.replay.jsonl older than N days
	PreImportDays int  `json:"preImportDays"` // delete .gokin-studio.pre-import-* dirs older than N days
	DryRun        bool `json:"dryRun"`
}

// DefaultCleanupParams returns the recommended defaults: replays after 7
// days, pre-import backups after 30 days, plus any orphaned staging dirs.
// Suitable for the "one-click cleanup" UI flow.
func DefaultCleanupParams() CleanupParams {
	return CleanupParams{
		ReplayAgeDays: 7,
		PreImportDays: 30,
		DryRun:        false,
	}
}

// CleanupOldData removes stale debugging artefacts. Two categories:
//
//  1. **Stale replay logs** — per-session crash-recovery JSONL files in
//     <configDir>/history/ that didn't get cleaned on chat:complete because
//     the app crashed or was force-quit. Diagnostics warns about these as
//     "stale replays" but the user had no way to clean them.
//
//  2. **Pre-import backups** — sibling directories of the config dir named
//     `.gokin-studio.pre-import-<timestamp>` created by iter 750+
//     ImportAllDataBase64 when overwriting existing data. Useful as
//     rollback for ~a week or two; after a month they're almost always
//     just wasted disk.
//
//  3. **Orphaned staging dirs** — `.gokin-studio.import-staging-*` left
//     behind if Import crashed mid-extract. These are always safe to delete.
//
// DryRun=true gives a preview without disk writes. Use the result counts
// to confirm with the user before re-running with DryRun=false.
//
// Best-effort: per-file errors are appended to Errors but don't abort the
// sweep. Returns a non-nil error only for catastrophic problems (e.g. can't
// stat the config dir at all).
func (s *Studio) CleanupOldData(params CleanupParams) (*CleanupResult, error) {
	if params.ReplayAgeDays < 0 {
		return nil, errors.New("replayAgeDays must be >= 0")
	}
	if params.PreImportDays < 0 {
		return nil, errors.New("preImportDays must be >= 0")
	}
	dir := configDir()
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return &CleanupResult{DryRun: params.DryRun}, nil
		}
		return nil, fmt.Errorf("cannot read config dir: %w", err)
	}

	result := &CleanupResult{DryRun: params.DryRun}
	now := time.Now()

	// Category 1: stale replays.
	if params.ReplayAgeDays > 0 {
		histDir := filepath.Join(dir, "history")
		cutoff := now.Add(-time.Duration(params.ReplayAgeDays) * 24 * time.Hour)
		_ = filepath.WalkDir(histDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d == nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".replay.jsonl") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if info.ModTime().After(cutoff) {
				return nil
			}
			result.BytesFreed += info.Size()
			result.StaleReplaysRemoved++
			if !params.DryRun {
				if err := os.Remove(path); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("remove %s: %v", path, err))
				}
			}
			return nil
		})
	}

	// Category 2 + 3: pre-import / pre-restore backup dirs and orphaned
	// staging dirs. These sit as siblings of configDir() (not inside it),
	// so we list the parent and filter by the well-known prefixes from
	// `snapshotPrefixes` (iter 830+ generalisation).
	parent := filepath.Dir(dir)
	entries, err := os.ReadDir(parent)
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			snapPrefix := hasSnapshotPrefix(name) // matches pre-import OR pre-restore
			isSnapshot := snapPrefix != ""
			isStaging := strings.HasPrefix(name, ".gokin-studio.import-staging-")
			if !isSnapshot && !isStaging {
				continue
			}
			if !e.IsDir() {
				continue
			}
			full := filepath.Join(parent, name)
			info, statErr := e.Info()
			if statErr != nil {
				continue
			}
			// Snapshot dirs: gate by PreImportDays. Staging dirs are orphans
			// once an import finishes — BUT an import in progress holds a
			// freshly-created staging dir as its live extract target. Apply a
			// short grace window so a manual Cleanup racing an active import
			// can't RemoveAll the dir mid-extract (which would fail/corrupt the
			// import). An import takes seconds; anything older than the window is
			// safely an orphan, and the next cleanup will still reap it.
			if isStaging {
				if info.ModTime().After(now.Add(-stagingGraceWindow)) {
					continue // likely an in-progress import — leave it alone
				}
			} else { // isSnapshot
				cutoff := now.Add(-time.Duration(params.PreImportDays) * 24 * time.Hour)
				if info.ModTime().After(cutoff) {
					continue
				}
			}
			size := dirSize(full)
			result.BytesFreed += size
			if isSnapshot {
				// Both pre-import and pre-restore counted under the same
				// "PreImportDirsRemoved" total — the result is shown as
				// "rollback snapshots" in the UI; renaming the field
				// would break the existing Wails JSON shape, so we keep
				// the name and unify the semantics.
				result.PreImportDirsRemoved++
			} else {
				result.StagingDirsRemoved++
			}
			if !params.DryRun {
				if err := os.RemoveAll(full); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("remove %s: %v", full, err))
				}
			}
		}
	}

	// Category 4 (iter 930+): excess auto-backups beyond AutoBackupRetention.
	// Always enforced regardless of Settings.AutoBackupEnabled — otherwise a
	// user who enables auto-backup, accumulates 7 files, then disables it
	// leaves those 7 files frozen on disk forever (iter 840+ pruneOldAutoBackups
	// only ran when auto-backup itself fired).
	prunedAB, freedAB := pruneOldAutoBackupsImpl(params.DryRun)
	result.AutoBackupsRemoved = prunedAB
	result.BytesFreed += freedAB

	// Surface non-trivial actions to the event log so users can see what
	// happened in the Logs viewer.
	totalRemoved := result.StaleReplaysRemoved + result.PreImportDirsRemoved + result.StagingDirsRemoved + result.AutoBackupsRemoved
	if !params.DryRun && totalRemoved > 0 {
		s.logf("info", "cleanup",
			"removed %d stale replay(s), %d pre-import backup(s), %d staging dir(s), %d excess auto-backup(s) (freed %s)",
			result.StaleReplaysRemoved, result.PreImportDirsRemoved, result.StagingDirsRemoved, result.AutoBackupsRemoved,
			humanBytes(result.BytesFreed))
	}
	return result, nil
}

// CleanupPreviewDefaults runs CleanupOldData with the default params in
// DryRun mode. Cheap "what would we delete?" query for the UI to call
// before showing the confirmation prompt.
func (s *Studio) CleanupPreviewDefaults() (*CleanupResult, error) {
	p := DefaultCleanupParams()
	p.DryRun = true
	return s.CleanupOldData(p)
}

// --- iter 790+: background auto-cleanup on startup ---

// AutoCleanupParams returns conservative cleanup parameters used by the
// once-per-24h background pass on startup. Much longer thresholds than the
// manual cleanup defaults (7/30 days) so users who occasionally rely on
// pre-import backups for rollback don't get them silently deleted.
func AutoCleanupParams() CleanupParams {
	return CleanupParams{
		ReplayAgeDays: 30,
		PreImportDays: 90,
		DryRun:        false,
	}
}

// autoCleanupSentinelPath returns the path to the file whose mtime tracks
// when auto-cleanup last ran. Stored at the root of the config dir so it's
// included in backups but doesn't pollute any subtree.
func autoCleanupSentinelPath() string {
	return filepath.Join(configDir(), ".last-auto-cleanup")
}

// autoCleanupThrottleHours gates how often the background cleanup runs.
// 24h is enough to amortise the file walk cost without letting accumulation
// drift beyond a day's worth of churn.
const autoCleanupThrottleHours = 24

// shouldRunAutoCleanup returns true if auto-cleanup should run now: either
// the sentinel file doesn't exist (first run) or its mtime is older than
// the throttle window. Stat errors other than "not exist" return true too —
// fail-open so a stat hiccup doesn't permanently skip cleanup.
func shouldRunAutoCleanup() bool {
	info, err := os.Stat(autoCleanupSentinelPath())
	if err != nil {
		return true // fresh install or stat failure
	}
	return time.Since(info.ModTime()) > autoCleanupThrottleHours*time.Hour
}

// touchAutoCleanupSentinel updates (or creates) the sentinel file so the
// next call to shouldRunAutoCleanup correctly sees the recent run. Best
// effort — disk failures are swallowed since this is a throttle hint, not
// data we lose on failure.
func touchAutoCleanupSentinel() {
	path := autoCleanupSentinelPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	// Try to touch mtime via Chtimes if the file exists.
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return
	}
	// Otherwise create it.
	if f, err := os.Create(path); err == nil {
		_ = f.Close()
	}
}

// RunAutoCleanupIfDue runs the conservative background cleanup pass when:
//   - the user hasn't disabled it via Settings.AutoCleanupDisabled, AND
//   - the throttle window has elapsed since the last run (or no sentinel).
//
// Designed to be called once at startup, from a goroutine, so even a slow
// file walk on a giant config dir doesn't block UI bring-up. Returns nil
// when skipped (disabled or not due) or after a successful run; an error
// only propagates if CleanupOldData fails catastrophically.
func (s *Studio) RunAutoCleanupIfDue() error {
	s.mu.RLock()
	disabled := s.config != nil && s.config.Settings.AutoCleanupDisabled
	s.mu.RUnlock()
	if disabled {
		return nil
	}
	if !shouldRunAutoCleanup() {
		return nil
	}
	result, err := s.CleanupOldData(AutoCleanupParams())
	if err != nil {
		s.logf("warn", "cleanup", "auto-cleanup failed: %v", err)
		return err
	}
	touchAutoCleanupSentinel()
	if result.StaleReplaysRemoved+result.PreImportDirsRemoved+result.StagingDirsRemoved == 0 {
		// Nothing to clean — log at info level so it's visible in the Logs
		// viewer but doesn't clutter the warn/error rails.
		s.logf("info", "cleanup", "auto-cleanup ran (nothing to remove)")
	}
	return nil
}
