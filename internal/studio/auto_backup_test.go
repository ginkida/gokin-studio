package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedRealConfig populates GOKIN_CONFIG_DIR with realistic files so the
// auto-backup walk produces a non-trivial archive.
func seedRealConfig(t *testing.T) {
	t.Helper()
	dir := configDir()
	if err := os.MkdirAll(filepath.Join(dir, "history"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("projects: []"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "history", "p_default.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunAutoBackupIfDue_SkippedWhenDisabled(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedRealConfig(t)

	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.AutoBackupEnabled = false
	result, err := s.RunAutoBackupIfDue()
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped {
		t.Error("expected Skipped=true when AutoBackupEnabled=false")
	}
	if !strings.Contains(result.SkipReason, "AutoBackupEnabled") {
		t.Errorf("SkipReason=%q, want mention of AutoBackupEnabled", result.SkipReason)
	}
	// No backup file should exist.
	if _, err := os.Stat(autoBackupDir()); err == nil {
		t.Error("backup dir should not exist when disabled")
	}
}

func TestRunAutoBackupIfDue_CreatesBackup(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedRealConfig(t)

	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.AutoBackupEnabled = true
	result, err := s.RunAutoBackupIfDue()
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped {
		t.Errorf("expected Skipped=false; SkipReason=%q", result.SkipReason)
	}
	if result.BackupPath == "" {
		t.Fatal("BackupPath empty")
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatalf("backup file not created: %v", err)
	}
	if !strings.HasSuffix(result.BackupPath, ".tar.gz") {
		t.Errorf("BackupPath=%q, want .tar.gz suffix", result.BackupPath)
	}
	if result.FilesIncluded == 0 {
		t.Error("FilesIncluded should be > 0 (seeded config.yaml + history file)")
	}
	if result.Size == 0 {
		t.Error("Size should be > 0")
	}
	// Sentinel should now exist.
	if _, err := os.Stat(autoBackupSentinelPath()); err != nil {
		t.Errorf("sentinel not touched: %v", err)
	}
}

func TestRunAutoBackupIfDue_ProducesValidTarGz(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedRealConfig(t)

	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.AutoBackupEnabled = true
	result, err := s.RunAutoBackupIfDue()
	if err != nil {
		t.Fatal(err)
	}
	// Open the tar.gz and verify config.yaml is in there.
	f, err := os.Open(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip open: %v", err)
	}
	tr := tar.NewReader(gz)
	foundConfig := false
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Name == "config.yaml" {
			foundConfig = true
			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(tr)
			if buf.String() != "projects: []" {
				t.Errorf("config.yaml content=%q, want 'projects: []'", buf.String())
			}
		}
	}
	if !foundConfig {
		t.Error("config.yaml not found in backup archive")
	}
}

func TestRunAutoBackupIfDue_ThrottleSkipsRecentRun(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedRealConfig(t)
	// Touch sentinel now so throttle blocks.
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	touchAutoBackupSentinel()

	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.AutoBackupEnabled = true
	result, err := s.RunAutoBackupIfDue()
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped {
		t.Errorf("expected Skipped=true under fresh throttle; got %+v", result)
	}
	if !strings.Contains(result.SkipReason, "throttle") {
		t.Errorf("SkipReason=%q, want 'throttle'", result.SkipReason)
	}
}

func TestRunAutoBackupIfDue_OldSentinelRuns(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedRealConfig(t)
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	path := autoBackupSentinelPath()
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.AutoBackupEnabled = true
	result, err := s.RunAutoBackupIfDue()
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped {
		t.Errorf("expected run when sentinel is stale; got %+v", result)
	}
}

func TestPruneOldAutoBackups_KeepsRetention(t *testing.T) {
	_ = withTempHistoryDir(t)
	dir := autoBackupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed AutoBackupRetention + 3 files with varying mtimes.
	total := AutoBackupRetention + 3
	for i := range total {
		name := fmt.Sprintf("%s%04d.tar.gz", autoBackupFilenamePrefix, i)
		full := filepath.Join(dir, name)
		if err := os.WriteFile(full, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Older index = older mtime.
		mtime := time.Now().Add(-time.Duration(total-i) * time.Hour)
		if err := os.Chtimes(full, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	removed, freed := pruneOldAutoBackups()
	if removed != 3 {
		t.Errorf("removed=%d, want 3", removed)
	}
	if freed == 0 {
		t.Error("freed should be > 0")
	}
	// Verify the OLDEST three are gone (indices 0, 1, 2 are oldest by mtime).
	for i := range 3 {
		name := fmt.Sprintf("%s%04d.tar.gz", autoBackupFilenamePrefix, i)
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("expected oldest file %s to be removed; err=%v", name, err)
		}
	}
	// The newer ones survived.
	for i := 3; i < total; i++ {
		name := fmt.Sprintf("%s%04d.tar.gz", autoBackupFilenamePrefix, i)
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected newer file %s to survive; err=%v", name, err)
		}
	}
}

func TestPruneOldAutoBackups_NothingToPrune(t *testing.T) {
	_ = withTempHistoryDir(t)
	dir := autoBackupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed exactly AutoBackupRetention files.
	for i := range AutoBackupRetention {
		name := fmt.Sprintf("%s%04d.tar.gz", autoBackupFilenamePrefix, i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	removed, freed := pruneOldAutoBackups()
	if removed != 0 || freed != 0 {
		t.Errorf("removed=%d freed=%d, want 0/0 when at retention limit", removed, freed)
	}
}

func TestPruneOldAutoBackups_IgnoresNonBackupFiles(t *testing.T) {
	_ = withTempHistoryDir(t)
	dir := autoBackupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Drop a stray file that doesn't match the backup prefix.
	stray := filepath.Join(dir, "user-renamed-this.tar.gz")
	if err := os.WriteFile(stray, []byte("important"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Add many auto-backups so prune fires.
	for i := range AutoBackupRetention + 5 {
		name := fmt.Sprintf("%s%04d.tar.gz", autoBackupFilenamePrefix, i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	pruneOldAutoBackups()
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("stray non-backup file should not be touched: %v", err)
	}
}

func TestWriteConfigArchive_SkipsBackupsSubdir(t *testing.T) {
	// THE REGRESSION GUARD: writeConfigArchive must NOT walk into
	// <configDir>/backups/ — otherwise manual Export would balloon by past
	// auto-backups, and would also nest the backup-of-backups recursively.
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	seedRealConfig(t)
	// Drop a stale backup-shaped file into the backups subdir.
	if err := os.MkdirAll(filepath.Join(cfgDir, AutoBackupDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	staleFile := filepath.Join(cfgDir, AutoBackupDirName, "auto-backup-old.tar.gz")
	if err := os.WriteFile(staleFile, []byte("nested-backup-content"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	count, err := writeConfigArchive(&buf, cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("expected non-zero count from seeded config")
	}

	// Decompress + scan: backups/* paths should not appear.
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if strings.HasPrefix(hdr.Name, AutoBackupDirName+"/") {
			t.Errorf("archive contains backups/ entry %q — should be skipped", hdr.Name)
		}
	}
}

func TestRunAutoBackupIfDue_DefaultIsOff(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedRealConfig(t)

	s := NewStudio()
	s.config = defaultConfig()
	// Default AutoBackupEnabled should be false → skipped without explicit setting.
	result, err := s.RunAutoBackupIfDue()
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped {
		t.Error("auto-backup must be opt-in (off by default)")
	}
}
