package studio

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CleanupResult summarises a CleanupOldData run. With DryRun=true, counts and
// BytesFreed estimate what would be removed. In a real run they include only
// successful operations; failures remain on disk and are listed in Errors.
// The sweep continues after individual failures.
type CleanupResult struct {
	StaleReplaysRemoved  int `json:"staleReplaysRemoved"`
	PreImportDirsRemoved int `json:"preImportDirsRemoved"`
	StagingDirsRemoved   int `json:"stagingDirsRemoved"`
	// iter 930+: excess auto-backup files beyond AutoBackupRetention (7).
	// Enforced here regardless of Settings.AutoBackupEnabled so disabling
	// auto-backup after accumulating 7 backups still gets retention
	// honoured during the next manual cleanup or auto-cleanup pass.
	AutoBackupsRemoved int `json:"autoBackupsRemoved"`
	// Old terminal delegation records own child chats. Protected children
	// (active, pinned, drafted, changed, used after completion, or owned by an
	// archived project) are counted separately and retain their durable record.
	DelegationRunsRemoved int      `json:"delegationRunsRemoved"`
	DelegationRunsSkipped int      `json:"delegationRunsSkipped"`
	BytesFreed            int64    `json:"bytesFreed"`
	DryRun                bool     `json:"dryRun"`
	Errors                []string `json:"errors,omitempty"`
}

// stagingGraceWindow is how recently a .gokin-studio.import-staging-* dir must
// have been touched to be treated as a possible in-progress import (and thus
// skipped by cleanup). An import completes in seconds; an hour is a generous
// margin that still reaps genuinely-orphaned staging dirs on the next pass.
const stagingGraceWindow = time.Hour

// Test seams for failure-path accounting. Production always uses the os
// implementations; keeping the calls injectable lets regression tests prove
// that a failed delete is reported but never counted as completed work.
var (
	cleanupRemoveFile = os.Remove
	cleanupRemoveTree = os.RemoveAll
	// A manual cleanup, preview, and startup auto-cleanup all traverse the same
	// global config tree and delegation store. Serialize complete sweeps so two
	// callers cannot both select one path and turn the second delete into a
	// misleading warning after the first succeeds.
	cleanupSweepMu   sync.Mutex
	autoCleanupRunMu sync.Mutex
)

// CleanupParams controls what CleanupOldData touches. A zero age disables its
// corresponding category; negative ages are rejected. Staging and excess
// auto-backup cleanup remain unconditional. DryRun previews without writes.
type CleanupParams struct {
	ReplayAgeDays     int  `json:"replayAgeDays"`     // delete *.replay.jsonl older than N days
	PreImportDays     int  `json:"preImportDays"`     // delete .gokin-studio.pre-import-* dirs older than N days
	DelegationAgeDays int  `json:"delegationAgeDays"` // delete safe terminal delegation chats older than N days
	DryRun            bool `json:"dryRun"`
}

// DefaultCleanupParams returns the recommended manual defaults: replays after
// 7 days, rollback snapshots and safely disposable terminal delegations after
// 30 days, plus orphaned staging dirs and excess auto-backups.
func DefaultCleanupParams() CleanupParams {
	return CleanupParams{
		ReplayAgeDays:     7,
		PreImportDays:     30,
		DelegationAgeDays: 30,
		DryRun:            false,
	}
}

// CleanupOldData removes stale app-owned data in five categories:
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
//  4. **Excess auto-backups** — snapshots beyond AutoBackupRetention.
//
//  5. **Old terminal delegations** — child chats and durable rows past their
//     configured age, but only after conservative user-work safety checks.
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
	if params.DelegationAgeDays < 0 {
		return nil, errors.New("delegationAgeDays must be >= 0")
	}
	cleanupSweepMu.Lock()
	defer cleanupSweepMu.Unlock()
	configDataMu.Lock()
	defer configDataMu.Unlock()
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
			if walkErr != nil {
				if !os.IsNotExist(walkErr) {
					result.Errors = append(result.Errors, fmt.Sprintf("inspect %s: %v", path, walkErr))
				}
				return nil
			}
			if d == nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".replay.jsonl") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("inspect %s: %v", path, err))
				return nil
			}
			if info.ModTime().After(cutoff) {
				return nil
			}
			if params.DryRun {
				result.BytesFreed += info.Size()
				result.StaleReplaysRemoved++
				return nil
			}
			if err := cleanupRemoveFile(path); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("remove %s: %v", path, err))
				return nil
			}
			result.BytesFreed += info.Size()
			result.StaleReplaysRemoved++
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
				result.Errors = append(result.Errors, fmt.Sprintf("inspect %s: %v", full, statErr))
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
				if params.PreImportDays == 0 {
					continue
				}
				cutoff := now.Add(-time.Duration(params.PreImportDays) * 24 * time.Hour)
				if snapshotRetentionTime(name, info.ModTime()).After(cutoff) {
					continue
				}
			}
			size := dirSize(full)
			countRemoved := func() {
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
			}
			if params.DryRun {
				countRemoved()
				continue
			}
			if err := cleanupRemoveTree(full); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("remove %s: %v", full, err))
				continue
			}
			countRemoved()
		}
	} else if !os.IsNotExist(err) {
		result.Errors = append(result.Errors, fmt.Sprintf("inspect cleanup parent %s: %v", parent, err))
	}

	// Category 4 (iter 930+): excess auto-backups beyond AutoBackupRetention.
	// Always enforced regardless of Settings.AutoBackupEnabled — otherwise a
	// user who enables auto-backup, accumulates 7 files, then disables it
	// leaves those 7 files frozen on disk forever (iter 840+ pruneOldAutoBackups
	// only ran when auto-backup itself fired).
	prunedAB, freedAB, backupErrors := pruneOldAutoBackupsDetailed(params.DryRun)
	result.AutoBackupsRemoved = prunedAB
	result.BytesFreed += freedAB
	result.Errors = append(result.Errors, backupErrors...)

	// Category 5: terminal cross-project delegations past their retention age.
	// The row is removed only after its child chat was deleted successfully (or
	// is already absent). A guarded delete holds the project lock across its
	// final safety check, so a new turn cannot race this background cleanup.
	if params.DelegationAgeDays > 0 {
		cutoff := now.Add(-time.Duration(params.DelegationAgeDays) * 24 * time.Hour).UnixMilli()
		candidates, listErr := listDelegationRunsOlderThan(cutoff)
		if listErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("inspect delegation history: %v", listErr))
		} else {
			removeIDs := make(map[string]struct{}, len(candidates))
			for _, run := range candidates {
				if params.DryRun {
					err := s.delegationCleanupProtection(run)
					if errors.Is(err, errDelegationCleanupProtected) {
						result.DelegationRunsSkipped++
						continue
					}
					if err != nil {
						result.Errors = append(result.Errors, fmt.Sprintf("inspect delegation %s: %v", run.ID, err))
						continue
					}
					removeIDs[run.ID] = struct{}{}
					continue
				}

				gone, protected, deleteErr := s.deleteDelegationSessionIfSafe(run)
				if protected {
					result.DelegationRunsSkipped++
					continue
				}
				if deleteErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("remove delegation %s child chat: %v", run.ID, deleteErr))
					continue
				}
				if gone {
					removeIDs[run.ID] = struct{}{}
				}
			}

			if params.DryRun {
				count, freed, estimateErr := estimateDelegationRunRemoval(removeIDs)
				if estimateErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("estimate delegation cleanup: %v", estimateErr))
				} else {
					result.DelegationRunsRemoved = count
					result.BytesFreed += freed
				}
			} else {
				removed, evicted, freed, removeErr := removeDelegationRunsByID(removeIDs)
				if removeErr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("update delegation history: %v", removeErr))
				} else {
					result.DelegationRunsRemoved = len(removed)
					result.BytesFreed += freed
					s.reapEvictedDelegationSessions(evicted)
				}
			}
		}
	}

	// Surface non-trivial actions to the event log so users can see what
	// happened in the Logs viewer.
	totalRemoved := result.removedCount()
	if !params.DryRun && totalRemoved > 0 {
		s.logf("info", "cleanup",
			"removed %d stale replay(s), %d pre-import backup(s), %d staging dir(s), %d excess auto-backup(s), %d old delegation(s) (freed %s; retained %d protected delegation(s))",
			result.StaleReplaysRemoved, result.PreImportDirsRemoved, result.StagingDirsRemoved, result.AutoBackupsRemoved, result.DelegationRunsRemoved,
			humanBytes(result.BytesFreed), result.DelegationRunsSkipped)
	}
	if !params.DryRun && len(result.Errors) > 0 {
		s.logf("warn", "cleanup", "cleanup completed with %d warning(s): %s", len(result.Errors), result.Errors[0])
	}
	return result, nil
}

var errDelegationCleanupProtected = errors.New("delegation chat is protected from automatic cleanup")

func delegationCleanupProtected(reason string) error {
	return fmt.Errorf("%w: %s", errDelegationCleanupProtected, reason)
}

// delegationCleanupProtection is the read-only half of the retention guard.
// A missing project/session means the row is already orphaned and can be
// removed. Existing chats are checked under the same project->session lock
// order used by interactive session operations.
func (s *Studio) delegationCleanupProtection(run DelegationRun) error {
	if run.ToProjectID == "" || run.ToSessionID == "" {
		return nil
	}
	s.mu.RLock()
	project := s.projects[run.ToProjectID]
	_, archived := s.archived[run.ToProjectID]
	s.mu.RUnlock()
	if archived {
		return delegationCleanupProtected("target project is archived")
	}
	if project == nil {
		return nil
	}
	project.mu.RLock()
	defer project.mu.RUnlock()
	session := project.sessions[run.ToSessionID]
	if session == nil {
		return nil
	}
	return s.delegationCleanupProtectionLocked(project, session, run)
}

// delegationCleanupProtectionLocked requires project.mu (read or write). It
// intentionally protects anything that looks user-owned or uncertain. The
// history mtime makes later manual use durable across restarts even though a
// session's in-memory lastUsedAt is reconstructed as zero.
func (s *Studio) delegationCleanupProtectionLocked(project *Project, session *ChatSession, run DelegationRun) error {
	if !delegationRunTerminal(run.Status) || run.CompletedAt <= 0 {
		return delegationCleanupProtected("delegation is not terminal")
	}
	if session.ID == "default" || session.ID != run.ToSessionID || project.ID != run.ToProjectID {
		return delegationCleanupProtected("child identity does not match the delegation record")
	}

	session.mu.RLock()
	owned := session.delegateChild || strings.HasPrefix(session.Name, "Delegation · ")
	active := session.active || session.queueWorker || len(session.queuedTurns) > 0
	pinned := session.Pinned
	archived := session.ArchivedAt > 0
	lastUsedAt := session.lastUsedAt
	session.mu.RUnlock()
	if !owned {
		return delegationCleanupProtected("chat was renamed or is not recognisable as a delegation child")
	}
	if active {
		return delegationCleanupProtected("chat is active or has queued work")
	}
	if pinned {
		return delegationCleanupProtected("chat is pinned")
	}
	if lastUsedAt > run.CompletedAt {
		return delegationCleanupProtected("chat was used after the delegation completed")
	}
	if !archived {
		activeSessions := 0
		for _, candidate := range project.sessions {
			candidate.mu.RLock()
			if candidate.ArchivedAt == 0 {
				activeSessions++
			}
			candidate.mu.RUnlock()
		}
		if activeSessions <= 1 {
			return delegationCleanupProtected("chat is the project's last active session")
		}
	}

	draft, err := s.GetDraft(project.ID, session.ID)
	if err != nil {
		return fmt.Errorf("inspect child draft: %w", err)
	}
	if strings.TrimSpace(draft) != "" {
		return delegationCleanupProtected("chat has an unsent draft")
	}
	if info, err := os.Stat(historyPath(projectSessionStorageKey(project.ID, session.ID))); err == nil {
		if info.ModTime().UnixMilli() > run.CompletedAt {
			return delegationCleanupProtected("chat history changed after the delegation completed")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect child history: %w", err)
	}
	worktree := sessionWorktreeStatus(session)
	if worktree.Error != "" {
		return delegationCleanupProtected("isolated worktree cannot be verified")
	}
	if worktree.Dirty {
		return delegationCleanupProtected("isolated worktree has uncommitted changes")
	}
	return nil
}

// deleteDelegationSessionIfSafe deletes a linked child chat with a final guard
// inside DeleteChatSession's project write lock. It returns gone=true when the
// project/session was already absent too, because then the durable row is stale.
func (s *Studio) deleteDelegationSessionIfSafe(run DelegationRun) (gone, protected bool, err error) {
	if run.ToProjectID == "" || run.ToSessionID == "" {
		return true, false, nil
	}
	exists, archived := s.delegationSessionState(run.ToProjectID, run.ToSessionID)
	if archived {
		return false, true, nil
	}
	if !exists {
		return true, false, nil
	}
	err = s.deleteChatSession(run.ToProjectID, run.ToSessionID, func(project *Project, session *ChatSession) error {
		return s.delegationCleanupProtectionLocked(project, session, run)
	})
	if err == nil {
		return true, false, nil
	}
	if errors.Is(err, errDelegationCleanupProtected) {
		return false, true, nil
	}
	// The project or child may have been removed between the optimistic lookup
	// and deleteChatSession acquiring its locks. In that case the row is stale,
	// not a cleanup failure.
	exists, archived = s.delegationSessionState(run.ToProjectID, run.ToSessionID)
	if archived {
		return false, true, nil
	}
	if !exists {
		return true, false, nil
	}
	return false, false, err
}

// delegationSessionState distinguishes an actually missing target from a
// project that is merely archived. ArchivedProjectRecord deliberately keeps
// every project-owned file on disk, so treating it as absent would orphan its
// child chat by dropping the only delegation row that links to it.
func (s *Studio) delegationSessionState(projectID, sessionID string) (exists, archived bool) {
	s.mu.RLock()
	project := s.projects[projectID]
	_, archived = s.archived[projectID]
	s.mu.RUnlock()
	if archived {
		return false, true
	}
	if project == nil {
		return false, false
	}
	project.mu.RLock()
	_, exists = project.sessions[sessionID]
	project.mu.RUnlock()
	return exists, false
}

func (r *CleanupResult) removedCount() int {
	if r == nil {
		return 0
	}
	return r.StaleReplaysRemoved + r.PreImportDirsRemoved + r.StagingDirsRemoved + r.AutoBackupsRemoved + r.DelegationRunsRemoved
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
		ReplayAgeDays:     30,
		PreImportDays:     90,
		DelegationAgeDays: 90,
		DryRun:            false,
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
	autoCleanupRunMu.Lock()
	defer autoCleanupRunMu.Unlock()

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
	if result.removedCount() == 0 {
		// Nothing to clean — log at info level so it's visible in the Logs
		// viewer but doesn't clutter the warn/error rails.
		s.logf("info", "cleanup", "auto-cleanup ran (nothing to remove)")
	}
	return nil
}
