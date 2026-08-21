package fileutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/logging"
)

// FileTransaction provides atomic multi-file operations with rollback support.
// Uses a two-phase commit pattern: prepare (backup) then apply.
type FileTransaction struct {
	id             string
	operations     []FileOperation
	tempDir        string
	committed      bool
	rolledBack     bool
	startTime      time.Time
	mu             sync.Mutex
	RollbackErrors []error
	snapshots      map[string]fileSnapshot
	prepared       bool
	mutated        bool
}

type fileSnapshot struct {
	existed    bool
	backupFile string
	mode       os.FileMode
}

// FileOperation represents a single file operation in a transaction.
type FileOperation struct {
	Type       OperationType
	Path       string
	Content    []byte
	TempFile   string // Temp file for staged content
	BackupFile string // Backup of original for rollback
	NewPath    string // For rename operations
	Mode       os.FileMode
	Applied    bool
}

// OperationType defines the type of file operation.
type OperationType int

const (
	// OpWrite creates or overwrites a file.
	OpWrite OperationType = iota
	// OpDelete removes a file.
	OpDelete
	// OpRename moves/renames a file.
	OpRename
	// OpChmod changes file permissions.
	OpChmod
)

// String returns the operation type name.
func (t OperationType) String() string {
	switch t {
	case OpWrite:
		return "write"
	case OpDelete:
		return "delete"
	case OpRename:
		return "rename"
	case OpChmod:
		return "chmod"
	default:
		return "unknown"
	}
}

// TransactionOption configures a FileTransaction.
type TransactionOption func(*FileTransaction)

// WithID sets a custom transaction ID.
func WithID(id string) TransactionOption {
	return func(tx *FileTransaction) {
		tx.id = id
	}
}

// NewFileTransaction creates a new file transaction.
func NewFileTransaction(opts ...TransactionOption) (*FileTransaction, error) {
	tx := &FileTransaction{
		id:        fmt.Sprintf("tx-%d", time.Now().UnixNano()),
		startTime: time.Now(),
	}

	for _, opt := range opts {
		opt(tx)
	}

	// The user-visible ID is metadata, not a path component. Keeping it out of
	// the MkdirTemp pattern prevents custom IDs from injecting separators.
	tempDir, err := os.MkdirTemp("", "gokin-tx-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	tx.tempDir = tempDir

	return tx, nil
}

// Write stages a file write operation.
func (tx *FileTransaction) Write(path string, content []byte) error {
	return tx.WriteWithMode(path, content, 0644)
}

// WriteWithMode stages a file write with specific permissions.
func (tx *FileTransaction) WriteWithMode(path string, content []byte, mode os.FileMode) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.committed || tx.rolledBack {
		return fmt.Errorf("transaction already finalized")
	}
	if path == "" {
		return fmt.Errorf("write path is empty")
	}

	// Stage content to temp file
	tempFile := filepath.Join(tx.tempDir, fmt.Sprintf("op-%d-write", len(tx.operations)))
	staged := append([]byte(nil), content...)
	if err := os.WriteFile(tempFile, staged, 0o600); err != nil {
		return fmt.Errorf("failed to stage write: %w", err)
	}

	tx.operations = append(tx.operations, FileOperation{
		Type:     OpWrite,
		Path:     path,
		Content:  staged,
		TempFile: tempFile,
		Mode:     mode.Perm(),
	})

	return nil
}

// Delete stages a file deletion operation.
func (tx *FileTransaction) Delete(path string) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.committed || tx.rolledBack {
		return fmt.Errorf("transaction already finalized")
	}
	if path == "" {
		return fmt.Errorf("delete path is empty")
	}

	tx.operations = append(tx.operations, FileOperation{
		Type: OpDelete,
		Path: path,
	})

	return nil
}

// Rename stages a file rename operation.
func (tx *FileTransaction) Rename(oldPath, newPath string) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.committed || tx.rolledBack {
		return fmt.Errorf("transaction already finalized")
	}
	if oldPath == "" || newPath == "" {
		return fmt.Errorf("rename paths must not be empty")
	}
	if filepath.Clean(oldPath) == filepath.Clean(newPath) {
		return fmt.Errorf("rename source and destination are the same path")
	}

	tx.operations = append(tx.operations, FileOperation{
		Type:    OpRename,
		Path:    oldPath,
		NewPath: newPath,
	})

	return nil
}

// Chmod stages a permission change operation.
func (tx *FileTransaction) Chmod(path string, mode os.FileMode) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.committed || tx.rolledBack {
		return fmt.Errorf("transaction already finalized")
	}
	if path == "" {
		return fmt.Errorf("chmod path is empty")
	}

	tx.operations = append(tx.operations, FileOperation{
		Type: OpChmod,
		Path: path,
		Mode: mode.Perm(),
	})

	return nil
}

// Commit applies all staged operations as a rollback-capable batch.
// If any operation fails, all previously applied operations are rolled back.
func (tx *FileTransaction) Commit() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.committed {
		return fmt.Errorf("transaction already committed")
	}
	if tx.rolledBack {
		return fmt.Errorf("transaction was rolled back")
	}

	if len(tx.operations) == 0 {
		tx.committed = true
		tx.cleanup()
		return nil
	}

	// Phase 1: Backup existing files
	if err := tx.backupPhase(); err != nil {
		tx.rolledBack = true
		tx.cleanup()
		return fmt.Errorf("backup phase failed: %w", err)
	}

	// Phase 2: Apply operations
	if err := tx.applyPhase(); err != nil {
		applyErr := fmt.Errorf("apply phase failed: %w", err)
		if rollbackErr := tx.rollbackInternal(); rollbackErr != nil {
			return errors.Join(applyErr, fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		return applyErr
	}

	tx.committed = true
	tx.cleanup()
	return nil
}

// backupPhase snapshots every distinct path before the first mutation. Rename
// destinations are included: operation-local backups cannot correctly restore
// chains such as A->B followed by a write to B.
func (tx *FileTransaction) backupPhase() error {
	paths := make(map[string]struct{})
	for _, op := range tx.operations {
		paths[op.Path] = struct{}{}
		if op.NewPath != "" {
			paths[op.NewPath] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)

	tx.snapshots = make(map[string]fileSnapshot, len(ordered))
	for i, path := range ordered {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			tx.snapshots[path] = fileSnapshot{}
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("transaction path is not a regular file: %s", path)
		}
		backupPath := filepath.Join(tx.tempDir, fmt.Sprintf("backup-%d", i))
		if err := backupRegularFile(path, backupPath); err != nil {
			return fmt.Errorf("failed to backup %s: %w", path, err)
		}
		tx.snapshots[path] = fileSnapshot{existed: true, backupFile: backupPath, mode: info.Mode().Perm()}
	}
	tx.prepared = true
	return nil
}

// applyPhase applies all operations.
func (tx *FileTransaction) applyPhase() error {
	for i := range tx.operations {
		op := &tx.operations[i]
		// Mark the transaction dirty before calling an OS operation: a failed
		// replace/chmod can still have changed the live path.
		tx.mutated = true

		var err error
		switch op.Type {
		case OpWrite:
			err = tx.applyWrite(op)
		case OpDelete:
			err = tx.applyDelete(op)
		case OpRename:
			err = tx.applyRename(op)
		case OpChmod:
			err = tx.applyChmod(op)
		}

		if err != nil {
			return err
		}
		op.Applied = true
	}
	return nil
}

func (tx *FileTransaction) applyWrite(op *FileOperation) error {
	// Create parent directories if needed
	dir := filepath.Dir(op.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	if err := atomicCopyFile(op.TempFile, op.Path, op.Mode); err != nil {
		return fmt.Errorf("failed to write %s: %w", op.Path, err)
	}
	return nil
}

func (tx *FileTransaction) applyDelete(op *FileOperation) error {
	if err := os.Remove(op.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete %s: %w", op.Path, err)
	}
	return nil
}

func (tx *FileTransaction) applyRename(op *FileOperation) error {
	// Create parent directories if needed
	dir := filepath.Dir(op.NewPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	if err := durableMove(op.Path, op.NewPath); err != nil {
		return fmt.Errorf("failed to rename %s to %s: %w", op.Path, op.NewPath, err)
	}
	return nil
}

func (tx *FileTransaction) applyChmod(op *FileOperation) error {
	if err := os.Chmod(op.Path, op.Mode); err != nil {
		return fmt.Errorf("failed to chmod %s: %w", op.Path, err)
	}
	return nil
}

// Rollback undoes all applied operations.
func (tx *FileTransaction) Rollback() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.committed {
		return fmt.Errorf("cannot rollback committed transaction")
	}
	if tx.rolledBack {
		return nil // Already rolled back
	}

	return tx.rollbackInternal()
}

// rollbackInternal performs the actual rollback (must be called with lock held).
func (tx *FileTransaction) rollbackInternal() error {
	tx.RollbackErrors = nil
	if tx.prepared && tx.mutated {
		paths := make([]string, 0, len(tx.snapshots))
		for path := range tx.snapshots {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		removed := make(map[string]bool, len(paths))
		for _, path := range paths {
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				removed[path] = true
				continue
			}
			if err != nil {
				tx.RollbackErrors = append(tx.RollbackErrors, fmt.Errorf("rollback inspect %s: %w", path, err))
				continue
			}
			if info.IsDir() {
				tx.RollbackErrors = append(tx.RollbackErrors, fmt.Errorf("rollback refuses directory at file path %s", path))
				continue
			}
			if err := os.Remove(path); err != nil {
				tx.RollbackErrors = append(tx.RollbackErrors, fmt.Errorf("rollback remove %s: %w", path, err))
				continue
			}
			removed[path] = true
		}
		for _, path := range paths {
			snapshot := tx.snapshots[path]
			if !snapshot.existed || !removed[path] {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				tx.RollbackErrors = append(tx.RollbackErrors, fmt.Errorf("rollback create parent for %s: %w", path, err))
				continue
			}
			if err := atomicCopyFile(snapshot.backupFile, path, snapshot.mode); err != nil {
				tx.RollbackErrors = append(tx.RollbackErrors, fmt.Errorf("rollback restore %s: %w", path, err))
			}
		}
	}

	if len(tx.RollbackErrors) > 0 {
		logging.Warn("transaction rollback encountered errors",
			"tx_id", tx.id,
			"error_count", len(tx.RollbackErrors))
		for _, err := range tx.RollbackErrors {
			logging.Warn("transaction rollback error", "tx_id", tx.id, "error", err)
		}
	}

	tx.rolledBack = true
	tx.cleanup()
	return errors.Join(tx.RollbackErrors...)
}

// cleanup removes the temporary directory.
func (tx *FileTransaction) cleanup() {
	if tx.tempDir != "" {
		_ = os.RemoveAll(tx.tempDir)
		tx.tempDir = ""
	}
}

// ID returns the transaction ID.
func (tx *FileTransaction) ID() string {
	return tx.id
}

// OperationCount returns the number of staged operations.
func (tx *FileTransaction) OperationCount() int {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	return len(tx.operations)
}

// IsFinalized returns true if the transaction is committed or rolled back.
func (tx *FileTransaction) IsFinalized() bool {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	return tx.committed || tx.rolledBack
}

// Duration returns how long the transaction has been open.
func (tx *FileTransaction) Duration() time.Duration {
	return time.Since(tx.startTime)
}

// GetOperations returns a copy of the operations for inspection.
func (tx *FileTransaction) GetOperations() []FileOperation {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	ops := make([]FileOperation, len(tx.operations))
	for i, op := range tx.operations {
		ops[i] = op
		ops[i].Content = append([]byte(nil), op.Content...)
	}
	return ops
}

// backupRegularFile makes a private, exclusive backup from a stable regular
// source. Backups never inherit broader permissions from live files.
func backupRegularFile(src, dst string) error {
	in, info, err := openStableRegular(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		_ = out.Close()
		if !success {
			_ = os.Remove(dst)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := ensureStableSource(src, info); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	success = true
	return nil
}

func openStableRegular(path string) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("path is not a regular file: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = f.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("file changed while opening: %s", path)
	}
	return f, info, nil
}

func ensureStableSource(path string, before os.FileInfo) error {
	after, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(before, after) ||
		after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return fmt.Errorf("file changed while copying: %s", path)
	}
	return nil
}

func atomicCopyFile(src, dst string, mode os.FileMode) error {
	in, info, err := openStableRegular(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".gokin-restore-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		_ = tmp.Close()
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		return err
	}
	if err := ensureStableSource(src, info); err != nil {
		return err
	}
	if err := tmp.Chmod(mode.Perm()); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := durableReplace(tmpPath, dst); err != nil {
		return err
	}
	success = true
	return nil
}

// TransactionResult contains information about a completed transaction.
type TransactionResult struct {
	ID             string
	Committed      bool
	RolledBack     bool
	Duration       time.Duration
	OperationCount int
	FilesModified  []string
}

// Result returns a summary of the transaction.
func (tx *FileTransaction) Result() TransactionResult {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	var files []string
	for _, op := range tx.operations {
		files = append(files, op.Path)
		if op.NewPath != "" {
			files = append(files, op.NewPath)
		}
	}

	return TransactionResult{
		ID:             tx.id,
		Committed:      tx.committed,
		RolledBack:     tx.rolledBack,
		Duration:       time.Since(tx.startTime),
		OperationCount: len(tx.operations),
		FilesModified:  files,
	}
}
