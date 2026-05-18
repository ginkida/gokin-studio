package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportAllDataBase64_CreatesPreImportSafetyDir is the iter 750+
// regression guard — Import must produce a `.gokin-studio.pre-import-*`
// safety dir, NOT pre-restore.
func TestImportAllDataBase64_CreatesPreImportSafetyDir(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed a config so there's something to move aside as safety backup.
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("v: 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedConfigDirForArchive(t, cfgDir)

	s := NewStudio()
	result, err := s.ExportAllDataBase64()
	if err != nil {
		t.Fatal(err)
	}
	imp, err := s.ImportAllDataBase64(result.Base64)
	if err != nil {
		t.Fatal(err)
	}
	if imp.PreBackupPath == "" {
		t.Fatal("expected non-empty PreBackupPath")
	}
	base := filepath.Base(imp.PreBackupPath)
	if !strings.HasPrefix(base, preImportPrefix) {
		t.Errorf("Import safety prefix=%q, want %q*", base, preImportPrefix)
	}
}

// TestRestoreAutoBackup_CreatesPreRestoreSafetyDir is the iter 860+
// regression guard — RestoreAutoBackup must produce a
// `.gokin-studio.pre-restore-*` safety dir, NOT mislabelled pre-import.
func TestRestoreAutoBackup_CreatesPreRestoreSafetyDir(t *testing.T) {
	_ = withTempHistoryDir(t)
	backupName := seedRealAutoBackup(t) // helper from auto_backup_management_test.go

	// Mutate configDir so the restore has something different to move aside.
	if err := os.WriteFile(filepath.Join(configDir(), "config.yaml"), []byte("v: mutated"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewStudio()
	result, err := s.RestoreAutoBackup(backupName)
	if err != nil {
		t.Fatal(err)
	}
	if result.PreBackupPath == "" {
		t.Fatal("expected non-empty PreBackupPath")
	}
	base := filepath.Base(result.PreBackupPath)
	if !strings.HasPrefix(base, preRestorePrefix) {
		t.Errorf("Restore safety prefix=%q, want %q* (NOT pre-import — that's the bug iter 860+ fixed)", base, preRestorePrefix)
	}
	// Defensive: explicitly NOT the pre-import prefix.
	if strings.HasPrefix(base, preImportPrefix) {
		t.Errorf("Restore mislabelled with pre-import prefix: %q", base)
	}
}

// TestRestoreAutoBackup_SafetyDirAppearsInListing — the iter 830+ unified
// listing must show the new pre-restore safety dir created by an
// auto-backup restore. Otherwise users have a phantom rollback target.
func TestRestoreAutoBackup_SafetyDirAppearsInListing(t *testing.T) {
	_ = withTempHistoryDir(t)
	backupName := seedRealAutoBackup(t)

	s := NewStudio()
	preList, _ := s.ListPreImportBackups()
	beforeCount := len(preList)

	if _, err := s.RestoreAutoBackup(backupName); err != nil {
		t.Fatal(err)
	}
	postList, _ := s.ListPreImportBackups()
	if len(postList) != beforeCount+1 {
		t.Fatalf("expected listing to grow by 1 after restore; before=%d after=%d", beforeCount, len(postList))
	}
	// And it should be the pre-restore prefix.
	if !strings.HasPrefix(postList[0].Name, preRestorePrefix) {
		t.Errorf("expected newest entry to be pre-restore; got %q", postList[0].Name)
	}
}
