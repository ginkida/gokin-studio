package studio

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
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

// NativeBackupResult describes a manual backup written directly by the Go
// process. Unlike BackupResult, it never carries archive bytes across the
// WebView bridge. Canceled is a normal outcome of dismissing the save dialog.
type NativeBackupResult struct {
	Filename   string `json:"filename"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	FilesCount int    `json:"filesCount"`
	Canceled   bool   `json:"canceled"`
}

// NativeRestoreReview is safe metadata for the explicit confirmation UI.
// Token addresses an immutable private staged copy, never the user-selected
// path, so confirmation cannot race an on-disk replacement of that path.
type NativeRestoreReview struct {
	Token    string `json:"token"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Canceled bool   `json:"canceled"`
}

type nativeRestoreCandidate struct {
	token    string
	filename string
	size     int64
	file     *os.File
	path     string // empty when an open Unix candidate was unlinked immediately
}

func (c *nativeRestoreCandidate) cleanup() {
	if c == nil {
		return
	}
	if c.file != nil {
		_ = c.file.Close()
	}
	if c.path != "" {
		_ = os.Remove(c.path)
	}
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

const (
	// ManualBackupArchiveMaxBytes is the portable manual export/restore bound.
	// Older bridges otherwise hold gzip bytes plus a 33%-larger base64 string at
	// once, while native flows stream/copy the same bounded archive outside JS.
	ManualBackupArchiveMaxBytes = 100 * 1024 * 1024
	// ImportArchiveMaxBytes caps the backend payload and extracted regular-file
	// content. Auto-backups use it as their creation cap too, ensuring every
	// archive Studio publishes is accepted by RestoreAutoBackup.
	ImportArchiveMaxBytes = 200 * 1024 * 1024
	// Tar headers, per-entry padding, and optional PAX metadata do not count as
	// file content. Bound the complete decompressed tar stream separately while
	// leaving enough overhead for 10k filesystem entries with long paths.
	ImportArchiveMaxExpandedBytes = ImportArchiveMaxBytes + 64*1024*1024
	// Empty tar entries compress extremely well and do not contribute to the
	// byte cap. Bound their count separately to prevent inode/CPU exhaustion.
	ImportArchiveMaxEntries = 10_000
	nativeRestoreTempPrefix = "gokin-studio-restore-review-"
	nativeRestoreTempSuffix = ".tar.gz"
	nativeRestoreMaxAge     = 24 * time.Hour
)

// configDataMu protects bulk operations over the global config tree. Ordinary
// atomic single-file saves remain independent; this gate prevents a cleanup or
// restore from deleting/renaming whole paths while Export or auto-backup walks
// them. Lock order for code that also touches auto-backups is configDataMu ->
// autoBackupMu.
var configDataMu sync.RWMutex

// archiveSkipNames are file basenames inside the config dir that are
// transient / device-specific and shouldn't be carried across machines.
// .gokin-write-probe is the sentinel from diagnostics writability checks.
var archiveSkipNames = map[string]bool{
	".gokin-write-probe": true,
	".DS_Store":          true,
	permissionsFileName:  true, // device-local network trust must be re-approved after restore/migration
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
	configDataMu.RLock()
	defer configDataMu.RUnlock()

	dir := configDir()
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("nothing to back up — config directory does not exist yet")
		}
		return nil, fmt.Errorf("cannot read config directory: %w", err)
	}

	var buf bytes.Buffer
	filesCount, err := writeConfigArchiveWithLimits(&buf, dir, configArchiveLimits{
		maxOutputBytes:   ManualBackupArchiveMaxBytes,
		maxExpandedBytes: ImportArchiveMaxExpandedBytes,
		maxContentBytes:  ImportArchiveMaxBytes,
		maxEntries:       ImportArchiveMaxEntries,
	})
	if err != nil {
		if errors.Is(err, errArchiveOutputLimit) {
			return nil, fmt.Errorf("backup archive exceeds the %d MB manual export limit", ManualBackupArchiveMaxBytes/(1024*1024))
		}
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

// ExportAllDataToFile asks for a native destination and streams the archive
// straight to disk. The adjacent temp + fsync + rename publication means a
// failed or interrupted export cannot truncate an existing chosen backup.
// ExportAllDataBase64 remains available for older/non-desktop frontends.
func (s *Studio) ExportAllDataToFile() (*NativeBackupResult, error) {
	filename := fmt.Sprintf("gokin-studio-backup-%s.tar.gz", time.Now().Format("2006-01-02"))
	var (
		destination string
		err         error
	)
	if s.testBackupSaveDialog != nil {
		destination, err = s.testBackupSaveDialog(filename)
	} else {
		if s.ctx == nil {
			return nil, errors.New("desktop context is unavailable")
		}
		destination, err = wailsRuntime.SaveFileDialog(s.ctx, wailsRuntime.SaveDialogOptions{
			Title:                "Back up all Gokin Studio data",
			DefaultFilename:      filename,
			CanCreateDirectories: true,
			Filters: []wailsRuntime.FileFilter{
				{DisplayName: "Gzip tar archives (*.tar.gz)", Pattern: "*.tar.gz"},
			},
		})
	}
	if err != nil {
		return nil, fmt.Errorf("choose backup destination: %w", err)
	}
	if destination == "" {
		return &NativeBackupResult{Canceled: true}, nil
	}

	configDataMu.RLock()
	defer configDataMu.RUnlock()
	result, err := writeManualBackupFile(destination, configDir())
	if err != nil {
		return nil, err
	}
	s.logf("info", "backup", "manual backup: wrote %s (%d files, %s)", filepath.Base(result.Path), result.FilesCount, humanBytes(result.Size))
	return result, nil
}

func writeManualBackupFile(destination, sourceDir string) (*NativeBackupResult, error) {
	if strings.TrimSpace(destination) == "" {
		return nil, errors.New("backup destination is empty")
	}
	if _, err := os.Stat(sourceDir); err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("nothing to back up — config directory does not exist yet")
		}
		return nil, fmt.Errorf("cannot read config directory: %w", err)
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return nil, fmt.Errorf("resolve backup destination: %w", err)
	}
	parent := filepath.Dir(destination)
	base := filepath.Base(destination)
	if base == "." || base == string(filepath.Separator) {
		return nil, errors.New("backup destination must be a file")
	}
	canonicalSource, err := filepath.EvalSymlinks(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("resolve config directory: %w", err)
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, fmt.Errorf("resolve backup destination directory: %w", err)
	}
	if pathInsideOrEqual(canonicalSource, filepath.Join(canonicalParent, base)) {
		return nil, errors.New("backup destination must be outside the Gokin Studio config directory")
	}
	f, err := os.CreateTemp(parent, "."+base+".partial-*")
	if err != nil {
		return nil, fmt.Errorf("create backup candidate: %w", err)
	}
	tempPath := f.Name()
	defer func() { _ = os.Remove(tempPath) }()
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	filesCount, archiveErr := writeConfigArchiveWithLimits(f, sourceDir, configArchiveLimits{
		maxOutputBytes:   ManualBackupArchiveMaxBytes,
		maxExpandedBytes: ImportArchiveMaxExpandedBytes,
		maxContentBytes:  ImportArchiveMaxBytes,
		maxEntries:       ImportArchiveMaxEntries,
	})
	if archiveErr != nil {
		if errors.Is(archiveErr, errArchiveOutputLimit) {
			return nil, fmt.Errorf("backup archive exceeds the %d MB manual export limit", ManualBackupArchiveMaxBytes/(1024*1024))
		}
		return nil, archiveErr
	}
	if err := f.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("secure backup candidate: %w", err)
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("flush backup candidate: %w", err)
	}
	if err := f.Close(); err != nil {
		closed = true
		return nil, fmt.Errorf("close backup candidate: %w", err)
	}
	closed = true
	if err := replacePublishedFile(tempPath, destination); err != nil {
		return nil, fmt.Errorf("publish backup: %w", err)
	}
	stat, err := os.Stat(destination)
	if err != nil {
		return nil, fmt.Errorf("verify published backup: %w", err)
	}
	return &NativeBackupResult{
		Filename:   filepath.Base(destination),
		Path:       destination,
		Size:       stat.Size(),
		FilesCount: filesCount,
	}, nil
}

func pathInsideOrEqual(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// SelectRestoreArchiveFile opens a native picker, copies the chosen archive
// into a private bounded candidate, and returns only review metadata + an
// opaque token. Selection never mutates live data. Copying now and confirming
// the staged descriptor later closes the path-replacement TOCTOU window.
func (s *Studio) SelectRestoreArchiveFile() (*NativeRestoreReview, error) {
	s.nativeRestoreSelectMu.Lock()
	defer s.nativeRestoreSelectMu.Unlock()
	s.lifecycleMu.Lock()
	shuttingDown := s.shuttingDown
	s.lifecycleMu.Unlock()
	if shuttingDown {
		return nil, errors.New("studio is shutting down")
	}
	s.clearNativeRestoreCandidate()

	var (
		selected string
		err      error
	)
	if s.testRestoreOpenDialog != nil {
		selected, err = s.testRestoreOpenDialog()
	} else {
		if s.ctx == nil {
			return nil, errors.New("desktop context is unavailable")
		}
		selected, err = wailsRuntime.OpenFileDialog(s.ctx, wailsRuntime.OpenDialogOptions{
			Title: "Select a Gokin Studio backup",
			Filters: []wailsRuntime.FileFilter{
				{DisplayName: "Gzip tar archives (*.tar.gz, *.gz)", Pattern: "*.tar.gz;*.gz"},
			},
		})
	}
	if err != nil {
		return nil, fmt.Errorf("choose restore archive: %w", err)
	}
	if selected == "" {
		return &NativeRestoreReview{Canceled: true}, nil
	}

	candidate, err := stageNativeRestoreCandidate(selected)
	if err != nil {
		return nil, err
	}
	s.lifecycleMu.Lock()
	if s.shuttingDown {
		s.lifecycleMu.Unlock()
		candidate.cleanup()
		return nil, errors.New("studio is shutting down")
	}
	s.nativeRestoreMu.Lock()
	s.nativeRestoreCandidate = candidate
	s.nativeRestoreMu.Unlock()
	s.lifecycleMu.Unlock()
	return &NativeRestoreReview{
		Token:    candidate.token,
		Filename: candidate.filename,
		Size:     candidate.size,
	}, nil
}

func stageNativeRestoreCandidate(selected string) (*nativeRestoreCandidate, error) {
	source, err := os.Open(selected)
	if err != nil {
		return nil, fmt.Errorf("open restore archive: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect restore archive: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("restore archive must be a regular file")
	}
	if info.Size() <= 0 {
		return nil, errors.New("restore archive is empty")
	}
	if info.Size() > ManualBackupArchiveMaxBytes {
		return nil, fmt.Errorf("restore archive exceeds the %d MB limit", ManualBackupArchiveMaxBytes/(1024*1024))
	}

	staged, err := os.CreateTemp("", nativeRestoreTempPrefix+"*"+nativeRestoreTempSuffix)
	if err != nil {
		return nil, fmt.Errorf("create restore review candidate: %w", err)
	}
	stagedPath := staged.Name()
	keep := false
	defer func() {
		if !keep {
			_ = staged.Close()
			_ = os.Remove(stagedPath)
		}
	}()
	if err := staged.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("secure restore review candidate: %w", err)
	}
	copied, err := io.Copy(staged, io.LimitReader(source, ManualBackupArchiveMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("stage restore archive: %w", err)
	}
	if copied > ManualBackupArchiveMaxBytes {
		return nil, fmt.Errorf("restore archive exceeds the %d MB limit", ManualBackupArchiveMaxBytes/(1024*1024))
	}
	if copied != info.Size() {
		return nil, fmt.Errorf("restore archive changed while selected (expected %d bytes, copied %d)", info.Size(), copied)
	}
	if err := staged.Sync(); err != nil {
		return nil, fmt.Errorf("flush restore review candidate: %w", err)
	}
	if _, err := staged.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind restore review candidate: %w", err)
	}
	if hideOpenRestoreReviewFile(stagedPath) {
		stagedPath = ""
	}
	keep = true
	return &nativeRestoreCandidate{
		token:    uuid.NewString(),
		filename: filepath.Base(selected),
		size:     copied,
		file:     staged,
		path:     stagedPath,
	}, nil
}

// ConfirmSelectedRestoreArchive consumes a review token exactly once. Even a
// failed validation consumes it, forcing a fresh selection instead of letting
// stale intent authorize different bytes.
func (s *Studio) ConfirmSelectedRestoreArchive(token string) (*ImportResult, error) {
	candidate, err := s.takeNativeRestoreCandidate(token)
	if err != nil {
		return nil, err
	}
	defer candidate.cleanup()
	info, err := candidate.file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != candidate.size {
		return nil, errors.New("selected restore archive is no longer available")
	}
	if _, err := candidate.file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind selected restore archive: %w", err)
	}
	configDataMu.Lock()
	defer configDataMu.Unlock()
	return s.extractArchiveToConfigDir(candidate.file, "import", preImportPrefix)
}

func (s *Studio) DiscardSelectedRestoreArchive(token string) error {
	candidate, err := s.takeNativeRestoreCandidate(token)
	if err != nil {
		return err
	}
	candidate.cleanup()
	return nil
}

func (s *Studio) takeNativeRestoreCandidate(token string) (*nativeRestoreCandidate, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("restore review token is required")
	}
	s.nativeRestoreMu.Lock()
	defer s.nativeRestoreMu.Unlock()
	candidate := s.nativeRestoreCandidate
	if candidate == nil || candidate.token != token {
		return nil, errors.New("restore review expired or does not match the selected archive")
	}
	s.nativeRestoreCandidate = nil
	return candidate, nil
}

func (s *Studio) clearNativeRestoreCandidate() {
	s.nativeRestoreMu.Lock()
	candidate := s.nativeRestoreCandidate
	s.nativeRestoreCandidate = nil
	s.nativeRestoreMu.Unlock()
	candidate.cleanup()
}

func cleanupStaleNativeRestoreCandidates(now time.Time, tempDir string) (int, []error) {
	directory, err := os.Open(tempDir)
	if err != nil {
		return 0, []error{fmt.Errorf("read temporary directory: %w", err)}
	}
	defer directory.Close()
	removed := 0
	errs := make([]error, 0)
	for {
		// A shared system temp directory can contain many unrelated entries.
		// Read it in batches so startup cleanup has bounded memory overhead.
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, nativeRestoreTempPrefix) || !strings.HasSuffix(name, nativeRestoreTempSuffix) {
				continue
			}
			// Never follow a planted symlink or remove a directory with a matching
			// name. CreateTemp produces regular files; anything else is not ours.
			if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				if len(errs) < 16 {
					errs = append(errs, fmt.Errorf("inspect stale restore candidate: %w", err))
				}
				continue
			}
			if !info.Mode().IsRegular() || now.Sub(info.ModTime()) < nativeRestoreMaxAge {
				continue
			}
			if err := os.Remove(filepath.Join(tempDir, name)); err != nil {
				if len(errs) < 16 {
					errs = append(errs, fmt.Errorf("remove stale restore candidate: %w", err))
				}
				continue
			}
			removed++
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			if len(errs) < 16 {
				errs = append(errs, fmt.Errorf("scan temporary directory: %w", readErr))
			}
			break
		}
	}
	return removed, errs
}

// writeConfigArchive gzip's + tar's every file under `dir` (recursively) to
// the given writer, skipping the iter 750+ skip list AND the iter 840+
// `backups/` subdir (where auto-backups land — recursive backup-of-backups
// would balloon archives). Returns the number of regular files included.
//
// Shared between iter 750+ ExportAllDataBase64 (writes to bytes.Buffer for
// base64 download) and iter 840+ RunAutoBackupIfDue (writes directly to a
// file). The walk is rooted at a held directory handle and includes only
// unchanged regular files; links, devices, sockets and pipes are omitted.
func writeConfigArchive(out io.Writer, dir string) (int, error) {
	return writeConfigArchiveWithLimits(out, dir, configArchiveLimits{
		maxOutputBytes:   ImportArchiveMaxBytes,
		maxExpandedBytes: ImportArchiveMaxExpandedBytes,
		maxContentBytes:  ImportArchiveMaxBytes,
		maxEntries:       ImportArchiveMaxEntries,
	})
}

type configArchiveLimits struct {
	maxOutputBytes   int64
	maxExpandedBytes int64
	maxContentBytes  int64
	maxEntries       int
}

var (
	errArchiveOutputLimit   = errors.New("archive output exceeds size limit")
	errArchiveExpandedLimit = errors.New("archive expanded stream exceeds size limit")
)

type boundedArchiveWriter struct {
	w       io.Writer
	max     int64
	written int64
	limit   error
}

func (w *boundedArchiveWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.max-w.written {
		return 0, fmt.Errorf("%w (%d bytes)", w.limit, w.max)
	}
	n, err := w.w.Write(p)
	w.written += int64(n)
	if n < len(p) && err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}

func writeConfigArchiveWithLimits(out io.Writer, dir string, limits configArchiveLimits) (int, error) {
	if limits.maxOutputBytes <= 0 || limits.maxExpandedBytes <= 0 || limits.maxContentBytes <= 0 || limits.maxEntries <= 0 {
		return 0, errors.New("archive limits must be positive")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, fmt.Errorf("open archive root: %w", err)
	}
	defer root.Close()

	boundedOut := &boundedArchiveWriter{w: out, max: limits.maxOutputBytes, limit: errArchiveOutputLimit}
	gz := gzip.NewWriter(boundedOut)
	boundedExpanded := &boundedArchiveWriter{w: gz, max: limits.maxExpandedBytes, limit: errArchiveExpandedLimit}
	tw := tar.NewWriter(boundedExpanded)
	filesCount := 0
	entriesWritten := 0
	contentBytes := int64(0)
	hasConfigYAML := false

	// Walk through Root.FS rather than resolving absolute path strings again.
	// This keeps directory reads confined too: replacing a visited directory
	// with an outward-pointing symlink cannot expose the target's entry names.
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if rel == "." {
				return walkErr
			}
			return nil // skip unreadable
		}
		if rel == "." {
			return nil // skip the root itself
		}
		// iter 840+: don't walk into the auto-backup directory. Backup files
		// are themselves backups; including them would make every Export
		// grow by the size of previous auto-backups.
		if rel == AutoBackupDirName || strings.HasPrefix(rel, AutoBackupDirName+"/") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// Session Git worktrees live under <configDir>/worktrees/. Each one is
		// a full second checkout of the user's repository, so walking them
		// would make every export and every daily auto-backup carry a copy of
		// every connected repo — gigabytes, and growing with each new chat.
		// They are reconstructible from the repository and are deliberately
		// device-local, so they never belong in a portable archive.
		if rel == sessionWorktreeDirName || strings.HasPrefix(rel, sessionWorktreeDirName+"/") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if archiveSkipNames[path.Base(rel)] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		hdr := &tar.Header{
			Name:    rel,
			Mode:    int64(info.Mode().Perm()),
			ModTime: info.ModTime(),
		}
		if d.IsDir() {
			if entriesWritten >= limits.maxEntries {
				return fmt.Errorf("archive contains too many entries (max %d)", limits.maxEntries)
			}
			hdr.Typeflag = tar.TypeDir
			hdr.Name += "/"
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			entriesWritten++
			return nil
		}
		// Never archive symlinks, FIFOs, sockets, or device nodes. Besides being
		// non-portable, os.Open on a symlink could disclose a file outside the
		// config tree and opening a FIFO can block indefinitely.
		if !info.Mode().IsRegular() {
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
		// Open relative to a held root descriptor. os.Root confines every symlink
		// component to dir on supported desktop platforms. The identity check below
		// then rejects a different target introduced after WalkDir observed the
		// entry (an in-tree link to the same inode remains harmless).
		f, err := root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|archiveOpenExtraFlags, 0)
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
		if !fi.Mode().IsRegular() || !os.SameFile(info, fi) {
			_ = f.Close()
			return nil
		}
		if fi.Size() < 0 || fi.Size() > limits.maxContentBytes-contentBytes {
			_ = f.Close()
			return fmt.Errorf("archive contents exceed %d bytes", limits.maxContentBytes)
		}
		if entriesWritten >= limits.maxEntries {
			_ = f.Close()
			return fmt.Errorf("archive contains too many entries (max %d)", limits.maxEntries)
		}
		hdr.Size = fi.Size()
		hdr.ModTime = fi.ModTime()
		hdr.Mode = int64(fi.Mode().Perm())
		if err := tw.WriteHeader(hdr); err != nil {
			_ = f.Close()
			return err
		}
		entriesWritten++
		contentBytes += fi.Size()
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
		if rel == "config.yaml" {
			hasConfigYAML = true
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("archive walk failed: %w", err)
	}
	if !hasConfigYAML {
		return 0, errors.New("archive source is missing a readable config.yaml")
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
	payload := strings.TrimSpace(base64Data)
	if payload == "" {
		return nil, errors.New("import payload is empty")
	}
	raw, err := decodeImportArchiveBase64(payload, ImportArchiveMaxBytes)
	if err != nil {
		return nil, err
	}
	configDataMu.Lock()
	defer configDataMu.Unlock()
	return s.extractArchiveToConfigDir(bytes.NewReader(raw), "import", preImportPrefix)
}

func decodeImportArchiveBase64(payload string, maxDecodedBytes int) ([]byte, error) {
	if err := validateImportArchiveEncodedSize(len(payload), maxDecodedBytes); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("invalid base64: %w", err)
	}
	if len(raw) > maxDecodedBytes {
		return nil, fmt.Errorf("archive too large (%d bytes, max %d)", len(raw), maxDecodedBytes)
	}
	return raw, nil
}

type boundedArchiveReader struct {
	r         io.Reader
	max       int64
	remaining int64
}

func newBoundedArchiveReader(r io.Reader, max int64) *boundedArchiveReader {
	return &boundedArchiveReader{r: r, max: max, remaining: max}
}

func (r *boundedArchiveReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		// Probe one byte so a stream whose size is exactly max is accepted only
		// after its gzip trailer has actually been read and verified.
		var probe [1]byte
		n, err := r.r.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("%w (%d bytes)", errArchiveExpandedLimit, r.max)
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:int(r.remaining)]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func validateGzipArchiveEnd(expanded *boundedArchiveReader, compressed *bufio.Reader) error {
	// POSIX tar permits zero-filled record padding after the two end markers.
	// Drain and accept only zeroes; any non-zero expanded byte is hidden data.
	padding := make([]byte, 32*1024)
	for {
		n, err := expanded.Read(padding)
		for _, b := range padding[:n] {
			if b != 0 {
				return errors.New("archive contains data after the tar end marker")
			}
		}
		if err == nil && n > 0 {
			continue
		}
		if err == nil {
			return io.ErrNoProgress
		}
		if errors.Is(err, errArchiveExpandedLimit) {
			return err
		}
		if !errors.Is(err, io.EOF) {
			return fmt.Errorf("gzip integrity check failed: %w", err)
		}
		break
	}
	if _, err := compressed.Peek(1); err == nil {
		return errors.New("archive contains trailing compressed data or multiple gzip members")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("inspect gzip trailer: %w", err)
	}
	return nil
}

// validateImportArchiveEncodedSize rejects an oversized bridge payload before
// DecodeString allocates its decoded buffer. The exact decoded-size check above
// remains necessary because padding affects the final byte count.
func validateImportArchiveEncodedSize(encodedBytes, maxDecodedBytes int) error {
	if encodedBytes > base64.StdEncoding.EncodedLen(maxDecodedBytes) {
		return fmt.Errorf("archive too large (encoded payload is %d bytes; maximum decoded size is %d bytes)", encodedBytes, maxDecodedBytes)
	}
	return nil
}

// extractArchiveToConfigDir reads a gzip+tar archive from r and atomically
// swaps it over configDir(). On success, the previous configDir is moved
// aside as a safety backup so the operation is reversible. Returns
// ImportResult on success.
//
// Shared between ImportAllDataBase64 (decoded base64 reader), native reviewed
// restore (private staged descriptor), and RestoreAutoBackup (os.Open reader).
// The caller must hold configDataMu for writing through validation, extraction
// and the directory swap. Three params:
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
	compressed := bufio.NewReader(r)
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, fmt.Errorf("not a gzip stream: %w", err)
	}
	defer gz.Close()
	// A Studio backup is exactly one gzip member containing exactly one tar.
	// Disabling multistream lets us reject concatenated members explicitly.
	gz.Multistream(false)
	expanded := newBoundedArchiveReader(gz, ImportArchiveMaxExpandedBytes)
	tr := tar.NewReader(expanded)

	dir := configDir()
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("cannot create parent: %w", err)
	}
	// 0700: the staging tree becomes the live config dir (API keys + history),
	// which is hardened to 0700 everywhere else. Build it private from the start
	// so there's never even a transient world-traversable window. MkdirTemp is
	// atomic and collision-resistant across multiple Studio processes; never
	// delete a predictable same-second path that another import may own.
	stagingDir, err := os.MkdirTemp(parent, ".gokin-studio.import-staging-"+archivePathNow().Format("20060102-150405")+"-")
	if err != nil {
		return nil, fmt.Errorf("cannot create staging dir: %w", err)
	}

	// Extract.
	filesImported := 0
	entriesSeen := 0
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
		entriesSeen++
		if entriesSeen > ImportArchiveMaxEntries {
			_ = os.RemoveAll(stagingDir)
			return nil, fmt.Errorf("archive contains too many entries (max %d)", ImportArchiveMaxEntries)
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			_ = os.RemoveAll(stagingDir)
			return nil, fmt.Errorf("archive contains unsafe path: %s", hdr.Name)
		}
		if filepath.Base(clean) == permissionsFileName {
			// Domain trust is device-local. Even a handcrafted or older backup
			// cannot silently grant network navigation on the restored machine.
			continue
		}
		target := filepath.Join(stagingDir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				_ = os.RemoveAll(stagingDir)
				return nil, fmt.Errorf("mkdir failed: %w", err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
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
	if err := validateGzipArchiveEnd(expanded, compressed); err != nil {
		_ = os.RemoveAll(stagingDir)
		return nil, err
	}

	if !hasConfigYAML {
		_ = os.RemoveAll(stagingDir)
		return nil, errors.New("archive missing config.yaml — not a Gokin Studio backup")
	}
	if err := validateStudioConfigFile(filepath.Join(stagingDir, "config.yaml")); err != nil {
		_ = os.RemoveAll(stagingDir)
		return nil, fmt.Errorf("archive has invalid config.yaml: %w", err)
	}

	preBackupPath := ""
	if _, err := os.Stat(dir); err == nil {
		preBackupPath, err = moveDirToUniqueSnapshot(dir, parent, safetyPrefix)
		if err != nil {
			_ = os.RemoveAll(stagingDir)
			return nil, fmt.Errorf("could not move existing config aside: %w", err)
		}
	}

	if err := promoteConfigDir(stagingDir, dir, preBackupPath); err != nil {
		// Don't leak the fully-extracted staging tree on swap failure (every
		// other error path above already removes it).
		_ = os.RemoveAll(stagingDir)
		return nil, fmt.Errorf("could not swap in imported data: %w", err)
	}
	// Re-harden the live config dir to 0700. The staging tree was built 0700, but
	// a tar dir entry could carry a looser mode, and os.MkdirAll on the existing
	// dir at next startup won't fix the mode — so enforce it here after the swap.
	_ = os.Chmod(dir, 0o700)

	s.logf("info", kind, "%s %d files from archive (pre-backup at %s)", kind+"ed", filesImported, preBackupPath)

	return &ImportResult{
		FilesImported:   filesImported,
		PreBackupPath:   preBackupPath,
		RestartRequired: true,
	}, nil
}
