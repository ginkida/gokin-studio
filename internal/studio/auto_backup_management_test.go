package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedAutoBackup creates a fake tar.gz file in the auto-backup dir. Caller
// is responsible for ensuring autoBackupDir exists.
func seedAutoBackupFile(t *testing.T, suffix string, age time.Duration, body []byte) string {
	t.Helper()
	dir := autoBackupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := autoBackupFilenamePrefix + suffix + ".tar.gz"
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, body, 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(full, when, when); err != nil {
		t.Fatal(err)
	}
	return full
}

// seedRealAutoBackup runs RunAutoBackupIfDue once so a valid tar.gz auto-backup
// exists on disk. Returns its filename.
func seedRealAutoBackup(t *testing.T) string {
	t.Helper()
	seedRealConfig(t) // configDir with a config.yaml + history file
	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.AutoBackupEnabled = true
	result, err := s.RunAutoBackupIfDue()
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped {
		t.Fatalf("expected backup; got skip: %s", result.SkipReason)
	}
	return filepath.Base(result.BackupPath)
}

func TestListAutoBackups_Empty(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	list, err := s.ListAutoBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list; got %d entries", len(list))
	}
}

func TestListAutoBackups_DiscoversAndSorts(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedAutoBackupFile(t, "2025-01-01", 10*24*time.Hour, []byte("old"))
	seedAutoBackupFile(t, "2025-05-15", 1*24*time.Hour, []byte("middle"))
	seedAutoBackupFile(t, "2025-05-16", 30*time.Minute, []byte("recent"))
	// Add a stray file that does NOT match the backup prefix → must be ignored.
	stray := filepath.Join(autoBackupDir(), "user-renamed.tar.gz")
	if err := os.WriteFile(stray, []byte("stray"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A matching prefix without the accepted archive suffix is not actionable
	// through Delete/Restore and therefore must not leak into discovery.
	invalidPrefixed := filepath.Join(autoBackupDir(), "auto-backup-not-an-archive.txt")
	if err := os.WriteFile(invalidPrefixed, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Nor should a backup-shaped symlink be advertised as a restorable archive.
	symlink := filepath.Join(autoBackupDir(), "auto-backup-link.tar.gz")
	symlinkCreated := os.Symlink(stray, symlink) == nil
	// And a subdirectory — also must be ignored.
	if err := os.MkdirAll(filepath.Join(autoBackupDir(), "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := NewStudio()
	list, err := s.ListAutoBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d entries, want 3 (valid auto-backups only); list=%+v", len(list), list)
	}
	if _, err := os.Stat(invalidPrefixed); err != nil {
		t.Fatalf("invalid prefixed file was unexpectedly touched: %v", err)
	}
	if symlinkCreated {
		if _, err := os.Lstat(symlink); err != nil {
			t.Fatalf("backup-shaped symlink was unexpectedly touched: %v", err)
		}
	}
	// Newest first.
	if !strings.Contains(list[0].Filename, "2025-05-16") {
		t.Errorf("list[0]=%q, want newest", list[0].Filename)
	}
	if !strings.Contains(list[2].Filename, "2025-01-01") {
		t.Errorf("list[2]=%q, want oldest", list[2].Filename)
	}
	for _, f := range list {
		if !filepath.IsAbs(f.Path) {
			t.Errorf("Path should be absolute; got %q", f.Path)
		}
		if f.Size == 0 {
			t.Errorf("Size should be > 0; entry=%+v", f)
		}
	}
}

func TestValidateAutoBackupFilename(t *testing.T) {
	cases := []struct {
		name    string
		wantErr string
	}{
		{"", "empty"},
		{"auto-backup-2025.tar.gz", ""},
		{"random.tar.gz", "must start"},
		{"auto-backup-2025.txt", "must end with .tar.gz"},
		{"auto-backup-../etc/passwd.tar.gz", "plain basename"},
		{"/auto-backup-x.tar.gz", "must start"},
		{"auto-backup-..foo.tar.gz", "must not contain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateAutoBackupFilename(c.name)
			if c.wantErr == "" {
				if err != nil {
					t.Errorf("expected no error for %q, got %v", c.name, err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q for %q, got nil", c.wantErr, c.name)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("error=%q, want substring %q", err.Error(), c.wantErr)
				}
			}
		})
	}
}

func TestDeleteAutoBackup_Success(t *testing.T) {
	_ = withTempHistoryDir(t)
	full := seedAutoBackupFile(t, "to-delete", 1*time.Hour, []byte("data"))
	name := filepath.Base(full)

	s := NewStudio()
	if err := s.DeleteAutoBackup(name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Errorf("file not removed: err=%v", err)
	}
}

func TestDeleteAutoBackup_RejectsBadName(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	for _, bad := range []string{
		"",
		"random.tar.gz",
		"auto-backup-x.txt",
		"/etc/passwd",
		"../auto-backup-x.tar.gz",
		"auto-backup-..foo.tar.gz",
	} {
		if err := s.DeleteAutoBackup(bad); err == nil {
			t.Errorf("DeleteAutoBackup(%q) should have errored", bad)
		}
	}
}

func TestDeleteAutoBackup_NonExistent(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	err := s.DeleteAutoBackup("auto-backup-ghost.tar.gz")
	if err == nil {
		t.Error("expected error for non-existent backup")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error=%q, want 'not found'", err.Error())
	}
}

func TestDeleteAutoBackup_LogsToEventLog(t *testing.T) {
	_ = withTempHistoryDir(t)
	full := seedAutoBackupFile(t, "log-me", 1*time.Hour, []byte("data"))
	name := filepath.Base(full)

	s := NewStudio()
	if err := s.DeleteAutoBackup(name); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range s.GetRecentLogs() {
		if l.Source == "backup" && strings.Contains(l.Message, "deleted auto-backup") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("delete should log to event log; got %+v", s.GetRecentLogs())
	}
}

func TestRestoreAutoBackup_RoundTrip(t *testing.T) {
	_ = withTempHistoryDir(t)
	// 1. Seed configDir with marker "v1" and create an auto-backup.
	cfgDir := configDir()
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("marker: v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	backupName := seedRealAutoBackup(t) // creates a backup with marker:v1
	// Re-read the actual marker that was inside our seed (overwrites previous)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("marker: v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 2. Mutate configDir to "v2".
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("marker: v2"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 3. Restore from the backup.
	s := NewStudio()
	result, err := s.RestoreAutoBackup(backupName)
	if err != nil {
		t.Fatalf("RestoreAutoBackup: %v", err)
	}
	if !result.RestartRequired {
		t.Error("RestartRequired should be true")
	}
	if result.FilesImported == 0 {
		t.Error("FilesImported should be > 0")
	}
	if result.PreBackupPath == "" {
		t.Error("PreBackupPath should be set (current configDir moved aside)")
	}

	// 4. Active configDir now has v1 again (whatever the seeded backup had at write time).
	content, err := os.ReadFile(filepath.Join(cfgDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The backup was taken when marker=v1 was set initially in seedRealConfig, so:
	if string(content) == "marker: v2" {
		t.Errorf("expected v1 restored from backup; got v2 (not restored?)")
	}
	// The pre-backup should have the v2 content we mutated to.
	v2content, err := os.ReadFile(filepath.Join(result.PreBackupPath, "config.yaml"))
	if err != nil {
		t.Fatalf("safety backup not readable: %v", err)
	}
	if string(v2content) != "marker: v2" {
		t.Errorf("safety backup config=%q, want 'marker: v2' (mutation before restore)", string(v2content))
	}
}

func TestRestoreAutoBackup_RejectsBadFilename(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	for _, bad := range []string{
		"",
		"random.tar.gz",
		"../auto-backup-x.tar.gz",
		"auto-backup-..foo.tar.gz",
	} {
		_, err := s.RestoreAutoBackup(bad)
		if err == nil {
			t.Errorf("RestoreAutoBackup(%q) should have errored", bad)
		}
	}
}

func TestRestoreAutoBackup_NonExistent(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	_, err := s.RestoreAutoBackup("auto-backup-ghost.tar.gz")
	if err == nil {
		t.Error("expected error for non-existent backup")
	}
}

func TestRestoreAutoBackup_RejectsCorruptArchive(t *testing.T) {
	_ = withTempHistoryDir(t)
	// Drop a file with the right NAME shape but garbage content.
	full := seedAutoBackupFile(t, "corrupt", 1*time.Hour, []byte("not a gzip"))
	name := filepath.Base(full)

	s := NewStudio()
	_, err := s.RestoreAutoBackup(name)
	if err == nil {
		t.Error("expected error for corrupt archive")
	}
	if !strings.Contains(err.Error(), "gzip") {
		t.Errorf("error=%q, want 'gzip' (not-a-gzip-stream)", err.Error())
	}
}

func TestRestoreAutoBackup_RejectsFileSwappedWhileOpening(t *testing.T) {
	_ = withTempHistoryDir(t)
	full := seedAutoBackupFile(t, "swap", time.Hour, []byte("original"))
	name := filepath.Base(full)
	previousOpen := autoBackupOpenFile
	autoBackupOpenFile = func(path string) (*os.File, error) {
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
			return nil, err
		}
		return previousOpen(path)
	}
	t.Cleanup(func() { autoBackupOpenFile = previousOpen })

	_, err := NewStudio().RestoreAutoBackup(name)
	if err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("swapped backup error=%v", err)
	}
}

func TestRestoreAutoBackup_LogsToEventLog(t *testing.T) {
	_ = withTempHistoryDir(t)
	backupName := seedRealAutoBackup(t)

	s := NewStudio()
	if _, err := s.RestoreAutoBackup(backupName); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range s.GetRecentLogs() {
		if l.Source == "backup" && strings.Contains(l.Message, "restored from auto-backup") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("restore should log to event log; got %+v", s.GetRecentLogs())
	}
}

func TestListAutoBackups_DirIsRegularFile(t *testing.T) {
	_ = withTempHistoryDir(t)
	// configDir exists but contains a REGULAR FILE where the backups dir
	// should be (e.g. user manually created a file named "backups").
	// ReadDir on that path returns ENOTDIR (or similar). Should not panic
	// or crash, should just return an error or empty list.
	cfgDir := configDir()
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, AutoBackupDirName), []byte("oops not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStudio()
	_, err := s.ListAutoBackups()
	// Either an error or empty list is acceptable; what matters is we don't panic.
	if err == nil {
		t.Log("ListAutoBackups returned no error when 'backups' is a file — empty list ok")
	}
}
