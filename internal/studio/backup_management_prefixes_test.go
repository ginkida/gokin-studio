package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedPreRestore creates a sibling `.gokin-studio.pre-restore-<suffix>` dir
// with the given age. Mirror of seedPreImport from backup_management_test.go.
func seedPreRestore(t *testing.T, parent, suffix string, age time.Duration) string {
	t.Helper()
	full := filepath.Join(parent, preRestorePrefix+suffix)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "config.yaml"), []byte("projects: []"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(full, when, when); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestHasSnapshotPrefix(t *testing.T) {
	cases := map[string]string{
		preImportPrefix + "20250516":  preImportPrefix,
		preRestorePrefix + "20250516": preRestorePrefix,
		".gokin-studio.import-staging-x": "", // staging is NOT a snapshot prefix
		"randomdir":                      "",
		"":                               "",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got := hasSnapshotPrefix(name)
			if got != want {
				t.Errorf("hasSnapshotPrefix(%q)=%q, want %q", name, got, want)
			}
		})
	}
}

func TestListPreImportBackups_IncludesPreRestoreSnapshots(t *testing.T) {
	_ = withTempHistoryDir(t)
	parent := filepath.Dir(configDir())

	// Seed one of each prefix.
	seedPreImport(t, parent, "import-snapshot", 1*time.Hour)
	seedPreRestore(t, parent, "restore-snapshot", 30*time.Minute)

	s := NewStudio()
	list, err := s.ListPreImportBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d entries, want 2 (one of each prefix); list=%+v", len(list), list)
	}
	// Sorted newest first → pre-restore appears first (30m vs 1h old).
	if !strings.Contains(list[0].Name, "restore-snapshot") {
		t.Errorf("list[0]=%q, want newest (pre-restore) first", list[0].Name)
	}
	// Both prefixes accepted.
	prefixes := map[string]bool{}
	for _, b := range list {
		if p := hasSnapshotPrefix(b.Name); p != "" {
			prefixes[p] = true
		}
	}
	if !prefixes[preImportPrefix] || !prefixes[preRestorePrefix] {
		t.Errorf("expected both prefixes in list; got %+v", prefixes)
	}
}

func TestValidateBackupName_AcceptsBothPrefixes(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{preImportPrefix + "20250516", false},
		{preRestorePrefix + "20250516", false},
		{"randomdir", true},
		{"", true},
		{".gokin-studio.import-staging-x", true}, // staging NOT a valid restore target
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateBackupName(c.name)
			if c.wantErr && err == nil {
				t.Errorf("expected error for %q, got nil", c.name)
			}
			if !c.wantErr && err != nil {
				t.Errorf("expected no error for %q, got %v", c.name, err)
			}
		})
	}
}

func TestDeletePreImportBackup_AcceptsPreRestorePrefix(t *testing.T) {
	_ = withTempHistoryDir(t)
	parent := filepath.Dir(configDir())
	full := seedPreRestore(t, parent, "to-delete", 1*time.Hour)
	name := filepath.Base(full)

	s := NewStudio()
	if err := s.DeletePreImportBackup(name); err != nil {
		t.Fatalf("DeletePreImportBackup should accept pre-restore names: %v", err)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Errorf("pre-restore dir not removed: err=%v", err)
	}
}

func TestRestorePreImportBackup_AcceptsPreRestorePrefix(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	parent := filepath.Dir(cfgDir)
	// Seed current config.
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("v: current"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Seed a pre-restore snapshot.
	v1dir := seedPreRestore(t, parent, "v1-snap", 1*time.Hour)
	if err := os.WriteFile(filepath.Join(v1dir, "config.yaml"), []byte("v: pre-restore-v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewStudio()
	result, err := s.RestorePreImportBackup(filepath.Base(v1dir))
	if err != nil {
		t.Fatalf("RestorePreImportBackup should accept pre-restore names: %v", err)
	}
	if !result.RestartRequired {
		t.Error("RestartRequired should be true")
	}
	content, err := os.ReadFile(filepath.Join(cfgDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "v: pre-restore-v1" {
		t.Errorf("active config not restored from pre-restore; got %q", string(content))
	}
}

func TestRestorePreImportBackup_CreatesVisiblePreRestoreSnapshot(t *testing.T) {
	// THE BUG REGRESSION GUARD: restoring from a pre-import snapshot creates
	// a new pre-restore snapshot. That new snapshot MUST appear in the next
	// ListPreImportBackups() call (it didn't before the iter 830+ fix).
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	parent := filepath.Dir(cfgDir)
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("v: current"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Pre-import to restore from.
	importDir := seedPreImport(t, parent, "import-source", 1*time.Hour)

	s := NewStudio()
	// Before restore: only the pre-import snapshot in the list.
	preList, _ := s.ListPreImportBackups()
	if len(preList) != 1 {
		t.Fatalf("pre-restore list has %d entries, want 1: %+v", len(preList), preList)
	}

	if _, err := s.RestorePreImportBackup(filepath.Base(importDir)); err != nil {
		t.Fatal(err)
	}
	// After restore: the pre-import was promoted (gone), but a NEW
	// pre-restore appeared. List should show 1 again.
	postList, _ := s.ListPreImportBackups()
	if len(postList) != 1 {
		t.Fatalf("post-restore list has %d entries, want 1: %+v", len(postList), postList)
	}
	if hasSnapshotPrefix(postList[0].Name) != preRestorePrefix {
		t.Errorf("post-restore entry should be pre-restore prefix; got %q", postList[0].Name)
	}
}

func TestCleanupOldData_RemovesOldPreRestoreSnapshots(t *testing.T) {
	// THE BUG REGRESSION GUARD: pre-restore snapshots must be subject to
	// the same age-based cleanup as pre-import. Previously they accumulated
	// forever because cleanup only matched the pre-import prefix.
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	parent := filepath.Dir(cfgDir)

	old := seedPreRestore(t, parent, "ancient-pre-restore", 100*24*time.Hour)
	recent := seedPreRestore(t, parent, "recent-pre-restore", 5*24*time.Hour)

	s := NewStudio()
	result, err := s.CleanupOldData(DefaultCleanupParams()) // 30-day pre-import threshold
	if err != nil {
		t.Fatal(err)
	}
	if result.PreImportDirsRemoved != 1 {
		t.Errorf("expected 1 snapshot removed; got %d. Pre-restore-prefix dirs are still ignored?", result.PreImportDirsRemoved)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("100-day pre-restore not removed: err=%v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("5-day pre-restore was incorrectly removed: %v", err)
	}
}

func TestAutoCleanup_RemovesOldPreRestoreSnapshots(t *testing.T) {
	// Same bug regression but through the iter 790+ auto-cleanup path:
	// conservative AutoCleanupParams uses 90-day threshold, so a 100-day
	// pre-restore should be removed.
	_ = withTempHistoryDir(t)
	parent := filepath.Dir(configDir())
	old := seedPreRestore(t, parent, "ancient", 100*24*time.Hour)

	s := NewStudio()
	s.config = defaultConfig()
	if err := s.RunAutoCleanupIfDue(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("auto-cleanup should have removed 100-day pre-restore; err=%v", err)
	}
}
