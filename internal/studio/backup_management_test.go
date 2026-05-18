package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedPreImportBackup creates a pre-import dir with given suffix + age.
// Reuses the helper pattern from auto_cleanup_test.go but local here.
func seedPreImport(t *testing.T, parent, suffix string, age time.Duration) string {
	t.Helper()
	full := filepath.Join(parent, preImportPrefix+suffix)
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

func TestListPreImportBackups_Empty(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	list, err := s.ListPreImportBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list; got %d entries: %+v", len(list), list)
	}
}

func TestListPreImportBackups_DiscoversAndSorts(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	parent := filepath.Dir(cfgDir)

	// Make 3 backups with increasing recency.
	seedPreImport(t, parent, "20250101-aaaa", 10*24*time.Hour)
	seedPreImport(t, parent, "20250515-bbbb", 1*24*time.Hour)
	seedPreImport(t, parent, "20250516-cccc", 1*time.Hour)
	// Also drop an UNRELATED sibling dir that must be IGNORED.
	noise := filepath.Join(parent, ".some-other-app")
	if err := os.MkdirAll(noise, 0o755); err != nil {
		t.Fatal(err)
	}
	// And a regular file with the right prefix — must also be ignored (not a dir).
	if err := os.WriteFile(filepath.Join(parent, preImportPrefix+"not-a-dir"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewStudio()
	list, err := s.ListPreImportBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d entries, want 3 (only valid pre-import dirs); list=%+v", len(list), list)
	}
	// Newest first.
	if !strings.Contains(list[0].Name, "cccc") {
		t.Errorf("list[0]=%q, want newest (cccc) first", list[0].Name)
	}
	if !strings.Contains(list[2].Name, "aaaa") {
		t.Errorf("list[2]=%q, want oldest (aaaa) last", list[2].Name)
	}
	// Path is absolute, name is basename.
	if !filepath.IsAbs(list[0].Path) {
		t.Errorf("Path should be absolute; got %q", list[0].Path)
	}
	if filepath.Base(list[0].Path) != list[0].Name {
		t.Errorf("Path basename mismatch with Name: %q vs %q", list[0].Path, list[0].Name)
	}
	if list[0].Size == 0 {
		t.Error("Size should be > 0 (we wrote config.yaml into each)")
	}
}

func TestValidatePreImportName(t *testing.T) {
	cases := []struct {
		name    string
		wantErr string // substring; "" = expect no error
	}{
		{"", "empty"},
		{"random-name", "must start with"},
		{".gokin-studio.pre-import-20250516", ""}, // valid
		// "../etc/passwd" inside the suffix → filepath.Base picks off everything before the last /,
		// so the normalized basename differs from input → "plain basename" rejection.
		{".gokin-studio.pre-import-../etc/passwd", "plain basename"},
		{"../" + preImportPrefix + "x", "must start"},
		// Leading slash makes HasPrefix(name, ".gokin...") false → "must start with" rejection.
		{"/" + preImportPrefix + "x", "must start with"},
		{preImportPrefix + "ok-name", ""}, // valid
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validatePreImportName(c.name)
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

func TestDeletePreImportBackup_Success(t *testing.T) {
	_ = withTempHistoryDir(t)
	parent := filepath.Dir(configDir())
	full := seedPreImport(t, parent, "to-delete", 1*time.Hour)
	name := filepath.Base(full)

	s := NewStudio()
	if err := s.DeletePreImportBackup(name); err != nil {
		t.Fatalf("DeletePreImportBackup: %v", err)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Errorf("backup not removed: err=%v", err)
	}
}

func TestDeletePreImportBackup_RejectsBadName(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	for _, bad := range []string{
		"",
		"randomdir",
		"../etc",
		"/etc/passwd",
		preImportPrefix + "../escape",
	} {
		if err := s.DeletePreImportBackup(bad); err == nil {
			t.Errorf("DeletePreImportBackup(%q) should have errored", bad)
		}
	}
}

func TestDeletePreImportBackup_NonExistent(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	err := s.DeletePreImportBackup(preImportPrefix + "never-existed")
	if err == nil {
		t.Error("expected error for non-existent backup")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error=%q, want 'not found'", err.Error())
	}
}

func TestRestorePreImportBackup_Success(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	parent := filepath.Dir(cfgDir)

	// Seed CURRENT config with marker "v2".
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("marker: v2"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Seed a pre-import backup with marker "v1".
	v1 := filepath.Join(parent, preImportPrefix+"v1-snapshot")
	if err := os.MkdirAll(v1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v1, "config.yaml"), []byte("marker: v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewStudio()
	result, err := s.RestorePreImportBackup(preImportPrefix + "v1-snapshot")
	if err != nil {
		t.Fatalf("RestorePreImportBackup: %v", err)
	}
	if !result.RestartRequired {
		t.Error("RestartRequired should be true")
	}
	if result.PreBackupPath == "" {
		t.Error("PreBackupPath should be set (current data moved aside)")
	}
	if _, err := os.Stat(result.PreBackupPath); err != nil {
		t.Errorf("safety backup not found: %v", err)
	}
	// configDir now has v1.
	content, err := os.ReadFile(filepath.Join(cfgDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "marker: v1" {
		t.Errorf("active config not restored from backup; content=%q", string(content))
	}
	// Safety backup has v2.
	v2content, err := os.ReadFile(filepath.Join(result.PreBackupPath, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(v2content) != "marker: v2" {
		t.Errorf("safety backup didn't capture v2 state; got %q", string(v2content))
	}
}

func TestRestorePreImportBackup_RejectsBadName(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	for _, bad := range []string{
		"",
		"random",
		"../etc",
		preImportPrefix + "../escape",
	} {
		_, err := s.RestorePreImportBackup(bad)
		if err == nil {
			t.Errorf("RestorePreImportBackup(%q) should have errored", bad)
		}
	}
}

func TestRestorePreImportBackup_NonExistent(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	_, err := s.RestorePreImportBackup(preImportPrefix + "ghost")
	if err == nil {
		t.Error("expected error for non-existent backup")
	}
}

func TestDeletePreImportBackup_LogsToEventLog(t *testing.T) {
	_ = withTempHistoryDir(t)
	parent := filepath.Dir(configDir())
	full := seedPreImport(t, parent, "log-me", 1*time.Hour)
	name := filepath.Base(full)

	s := NewStudio()
	if err := s.DeletePreImportBackup(name); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range s.GetRecentLogs() {
		if l.Source == "backup" && strings.Contains(l.Message, "deleted") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("delete should log to event log; got logs=%+v", s.GetRecentLogs())
	}
}

func TestListPreImportBackups_MissingParent(t *testing.T) {
	// Point at config dir whose parent doesn't exist — should return
	// empty list, not crash.
	tmp := filepath.Join(t.TempDir(), "nope", "configdir")
	prev := os.Getenv("GOKIN_CONFIG_DIR")
	_ = os.Setenv("GOKIN_CONFIG_DIR", tmp)
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", prev) })

	s := NewStudio()
	list, err := s.ListPreImportBackups()
	if err != nil {
		t.Errorf("missing parent should not error; got %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list; got %+v", list)
	}
}
