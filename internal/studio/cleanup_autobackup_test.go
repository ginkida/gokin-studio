package studio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedAutoBackupBatch creates `count` files in autoBackupDir with the
// auto-backup naming convention and decreasing mtimes (older for higher i).
// Returns the list of paths in creation order so tests can identify which
// were oldest vs newest.
func seedAutoBackupBatch(t *testing.T, count int) []string {
	t.Helper()
	dir := autoBackupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, count)
	for i := range count {
		name := fmt.Sprintf("%s%04d.tar.gz", autoBackupFilenamePrefix, i)
		full := filepath.Join(dir, name)
		body := []byte(strings.Repeat("x", 50)) // small but non-zero
		if err := os.WriteFile(full, body, 0o600); err != nil {
			t.Fatal(err)
		}
		// Older mtime for older index.
		mtime := time.Now().Add(-time.Duration(count-i) * time.Hour)
		if err := os.Chtimes(full, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, full)
	}
	return paths
}

// TestCleanupOldData_PrunesExcessAutoBackups is the iter 930+ bug fix
// regression guard. Before the fix, an excess of auto-backup files
// survived CleanupOldData calls because the pruning only ran inside
// RunAutoBackupIfDue (iter 840+). Now CleanupOldData enforces retention
// independently, so a user who disabled auto-backup mid-cycle still gets
// the dead files cleaned by the next manual or auto cleanup.
func TestCleanupOldData_PrunesExcessAutoBackups(t *testing.T) {
	_ = withTempHistoryDir(t)
	total := AutoBackupRetention + 3
	paths := seedAutoBackupBatch(t, total)

	s := NewStudio()
	result, err := s.CleanupOldData(DefaultCleanupParams())
	if err != nil {
		t.Fatal(err)
	}
	if result.AutoBackupsRemoved != 3 {
		t.Errorf("AutoBackupsRemoved=%d, want 3", result.AutoBackupsRemoved)
	}
	if result.BytesFreed == 0 {
		t.Error("BytesFreed should be > 0 (each backup is 50 bytes)")
	}
	// Oldest 3 should be gone; rest still on disk.
	for i := 0; i < 3; i++ {
		if _, err := os.Stat(paths[i]); !os.IsNotExist(err) {
			t.Errorf("oldest file %s should be removed; err=%v", paths[i], err)
		}
	}
	for i := 3; i < total; i++ {
		if _, err := os.Stat(paths[i]); err != nil {
			t.Errorf("newer file %s should survive retention; err=%v", paths[i], err)
		}
	}
}

func TestCleanupOldData_AutoBackupsWithinRetention_NotPruned(t *testing.T) {
	_ = withTempHistoryDir(t)
	// Exactly AutoBackupRetention files — should NOT trigger pruning.
	paths := seedAutoBackupBatch(t, AutoBackupRetention)

	s := NewStudio()
	result, err := s.CleanupOldData(DefaultCleanupParams())
	if err != nil {
		t.Fatal(err)
	}
	if result.AutoBackupsRemoved != 0 {
		t.Errorf("AutoBackupsRemoved=%d, want 0 (at retention limit)", result.AutoBackupsRemoved)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("file at retention boundary should survive: %v", err)
		}
	}
}

func TestCleanupOldData_DryRunCountsAutoBackupsButPreserves(t *testing.T) {
	_ = withTempHistoryDir(t)
	total := AutoBackupRetention + 5
	paths := seedAutoBackupBatch(t, total)

	s := NewStudio()
	params := DefaultCleanupParams()
	params.DryRun = true
	result, err := s.CleanupOldData(params)
	if err != nil {
		t.Fatal(err)
	}
	if result.AutoBackupsRemoved != 5 {
		t.Errorf("dry-run AutoBackupsRemoved=%d, want 5", result.AutoBackupsRemoved)
	}
	if !result.DryRun {
		t.Error("DryRun flag not propagated")
	}
	// All files still on disk in dry-run mode.
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("dry-run should not delete; got err=%v", err)
		}
	}
}

func TestCleanupOldData_AutoBackupsCountedInTotalLog(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedAutoBackupBatch(t, AutoBackupRetention+2)

	s := NewStudio()
	_, err := s.CleanupOldData(DefaultCleanupParams())
	if err != nil {
		t.Fatal(err)
	}
	// Should have logged the cleanup with the new "excess auto-backup(s)" mention.
	found := false
	for _, l := range s.GetRecentLogs() {
		if l.Source == "cleanup" && strings.Contains(l.Message, "excess auto-backup") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("cleanup log should mention excess auto-backup; got %+v", s.GetRecentLogs())
	}
}

func TestCleanupPreviewDefaults_IncludesAutoBackups(t *testing.T) {
	// Preview path (DryRun=true) must surface the new AutoBackupsRemoved
	// count so the UI can show it in the confirmation dialog.
	_ = withTempHistoryDir(t)
	seedAutoBackupBatch(t, AutoBackupRetention+4)

	s := NewStudio()
	preview, err := s.CleanupPreviewDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if !preview.DryRun {
		t.Error("preview should be DryRun=true")
	}
	if preview.AutoBackupsRemoved != 4 {
		t.Errorf("preview AutoBackupsRemoved=%d, want 4", preview.AutoBackupsRemoved)
	}
}

func TestPruneOldAutoBackupsImpl_DryRunDoesNotDelete(t *testing.T) {
	_ = withTempHistoryDir(t)
	paths := seedAutoBackupBatch(t, AutoBackupRetention+2)
	removed, freed := pruneOldAutoBackupsImpl(true)
	if removed != 2 {
		t.Errorf("dry-run removed=%d, want 2", removed)
	}
	if freed == 0 {
		t.Error("dry-run freed should be >0")
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("dry-run should not touch file %s: %v", p, err)
		}
	}
}
