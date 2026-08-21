package studio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// preImportPrefix names the sibling-of-configDir folders that
// ImportAllDataBase64 (iter 750+) creates before overwriting existing data.
const preImportPrefix = ".gokin-studio.pre-import-"

// preRestorePrefix names the sibling-of-configDir folders that
// RestorePreImportBackup (iter 810+) creates before promoting a backup
// to active config. Conceptually identical to pre-import — both are
// recoverable snapshots of previous state — so they share the management
// surface (list/delete/restore + auto-cleanup).
const preRestorePrefix = ".gokin-studio.pre-restore-"

// restoreClaimPrefix is still covered by preRestorePrefix, so a process crash
// after claiming a snapshot cannot strand it outside the normal backup list.
// The claim closes the check/rename pathname race in RestorePreImportBackup:
// validation and promotion operate on the same renamed directory, while a new
// object planted at the user-facing name remains unrelated.
const restoreClaimPrefix = preRestorePrefix + "claim-"

// snapshotPrefixes is the full list of folder-name prefixes that
// ListPreImportBackups, DeletePreImportBackup, RestorePreImportBackup, and
// the cleanup sweepers should recognise. Adding a new prefix here makes
// it visible AND prunable AND restorable in one place.
//
// NOTE on naming: the Wails-bound methods keep the "PreImport" name for
// backward-compat with the frontend, but they now operate on the union
// of all snapshot prefixes. Internal helpers use the generalised
// `validateBackupName` accordingly.
var snapshotPrefixes = []string{preImportPrefix, preRestorePrefix}

// archivePathNow is a narrow test seam for proving same-second operations do
// not collide. Production always uses time.Now.
var (
	archivePathNow  = time.Now
	configDirRename = os.Rename
)

// moveDirToUniqueSnapshot moves src to a collision-resistant snapshot name.
// MkdirTemp reserves an unpredictable sibling atomically; removing the empty
// placeholder immediately before Rename keeps the final on-disk shape backward
// compatible (config.yaml remains at the snapshot root). If another process
// wins the tiny handoff window, retry with a new random name.
func moveDirToUniqueSnapshot(src, parent, prefix string) (string, error) {
	const attempts = 8
	createdAt := archivePathNow()
	pattern := prefix + createdAt.Format("20060102-150405") + "-"
	for range attempts {
		reserved, err := os.MkdirTemp(parent, pattern)
		if err != nil {
			return "", err
		}
		if err := os.Remove(reserved); err != nil {
			return "", err
		}
		if err := configDirRename(src, reserved); err == nil {
			// Rename preserves the source directory's potentially old mtime, while
			// ListPreImportBackups uses it as CreatedAtMs and its sort key.
			// Touch best-effort only: after a successful move, reporting a failure
			// would strand the active config behind an apparent error.
			_ = os.Chtimes(reserved, createdAt, createdAt)
			return reserved, nil
		} else if _, collisionErr := os.Lstat(reserved); collisionErr == nil {
			continue
		} else {
			return "", err
		}
	}
	return "", errors.New("could not allocate a unique snapshot path")
}

// claimSnapshotDir atomically detaches one selected snapshot from its public
// name into a collision-resistant, still-discoverable sibling. It remembers
// the snapshot's age for rollback, while the on-disk claim gets a fresh mtime
// so a concurrent cleanup process cannot classify active restore work as old.
type snapshotClaim struct {
	path            string
	originalPath    string
	originalModTime time.Time
	originalMode    os.FileMode
	restoreModTime  bool
}

func claimSnapshotDir(src, parent string) (snapshotClaim, error) {
	const attempts = 8
	claimedAt := archivePathNow()
	pattern := restoreClaimPrefix + strconv.FormatInt(claimedAt.UnixMilli(), 10) + "-"
	for range attempts {
		claimed, err := os.MkdirTemp(parent, pattern)
		if err != nil {
			return snapshotClaim{}, err
		}
		if err := os.Remove(claimed); err != nil {
			return snapshotClaim{}, err
		}
		if err := configDirRename(src, claimed); err == nil {
			info, statErr := os.Lstat(claimed)
			if statErr != nil {
				claim := snapshotClaim{path: claimed, originalPath: src}
				return snapshotClaim{}, returnClaimedSnapshot(claim, fmt.Errorf("cannot inspect claimed backup: %w", statErr))
			}
			claim := snapshotClaim{
				path:            claimed,
				originalPath:    src,
				originalModTime: info.ModTime(),
				originalMode:    info.Mode().Perm(),
				restoreModTime:  info.Mode()&os.ModeSymlink == 0 && info.IsDir(),
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return claim, nil
			}
			// Cleanup uses directory mtime as snapshot age. Refresh it while the
			// claim is live so another Studio process cannot reap a freshly claimed
			// but historically old backup in the middle of validation/promotion.
			if err := os.Chtimes(claimed, claimedAt, claimedAt); err != nil {
				return snapshotClaim{}, returnClaimedSnapshot(claim, fmt.Errorf("cannot protect claimed backup from cleanup: %w", err))
			}
			return claim, nil
		} else if _, collisionErr := os.Lstat(claimed); collisionErr == nil {
			continue
		} else {
			return snapshotClaim{}, err
		}
	}
	return snapshotClaim{}, errors.New("could not allocate a unique restore claim path")
}

func snapshotRetentionTime(name string, modTime time.Time) time.Time {
	if !strings.HasPrefix(name, restoreClaimPrefix) {
		return modTime
	}
	remainder := strings.TrimPrefix(name, restoreClaimPrefix)
	separator := strings.IndexByte(remainder, '-')
	if separator <= 0 {
		return modTime
	}
	millis, err := strconv.ParseInt(remainder[:separator], 10, 64)
	if err != nil || millis <= 0 {
		return modTime
	}
	claimedAt := time.UnixMilli(millis)
	if claimedAt.After(modTime) {
		return claimedAt
	}
	return modTime
}

// returnClaimedSnapshot best-effort restores the original public backup name.
// Never hide the only known copy: if that name has been recreated or rollback
// fails, the error reports the exact claim path, which remains visible through
// ListPreImportBackups because it uses restoreClaimPrefix.
func returnClaimedSnapshot(claim snapshotClaim, cause error) error {
	if _, err := os.Lstat(claim.originalPath); err == nil {
		return fmt.Errorf("%w; backup name was recreated; selected backup remains at %s", cause, claim.path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("%w; cannot inspect original backup name: %v; selected backup remains at %s", cause, err, claim.path)
	}
	if err := configDirRename(claim.path, claim.originalPath); err != nil {
		return fmt.Errorf("%w; could not return selected backup to its original name: %v; selected backup remains at %s", cause, err, claim.path)
	}
	if claim.restoreModTime {
		if err := os.Chmod(claim.originalPath, claim.originalMode); err != nil {
			return fmt.Errorf("%w; selected backup returned to %s but its original permissions could not be restored: %v", cause, claim.originalPath, err)
		}
		if !claim.originalModTime.IsZero() {
			if err := os.Chtimes(claim.originalPath, claim.originalModTime, claim.originalModTime); err != nil {
				return fmt.Errorf("%w; selected backup returned to %s but its original timestamp could not be restored: %v", cause, claim.originalPath, err)
			}
		}
	}
	return cause
}

// promoteConfigDir installs replacement at liveDir and restores the safety
// snapshot if promotion fails. A failed rollback is surfaced with the exact
// recovery path instead of being silently discarded.
func promoteConfigDir(replacement, liveDir, safetyPath string) error {
	if err := configDirRename(replacement, liveDir); err != nil {
		if safetyPath == "" {
			return fmt.Errorf("promotion failed: %w", err)
		}
		if rollbackErr := configDirRename(safetyPath, liveDir); rollbackErr != nil {
			return fmt.Errorf("promotion failed: %w; rollback failed: %v; previous data remains at %s", err, rollbackErr, safetyPath)
		}
		return fmt.Errorf("promotion failed: %w (previous data restored)", err)
	}
	return nil
}

// hasSnapshotPrefix returns the matching prefix (or "") so a caller can
// log it / surface the snapshot kind. Used by listing + cleanup.
func hasSnapshotPrefix(name string) string {
	for _, p := range snapshotPrefixes {
		if strings.HasPrefix(name, p) {
			return p
		}
	}
	return ""
}

// PreImportBackup describes one rollback snapshot left behind by iter 750+
// Restore. Used by the frontend Settings UI to list, delete, and restore
// from past restore points without poking around the filesystem.
type PreImportBackup struct {
	Name        string `json:"name"`        // basename, e.g. ".gokin-studio.pre-import-20250516-153045"
	Path        string `json:"path"`        // absolute path (frontend may show as tooltip)
	CreatedAtMs int64  `json:"createdAtMs"` // mtime in ms
	Size        int64  `json:"size"`        // recursive byte total
}

// ListPreImportBackups returns all rollback snapshots found as siblings of
// the active config directory, newest first. Uses the SAME convention as
// ImportAllDataBase64 / CleanupOldData, so anything created by Restore or
// pruned by Auto-cleanup is consistent.
func (s *Studio) ListPreImportBackups() ([]PreImportBackup, error) {
	configDataMu.RLock()
	defer configDataMu.RUnlock()

	dir := configDir()
	parent := filepath.Dir(dir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return []PreImportBackup{}, nil
		}
		return nil, fmt.Errorf("cannot read parent dir: %w", err)
	}
	out := []PreImportBackup{}
	for _, e := range entries {
		name := e.Name()
		if hasSnapshotPrefix(name) == "" || !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		full := filepath.Join(parent, name)
		createdAt := snapshotRetentionTime(name, info.ModTime())
		out = append(out, PreImportBackup{
			Name:        name,
			Path:        full,
			CreatedAtMs: createdAt.UnixMilli(),
			Size:        dirSize(full),
		})
	}
	// Newest first — most-recent backup is most likely the one the user
	// just made and wants to roll back to.
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAtMs > out[j].CreatedAtMs
	})
	return out, nil
}

// validateBackupName rejects anything that isn't a well-formed sibling
// snapshot dir name. Accepts either pre-import OR pre-restore prefixes
// (see `snapshotPrefixes`). Defends DeletePreImportBackup/Restore from
// path traversal — the frontend passes a basename string, NOT a path,
// and we must not allow `..` / absolute paths / arbitrary basenames.
func validateBackupName(name string) error {
	if name == "" {
		return errors.New("backup name is empty")
	}
	if hasSnapshotPrefix(name) == "" {
		// Quote the most common prefix in the error so test fixtures and
		// users have a recognisable hint. Both prefixes are valid; we
		// don't enumerate them in the message to keep it readable.
		return fmt.Errorf("backup name must start with %q or %q", preImportPrefix, preRestorePrefix)
	}
	// filepath.Base strips any directory components — if a clever caller
	// passes "../etc" or "/etc/passwd", basename normalises it. We then
	// require the normalised form to equal the input — anything that
	// changed under Base is rejected.
	if filepath.Base(name) != name {
		return errors.New("backup name must be a plain basename (no path separators)")
	}
	// Disallow `..` even after Base, defensively.
	if strings.Contains(name, "..") {
		return errors.New("backup name must not contain ..")
	}
	return nil
}

// validatePreImportName is the original name kept as a thin alias for
// the existing tests that reference it. Prefer validateBackupName in new
// code — it's the same logic but covers all snapshot prefixes.
func validatePreImportName(name string) error {
	return validateBackupName(name)
}

// DeletePreImportBackup removes a specific pre-import snapshot by name.
// Identifies the target by basename — NOT by absolute path — so the
// frontend cannot trick the backend into deleting arbitrary directories.
func (s *Studio) DeletePreImportBackup(name string) error {
	if err := validateBackupName(name); err != nil {
		return err
	}
	configDataMu.Lock()
	defer configDataMu.Unlock()
	parent := filepath.Dir(configDir())
	target := filepath.Join(parent, name)
	// Confirm target really IS a sibling of configDir — protects against
	// the case where someone manipulates configDir() via env to make
	// `parent + name` land elsewhere mid-call. Stat first; reject symlinks.
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("backup not found")
		}
		return fmt.Errorf("cannot stat backup: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to follow symlink at backup path")
	}
	if !info.IsDir() {
		return errors.New("backup path is not a directory")
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("could not remove backup: %w", err)
	}
	s.logf("info", "backup", "deleted pre-import backup %q", name)
	return nil
}

// RestorePreImportBackup makes a specific pre-import snapshot the active
// config. Process:
//
//  1. Validate the name.
//  2. Move the CURRENT configDir aside as a new pre-restore safety backup
//     (so this restore is itself reversible).
//  3. Move the target backup into configDir's place.
//
// Atomic-ish via Rename — failure rolls back. Like ImportAllDataBase64,
// in-memory Studio state is NOT reloaded, so the user must restart for the
// restore to take full effect.
func (s *Studio) RestorePreImportBackup(name string) (*ImportResult, error) {
	if err := validateBackupName(name); err != nil {
		return nil, err
	}
	configDataMu.Lock()
	defer configDataMu.Unlock()
	dir := configDir()
	parent := filepath.Dir(dir)
	target := filepath.Join(parent, name)

	claim, err := claimSnapshotDir(target, parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("backup not found")
		}
		return nil, fmt.Errorf("cannot claim backup for restore: %w", err)
	}
	returnClaim := func(cause error) error {
		return returnClaimedSnapshot(claim, cause)
	}

	info, err := os.Lstat(claim.path)
	if err != nil {
		return nil, returnClaim(fmt.Errorf("cannot stat claimed backup: %w", err))
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, returnClaim(errors.New("refusing to follow symlink at backup path"))
	}
	if !info.IsDir() {
		return nil, returnClaim(errors.New("backup path is not a directory"))
	}
	if err := validateStudioConfigFile(filepath.Join(claim.path, "config.yaml")); err != nil {
		return nil, returnClaim(fmt.Errorf("backup has invalid config.yaml: %w", err))
	}
	// The snapshot may predate the 0700 config-root invariant or have been
	// copied in manually with broader permissions. Harden it before promotion
	// so active secrets are never exposed even transiently after the rename.
	if err := os.Chmod(claim.path, 0o700); err != nil {
		return nil, returnClaim(fmt.Errorf("cannot secure backup before restore: %w", err))
	}

	// Move current config aside as a pre-restore safety backup.
	preRestorePath := ""
	if _, err := os.Stat(dir); err == nil {
		preRestorePath, err = moveDirToUniqueSnapshot(dir, parent, preRestorePrefix)
		if err != nil {
			return nil, returnClaim(fmt.Errorf("could not move current config aside: %w", err))
		}
	}

	// Promote the exact claimed directory, not the reusable public pathname.
	if err := promoteConfigDir(claim.path, dir, preRestorePath); err != nil {
		return nil, returnClaim(fmt.Errorf("could not promote backup to active: %w", err))
	}

	s.logf("info", "backup", "restored from pre-import backup %q (safety backup at %s)", name, preRestorePath)

	// Count files for the result so the UI can show "restored N files".
	filesCount := 0
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil || d.IsDir() {
			return nil
		}
		filesCount++
		return nil
	})
	return &ImportResult{
		FilesImported:   filesCount,
		PreBackupPath:   preRestorePath,
		RestartRequired: true,
	}, nil
}
