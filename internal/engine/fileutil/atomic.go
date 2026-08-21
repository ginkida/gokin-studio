package fileutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// AtomicWrite writes data to a file atomically using a tmp file + rename pattern.
// This prevents data corruption if the process is interrupted during write.
// The file is written to a temporary file in the same directory, its contents
// and permissions are synced, then it is atomically published and the parent
// directory metadata is flushed where the OS exposes that operation.
func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	// Create temporary file in the same directory (required for atomic rename)
	tmp, err := os.CreateTemp(dir, ".gokin-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	// Track success to determine cleanup behavior
	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	// Write data to temporary file
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}

	// Apply permissions before Sync so the durable candidate includes metadata,
	// not only bytes. CreateTemp starts at 0600, so there is no broad-access
	// window when callers request restrictive permissions.
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}

	// Sync to disk to ensure data and mode are persisted before publication.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if err := durableReplace(tmpPath, path); err != nil {
		return err
	}

	success = true
	return nil
}

// SafeFilenameComponent reports whether value can be appended to a trusted
// storage directory as exactly one portable filename component. Store IDs are
// persisted input, so validate both slash spellings regardless of host OS.
func SafeFilenameComponent(value string) bool {
	// Leave room for store-specific suffixes such as .json and avoid Windows'
	// trailing-dot aliases even when validation runs on another host OS.
	if value == "" || len(value) > 240 || value == "." || value == ".." || strings.HasSuffix(value, ".") {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	if strings.ContainsAny(value, `/\\`) {
		return false
	}

	// Windows treats these device basenames as special even when an extension
	// follows. Reject them everywhere so a persisted ID remains portable.
	base := strings.ToUpper(strings.SplitN(value, ".", 2)[0])
	switch base {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return false
	}
	return true
}

// ReadRegularFileLimited reads one stable regular file without following a
// final symlink and without allocating beyond maxBytes. Rechecking identity
// after open closes the lstat/open replacement window before any bytes are
// consumed.
func ReadRegularFileLimited(path string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("invalid file size limit")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("storage path is not a regular file")
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("storage file is too large (%d bytes, maximum %d)", info.Size(), maxBytes)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("storage file changed while opening")
	}
	if opened.Size() > maxBytes {
		return nil, fmt.Errorf("storage file is too large (%d bytes, maximum %d)", opened.Size(), maxBytes)
	}

	data, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return nil, err
	}
	var extra [1]byte
	n, readErr := f.Read(extra[:])
	if n != 0 {
		return nil, fmt.Errorf("storage file is too large (more than %d bytes)", maxBytes)
	}
	if readErr != nil && readErr != io.EOF {
		return nil, readErr
	}
	return data, nil
}

// ReadRegularFileRange reads at most maxBytes from a stable regular file at a
// byte offset. It is intended for incremental log reads where allocating the
// complete remaining file would let an unbounded output exhaust memory.
func ReadRegularFileRange(path string, offset, maxBytes int64) (data []byte, nextOffset, totalBytes int64, err error) {
	if offset < 0 {
		return nil, offset, 0, fmt.Errorf("invalid negative file offset")
	}
	if maxBytes <= 0 {
		return nil, offset, 0, fmt.Errorf("invalid file read limit")
	}

	info, err := os.Lstat(path)
	if err != nil {
		return nil, offset, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, offset, 0, fmt.Errorf("storage path is not a regular file")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, offset, 0, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, offset, 0, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, offset, 0, fmt.Errorf("storage file changed while opening")
	}
	totalBytes = opened.Size()
	if offset >= totalBytes {
		return []byte{}, offset, totalBytes, nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, totalBytes, err
	}
	pageBytes := totalBytes - offset
	if pageBytes > maxBytes {
		pageBytes = maxBytes
	}
	// Cap the reader to the size snapshot returned to the caller. If a live log
	// grows after Stat, those bytes belong to the next page; otherwise
	// nextOffset could exceed totalBytes in one response.
	data, err = io.ReadAll(io.LimitReader(f, pageBytes))
	if err != nil {
		return nil, offset, totalBytes, err
	}
	return data, offset + int64(len(data)), totalBytes, nil
}

// CreatePrivateOutputFile creates a new private streaming-output file. The
// preferred path is used when available; an exclusive sibling is selected on
// collision so an existing file or symlink is never truncated or followed.
func CreatePrivateOutputFile(path string) (*os.File, string, error) {
	base := filepath.Base(path)
	if !SafeFilenameComponent(base) {
		return nil, "", fmt.Errorf("invalid output filename")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return nil, "", err
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return nil, "", fmt.Errorf("output parent is not a real directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, "", err
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return f, path, nil
	}
	if !os.IsExist(err) {
		return nil, "", err
	}
	f, err = os.CreateTemp(dir, base+".*")
	if err != nil {
		return nil, "", err
	}
	return f, f.Name(), nil
}

// RegularFileExists reports whether path currently names a real regular file,
// not a directory, device, or symlink.
func RegularFileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// LatestFileWriter serializes snapshots for one logical store and coalesces
// queued generations. A snapshot that was scheduled before a newer one can
// never publish after it. The zero value is ready for use.
type LatestFileWriter struct {
	writeMu sync.Mutex
	next    atomic.Uint64
	latest  atomic.Uint64

	// writeFile is a test seam. Production always resolves to AtomicWrite.
	writeFile func(string, []byte, os.FileMode) error
}

func (w *LatestFileWriter) persist(generation uint64, path string, data []byte, perm os.FileMode) error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if generation != w.latest.Load() {
		return nil
	}
	write := w.writeFile
	if write == nil {
		write = AtomicWrite
	}
	err := write(path, data, perm)
	// A newer reservation makes this result obsolete even when it arrived
	// while the physical write was already in progress. The newer generation
	// will publish next; callers must not resurrect stale dirty/error state.
	if generation != w.latest.Load() {
		return nil
	}
	return err
}

func (w *LatestFileWriter) reserve() uint64 {
	generation := w.next.Add(1)
	for {
		latest := w.latest.Load()
		if generation <= latest || w.latest.CompareAndSwap(latest, generation) {
			return generation
		}
	}
}

// Schedule reserves the snapshot's generation synchronously, copies its bytes,
// and persists it in the background. complete runs once with nil for success or
// coalescing, or with the durable write error.
func (w *LatestFileWriter) Schedule(path string, data []byte, perm os.FileMode, complete func(error)) {
	w.ScheduleTracked(path, data, perm, func(_ uint64, err error) {
		if complete != nil {
			complete(err)
		}
	})
}

// ScheduleTracked is Schedule with the reserved generation included in the
// completion. Stateful callers can ignore an obsolete error after acquiring
// their own data lock by checking IsLatest.
func (w *LatestFileWriter) ScheduleTracked(path string, data []byte, perm os.FileMode, complete func(uint64, error)) uint64 {
	generation := w.reserve()
	snapshot := append([]byte(nil), data...)
	go func() {
		err := w.persist(generation, path, snapshot, perm)
		if complete != nil {
			complete(generation, err)
		}
	}()
	return generation
}

// IsLatest is safe to call while holding a store's data lock. Stores reserve
// every write while holding that same lock, so the check and dirty-state update
// form one ordered transition.
func (w *LatestFileWriter) IsLatest(generation uint64) bool {
	return generation == w.latest.Load()
}

// Write synchronously publishes a snapshot and supersedes every previously
// scheduled generation, including one waiting for the persistence lock.
func (w *LatestFileWriter) Write(path string, data []byte, perm os.FileMode) error {
	generation := w.reserve()
	return w.persist(generation, path, data, perm)
}

// ValidateStoreID returns a contextual error for public store methods.
func ValidateStoreID(kind, value string) error {
	if !SafeFilenameComponent(value) {
		return fmt.Errorf("invalid %s ID", kind)
	}
	return nil
}

// AtomicWriteString is a convenience wrapper for AtomicWrite that accepts a string.
func AtomicWriteString(path string, content string, perm os.FileMode) error {
	return AtomicWrite(path, []byte(content), perm)
}
