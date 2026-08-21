package fileutil

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFileTransactionRollbackRestoresRenameSourceAndDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	destination := filepath.Join(dir, "destination.txt")
	missing := filepath.Join(dir, "missing.txt")
	writeTestFile(t, source, "source-before", 0o640)
	writeTestFile(t, destination, "destination-before", 0o600)

	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rename(source, destination); err != nil {
		t.Fatal(err)
	}
	if err := tx.Chmod(missing, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("Commit succeeded despite chmod of a missing file")
	}

	assertTestFile(t, source, "source-before", 0o640)
	assertTestFile(t, destination, "destination-before", 0o600)
	if result := tx.Result(); !result.RolledBack || result.Committed {
		t.Fatalf("result=%+v, want rolled back transaction", result)
	}
}

func TestFileTransactionRollbackRestoresInitialStateAcrossOperationChain(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	destination := filepath.Join(dir, "destination.txt")
	created := filepath.Join(dir, "created.txt")
	missing := filepath.Join(dir, "missing.txt")
	writeTestFile(t, source, "source-before", 0o600)
	writeTestFile(t, destination, "destination-before", 0o640)

	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rename(source, destination); err != nil {
		t.Fatal(err)
	}
	if err := tx.WriteWithMode(destination, []byte("destination-after"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tx.Write(created, []byte("created-after")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Chmod(missing, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("Commit succeeded despite chmod of a missing file")
	}

	assertTestFile(t, source, "source-before", 0o600)
	assertTestFile(t, destination, "destination-before", 0o640)
	if _, err := os.Lstat(created); !os.IsNotExist(err) {
		t.Fatalf("created file survived rollback: %v", err)
	}
}

func TestFileTransactionRejectsSymlinkBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	other := filepath.Join(dir, "other.txt")
	writeTestFile(t, target, "target-before", 0o600)
	writeTestFile(t, other, "other-before", 0o600)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Write(other, []byte("other-after")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Delete(link); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("Commit accepted a symlink transaction path")
	}

	assertTestFile(t, target, "target-before", 0o600)
	assertTestFile(t, other, "other-before", 0o600)
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink changed during failed prepare: info=%v err=%v", info, err)
	}
}

func TestFileTransactionCopiesStagedAndInspectedContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.txt")
	content := []byte("original")
	tx, err := NewFileTransaction(WithID("../../untrusted"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(tx.tempDir) != filepath.Clean(os.TempDir()) {
		t.Fatalf("temp directory escaped system temp: %s", tx.tempDir)
	}
	if err := tx.WriteWithMode(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	content[0] = 'X'
	operations := tx.GetOperations()
	operations[0].Content[1] = 'X'
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	assertTestFile(t, path, "original", 0o600)
}

func TestFileTransactionRejectsInvalidPathsAndSamePathRename(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if err := tx.Write("", nil); err == nil {
		t.Error("Write accepted an empty path")
	}
	if err := tx.Delete(""); err == nil {
		t.Error("Delete accepted an empty path")
	}
	if err := tx.Chmod("", 0o600); err == nil {
		t.Error("Chmod accepted an empty path")
	}
	if err := tx.Rename("same", filepath.Join(".", "same")); err == nil {
		t.Error("Rename accepted the same cleaned path")
	}
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte(content)) {
		t.Fatalf("%s content=%q, want %q", path, data, content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != mode.Perm() {
		t.Fatalf("%s mode=%#o, want %#o", path, got, mode.Perm())
	}
}
