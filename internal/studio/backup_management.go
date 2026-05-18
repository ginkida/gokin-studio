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

// preImportPrefix names the sibling-of-configDir folders that
// ImportAllDataBase64 (iter 750+) creates before overwriting existing data.
const preImportPrefix = ".gokin-studio.pre-import-"

// preRestorePrefix names the sibling-of-configDir folders that
// RestorePreImportBackup (iter 810+) creates before promoting a backup
// to active config. Conceptually identical to pre-import — both are
// recoverable snapshots of previous state — so they share the management
// surface (list/delete/restore + auto-cleanup).
const preRestorePrefix = ".gokin-studio.pre-restore-"

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
		out = append(out, PreImportBackup{
			Name:        name,
			Path:        full,
			CreatedAtMs: info.ModTime().UnixMilli(),
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
	dir := configDir()
	parent := filepath.Dir(dir)
	target := filepath.Join(parent, name)

	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("backup not found")
		}
		return nil, fmt.Errorf("cannot stat backup: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("refusing to follow symlink at backup path")
	}
	if !info.IsDir() {
		return nil, errors.New("backup path is not a directory")
	}

	stamp := time.Now().Format("20060102-150405")

	// Move current config aside as a pre-restore safety backup.
	preRestorePath := ""
	if _, err := os.Stat(dir); err == nil {
		preRestorePath = filepath.Join(parent, ".gokin-studio.pre-restore-"+stamp)
		if err := os.Rename(dir, preRestorePath); err != nil {
			return nil, fmt.Errorf("could not move current config aside: %w", err)
		}
	}

	// Promote the target backup into the configDir slot.
	if err := os.Rename(target, dir); err != nil {
		// Try to roll back the safety move.
		if preRestorePath != "" {
			_ = os.Rename(preRestorePath, dir)
		}
		return nil, fmt.Errorf("could not promote backup to active: %w", err)
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
