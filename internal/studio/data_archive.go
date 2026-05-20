package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BackupResult is the response from ExportAllDataBase64. The Base64 field
// holds gzip-compressed tar content of the user's config directory,
// suitable for downloading from the frontend as a file. Filename includes
// a date stamp; the frontend uses it as the default name in the Save dialog.
type BackupResult struct {
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	FilesCount int    `json:"filesCount"`
	Base64     string `json:"base64"`
}

// ImportResult is the response from ImportAllDataBase64. PreBackupPath is
// where we stashed the existing config directory before overwriting (so a
// user can roll back manually if something went wrong). FilesImported is
// what we wrote from the archive. RestartRequired tells the UI to nudge the
// user to restart — in-memory state in `Studio` does not auto-reload.
type ImportResult struct {
	FilesImported   int    `json:"filesImported"`
	PreBackupPath   string `json:"preBackupPath"`
	RestartRequired bool   `json:"restartRequired"`
}

// ImportArchiveMaxBytes caps the decoded archive size. 200 MB is well past
// what any realistic config directory should hold (typical < 5 MB), and
// guards against an over-decompressed malicious archive.
const ImportArchiveMaxBytes = 200 * 1024 * 1024

// archiveSkipNames are file basenames inside the config dir that are
// transient / device-specific and shouldn't be carried across machines.
// .gokin-write-probe is the sentinel from diagnostics writability checks.
var archiveSkipNames = map[string]bool{
	".gokin-write-probe": true,
	".DS_Store":          true,
}

// ExportAllDataBase64 walks the studio config directory, builds a gzip'd
// tar of every file under it (except the skip list), base64-encodes the
// result, and returns it to the frontend for download.
//
// We base64-encode (rather than returning raw []byte) because Wails'
// JS<->Go bridge handles strings well; large byte arrays can hit edge
// cases on some platforms. The base64 overhead (~33%) is acceptable for
// config-dir-sized payloads (typically < 10 MB encoded).
func (s *Studio) ExportAllDataBase64() (*BackupResult, error) {
	dir := configDir()
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("nothing to back up — config directory does not exist yet")
		}
		return nil, fmt.Errorf("cannot read config directory: %w", err)
	}

	var buf bytes.Buffer
	filesCount, err := writeConfigArchive(&buf, dir)
	if err != nil {
		return nil, err
	}

	size := int64(buf.Len())
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	filename := fmt.Sprintf("gokin-studio-backup-%s.tar.gz", time.Now().Format("2006-01-02"))

	return &BackupResult{
		Filename:   filename,
		Size:       size,
		FilesCount: filesCount,
		Base64:     encoded,
	}, nil
}

// writeConfigArchive gzip's + tar's every file under `dir` (recursively) to
// the given writer, skipping the iter 750+ skip list AND the iter 840+
// `backups/` subdir (where auto-backups land — recursive backup-of-backups
// would balloon archives). Returns the number of regular files included.
//
// Shared between iter 750+ ExportAllDataBase64 (writes to bytes.Buffer for
// base64 download) and iter 840+ RunAutoBackupIfDue (writes directly to a
// file). Walk semantics are unchanged from iter 750+; just hoisted into a
// helper.
func writeConfigArchive(out io.Writer, dir string) (int, error) {
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	filesCount := 0

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable
		}
		if path == dir {
			return nil // skip the root itself
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		// iter 840+: don't walk into the auto-backup directory. Backup files
		// are themselves backups; including them would make every Export
		// grow by the size of previous auto-backups.
		if rel == AutoBackupDirName || strings.HasPrefix(rel, AutoBackupDirName+string(filepath.Separator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if archiveSkipNames[filepath.Base(rel)] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		hdr := &tar.Header{
			Name:    filepath.ToSlash(rel),
			Mode:    int64(info.Mode().Perm()),
			ModTime: info.ModTime(),
		}
		if d.IsDir() {
			hdr.Typeflag = tar.TypeDir
			hdr.Name += "/"
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			return nil
		}
		hdr.Typeflag = tar.TypeReg
		// iter 980+: open BEFORE writing the header so a failed open
		// (permission revoked, file deleted between WalkDir and now) skips
		// the entry entirely. Previously the header was written first,
		// then os.Open failure returned nil — leaving an orphan header
		// promising N bytes with 0 bytes following. The next entry's bytes
		// were then misinterpreted as this entry's content → corrupt
		// archive, unreadable on restore.
		//
		// Also: use fstat on the open fd instead of WalkDir's `info` so
		// the header Size matches the bytes we actually have. If the file
		// shrank between WalkDir and Open (log rotation, DB checkpoint),
		// info.Size() would over-promise and io.Copy would under-deliver
		// → same corruption.
		f, err := os.Open(path)
		if err != nil {
			// Common case: file deleted between WalkDir and Open (cache
			// dir churn, temp files). Skip cleanly — the archive stays
			// valid, the caller's filesCount accurately reflects what
			// landed inside.
			return nil
		}
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil
		}
		hdr.Size = fi.Size()
		hdr.ModTime = fi.ModTime()
		if err := tw.WriteHeader(hdr); err != nil {
			_ = f.Close()
			return err
		}
		copied, copyErr := io.Copy(tw, f)
		_ = f.Close()
		if copyErr != nil {
			return copyErr
		}
		// io.Copy can return fewer bytes than hdr.Size if the file was
		// truncated mid-copy. tar.Writer.Close would later reject the
		// archive with "unexpected EOF on file". Surface as a real error
		// so the export is aborted with a clean message instead of
		// producing a half-written corrupt file the user will only learn
		// is broken when they try to restore.
		if copied != hdr.Size {
			return fmt.Errorf("file %q changed during archive (header size %d, copied %d)", rel, hdr.Size, copied)
		}
		filesCount++
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("archive walk failed: %w", err)
	}
	if err := tw.Close(); err != nil {
		return 0, fmt.Errorf("tar finalize failed: %w", err)
	}
	if err := gz.Close(); err != nil {
		return 0, fmt.Errorf("gzip finalize failed: %w", err)
	}
	return filesCount, nil
}

// ImportAllDataBase64 decodes a previously-exported archive, validates it,
// stashes the existing config directory next to itself as a pre-backup,
// then unpacks the archive over the original location. Returns an
// ImportResult with the pre-backup path so the user can roll back if needed.
//
// IMPORTANT: in-memory state in Studio is NOT reloaded by this call. The
// user must restart the app for the import to take full effect. We
// communicate this via RestartRequired: true in the response and log an
// info event.
func (s *Studio) ImportAllDataBase64(base64Data string) (*ImportResult, error) {
	if strings.TrimSpace(base64Data) == "" {
		return nil, errors.New("import payload is empty")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(base64Data))
	if err != nil {
		return nil, fmt.Errorf("invalid base64: %w", err)
	}
	if int64(len(raw)) > ImportArchiveMaxBytes {
		return nil, fmt.Errorf("archive too large (%d bytes, max %d)", len(raw), ImportArchiveMaxBytes)
	}
	return s.extractArchiveToConfigDir(bytes.NewReader(raw), "import", preImportPrefix)
}

// extractArchiveToConfigDir reads a gzip+tar archive from r and atomically
// swaps it over configDir(). On success, the previous configDir is moved
// aside as a safety backup so the operation is reversible. Returns
// ImportResult on success.
//
// Shared between iter 750+ ImportAllDataBase64 (decoded base64 reader) and
// iter 850+ RestoreAutoBackup (os.Open(file) reader). Three params:
//
//   - r: gzip+tar source
//   - kind: cosmetic log label, "import" or "restore"
//   - safetyPrefix: snapshot-dir prefix used when moving the CURRENT
//     configDir aside. Must be one of the values registered in
//     `snapshotPrefixes` (iter 830+) so the resulting dir shows up in
//     ListPreImportBackups + gets included in iter 770+/790+ cleanup.
//     Iter 750+ Import uses `preImportPrefix`; iter 850+ RestoreAutoBackup
//     uses `preRestorePrefix` — the iter 860+ fix that closes the
//     "auto-backup restore creates a pre-import safety dir" mislabel.
func (s *Studio) extractArchiveToConfigDir(r io.Reader, kind, safetyPrefix string) (*ImportResult, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("not a gzip stream: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	dir := configDir()
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("cannot create parent: %w", err)
	}
	stamp := time.Now().Format("20060102-150405")
	stagingDir := filepath.Join(parent, ".gokin-studio.import-staging-"+stamp)
	if err := os.RemoveAll(stagingDir); err != nil {
		return nil, fmt.Errorf("cannot clear staging dir: %w", err)
	}
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("cannot create staging dir: %w", err)
	}

	// Extract.
	filesImported := 0
	hasConfigYAML := false
	totalBytes := int64(0)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = os.RemoveAll(stagingDir)
			return nil, fmt.Errorf("tar read failed: %w", err)
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			_ = os.RemoveAll(stagingDir)
			return nil, fmt.Errorf("archive contains unsafe path: %s", hdr.Name)
		}
		target := filepath.Join(stagingDir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				_ = os.RemoveAll(stagingDir)
				return nil, fmt.Errorf("mkdir failed: %w", err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				_ = os.RemoveAll(stagingDir)
				return nil, fmt.Errorf("mkdir failed: %w", err)
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err != nil {
				_ = os.RemoveAll(stagingDir)
				return nil, fmt.Errorf("open file failed: %w", err)
			}
			lim := io.LimitReader(tr, ImportArchiveMaxBytes-totalBytes+1)
			n, err := io.Copy(f, lim)
			_ = f.Close()
			if err != nil {
				_ = os.RemoveAll(stagingDir)
				return nil, fmt.Errorf("write file failed: %w", err)
			}
			totalBytes += n
			if totalBytes > ImportArchiveMaxBytes {
				_ = os.RemoveAll(stagingDir)
				return nil, fmt.Errorf("archive contents exceed %d bytes", ImportArchiveMaxBytes)
			}
			if clean == "config.yaml" {
				hasConfigYAML = true
			}
			filesImported++
		default:
			// Skip symlinks/hardlinks/etc.
		}
	}

	if !hasConfigYAML {
		_ = os.RemoveAll(stagingDir)
		return nil, errors.New("archive missing config.yaml — not a Gokin Studio backup")
	}

	preBackupPath := ""
	if _, err := os.Stat(dir); err == nil {
		preBackupPath = filepath.Join(parent, safetyPrefix+stamp)
		if err := os.Rename(dir, preBackupPath); err != nil {
			_ = os.RemoveAll(stagingDir)
			return nil, fmt.Errorf("could not move existing config aside: %w", err)
		}
	}

	if err := os.Rename(stagingDir, dir); err != nil {
		if preBackupPath != "" {
			_ = os.Rename(preBackupPath, dir)
		}
		return nil, fmt.Errorf("could not swap in imported data: %w", err)
	}

	s.logf("info", kind, "%s %d files from archive (pre-backup at %s)", kind+"ed", filesImported, preBackupPath)

	return &ImportResult{
		FilesImported:   filesImported,
		PreBackupPath:   preBackupPath,
		RestartRequired: true,
	}, nil
}
