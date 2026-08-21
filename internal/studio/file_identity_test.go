package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// statPair returns the "before" stat and the stat of a freshly opened
// descriptor, which is exactly the pair every caller of sameOpenedFile compares.
func statPair(t *testing.T, path string, mutate func()) (os.FileInfo, os.FileInfo) {
	t.Helper()
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate()
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	after, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return before, after
}

func TestSameOpenedFileAcceptsUntouchedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stable.bin")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, after := statPair(t, path, nil)
	if !sameOpenedFile(before, after) {
		t.Fatal("an untouched file must compare equal")
	}
}

// This is the case bare os.SameFile cannot see. Writing in place keeps the
// device and inode identical — the same thing ext4 produces by handing a freed
// inode straight back to the next create — so device+inode alone still reports
// "same file" while the bytes underneath have been replaced.
func TestSameOpenedFileRejectsInPlaceContentSwap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swapped.bin")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, after := statPair(t, path, func() {
		if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if !os.SameFile(before, after) {
		t.Skip("filesystem allocated a new inode for the rewrite; nothing to prove here")
	}
	if sameOpenedFile(before, after) {
		t.Fatal("a same-inode content swap must be rejected")
	}
}

func TestSameOpenedFileRejectsMtimeOnlyChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "touched.bin")
	if err := os.WriteFile(path, []byte("samesize"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, after := statPair(t, path, func() {
		// Same byte length, different mtime: only the timestamp arm can catch it.
		stamp := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	})
	if !os.SameFile(before, after) {
		t.Skip("filesystem allocated a new inode; nothing to prove here")
	}
	if sameOpenedFile(before, after) {
		t.Fatal("a same-inode same-size mtime change must be rejected")
	}
}

func TestSameOpenedFileRejectsNilInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "present.bin")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if sameOpenedFile(nil, info) || sameOpenedFile(info, nil) || sameOpenedFile(nil, nil) {
		t.Fatal("a missing stat must never compare equal")
	}
}

// The restore path's own regression test: swapping the backup's contents in
// place keeps the inode, so before this check hardened, the substituted archive
// reached the gzip reader instead of being refused. Distinct from
// TestRestoreAutoBackup_RejectsFileSwappedWhileOpening, which deletes and
// recreates the file and therefore only fails on filesystems that hand out a
// fresh inode.
func TestRestoreAutoBackup_RejectsInPlaceContentSwap(t *testing.T) {
	_ = withTempHistoryDir(t)
	full := seedAutoBackupFile(t, "inplace", time.Hour, []byte("original"))
	name := filepath.Base(full)

	previousOpen := autoBackupOpenFile
	autoBackupOpenFile = func(path string) (*os.File, error) {
		if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
			return nil, err
		}
		return previousOpen(path)
	}
	t.Cleanup(func() { autoBackupOpenFile = previousOpen })

	_, err := NewStudio().RestoreAutoBackup(name)
	if err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("in-place swapped backup error=%v, want 'changed while opening'", err)
	}
}
