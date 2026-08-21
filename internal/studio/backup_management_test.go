package studio

import (
	"bytes"
	"errors"
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
	if info, err := os.Stat(cfgDir); err != nil {
		t.Fatal(err)
	} else if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("restored config dir mode=%#o, want 0700", got)
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

func TestRestorePreImportBackup_ConsecutiveSameSecondRestoresUseDistinctSafetyPaths(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	parent := filepath.Dir(cfgDir)
	seed := func(name, marker string) {
		t.Helper()
		dir := filepath.Join(parent, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	currentConfig := "projects: []\nsettings:\n  theme: dark\n"
	firstConfig := "projects: []\nsettings:\n  theme: light\n"
	secondConfig := "projects: []\nsettings:\n  theme: system\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(currentConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	firstTarget := preImportPrefix + "first"
	secondTarget := preImportPrefix + "second"
	seed(firstTarget, firstConfig)
	seed(secondTarget, secondConfig)

	previousNow := archivePathNow
	archivePathNow = func() time.Time { return time.Date(2026, 8, 12, 12, 34, 56, 0, time.UTC) }
	t.Cleanup(func() { archivePathNow = previousNow })
	s := NewStudio()
	first, err := s.RestorePreImportBackup(firstTarget)
	if err != nil {
		t.Fatalf("first restore: %v", err)
	}
	second, err := s.RestorePreImportBackup(secondTarget)
	if err != nil {
		t.Fatalf("second same-second restore: %v", err)
	}
	if first.PreBackupPath == second.PreBackupPath {
		t.Fatalf("same-second restores collided at %q", first.PreBackupPath)
	}
	for _, snapshot := range []string{first.PreBackupPath, second.PreBackupPath} {
		if _, err := os.Stat(filepath.Join(snapshot, "config.yaml")); err != nil {
			t.Fatalf("safety snapshot %q missing: %v", snapshot, err)
		}
		info, err := os.Stat(snapshot)
		if err != nil {
			t.Fatalf("stat snapshot %q: %v", snapshot, err)
		}
		if info.ModTime().Unix() != archivePathNow().Unix() {
			t.Fatalf("snapshot %q creation mtime=%v, want %v", snapshot, info.ModTime(), archivePathNow())
		}
	}
	active, err := os.ReadFile(filepath.Join(cfgDir, "config.yaml"))
	if err != nil || string(active) != secondConfig {
		t.Fatalf("active config=%q err=%v, want second snapshot", active, err)
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

func TestRestorePreImportBackup_RejectsInvalidConfigBeforeMovingCurrentData(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	parent := filepath.Dir(cfgDir)
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	current := []byte("projects: []\n")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), current, 0o600); err != nil {
		t.Fatal(err)
	}
	name := preImportPrefix + "invalid"
	target := filepath.Join(parent, name)
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "config.yaml"), []byte("projects: [broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := NewStudio().RestorePreImportBackup(name)
	if err == nil || !strings.Contains(err.Error(), "invalid config.yaml") || result != nil {
		t.Fatalf("result=%+v error=%v, want config preflight rejection", result, err)
	}
	got, readErr := os.ReadFile(filepath.Join(cfgDir, "config.yaml"))
	if readErr != nil || !bytes.Equal(got, current) {
		t.Fatalf("current config moved by rejected restore: content=%q err=%v", got, readErr)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("invalid snapshot was consumed: %v", statErr)
	}
	claims, globErr := filepath.Glob(filepath.Join(parent, restoreClaimPrefix+"*"))
	if globErr != nil || len(claims) != 0 {
		t.Fatalf("invalid snapshot leaked restore claims: %v (glob error: %v)", claims, globErr)
	}
}

func TestRestorePreImportBackup_PromotesClaimedSnapshotNotRecreatedName(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	parent := filepath.Dir(cfgDir)
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	current := []byte("projects: []\nsettings:\n  theme: dark\n")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), current, 0o600); err != nil {
		t.Fatal(err)
	}
	name := preImportPrefix + "selected"
	target := filepath.Join(parent, name)
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	selected := []byte("projects: []\nsettings:\n  theme: light\n")
	if err := os.WriteFile(filepath.Join(target, "config.yaml"), selected, 0o600); err != nil {
		t.Fatal(err)
	}

	recreated := []byte("projects: []\nsettings:\n  theme: system\n")
	previousRename := configDirRename
	swapped := false
	configDirRename = func(oldPath, newPath string) error {
		if oldPath == target && strings.HasPrefix(filepath.Base(newPath), restoreClaimPrefix) {
			if err := previousRename(oldPath, newPath); err != nil {
				return err
			}
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(target, "config.yaml"), recreated, 0o600); err != nil {
				return err
			}
			swapped = true
			return nil
		}
		return previousRename(oldPath, newPath)
	}
	t.Cleanup(func() { configDirRename = previousRename })

	result, err := NewStudio().RestorePreImportBackup(name)
	if err != nil {
		t.Fatalf("RestorePreImportBackup: %v", err)
	}
	if !swapped {
		t.Fatal("test did not recreate the public snapshot name after claim")
	}
	active, readErr := os.ReadFile(filepath.Join(cfgDir, "config.yaml"))
	if readErr != nil || !bytes.Equal(active, selected) {
		t.Fatalf("active config=%q err=%v, want originally selected snapshot", active, readErr)
	}
	leftAtName, readErr := os.ReadFile(filepath.Join(target, "config.yaml"))
	if readErr != nil || !bytes.Equal(leftAtName, recreated) {
		t.Fatalf("recreated public snapshot=%q err=%v, want untouched replacement", leftAtName, readErr)
	}
	previous, readErr := os.ReadFile(filepath.Join(result.PreBackupPath, "config.yaml"))
	if readErr != nil || !bytes.Equal(previous, current) {
		t.Fatalf("safety snapshot=%q err=%v, want previous active config", previous, readErr)
	}
}

func TestRestorePreImportBackup_PromotionFailureReturnsClaimAndCurrentData(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	parent := filepath.Dir(cfgDir)
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	current := []byte("projects: []\nsettings:\n  theme: dark\n")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), current, 0o600); err != nil {
		t.Fatal(err)
	}
	name := preImportPrefix + "promotion-failure"
	target := filepath.Join(parent, name)
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	selected := []byte("projects: []\nsettings:\n  theme: light\n")
	if err := os.WriteFile(filepath.Join(target, "config.yaml"), selected, 0o600); err != nil {
		t.Fatal(err)
	}

	previousRename := configDirRename
	configDirRename = func(oldPath, newPath string) error {
		if strings.HasPrefix(filepath.Base(oldPath), restoreClaimPrefix) && newPath == cfgDir {
			return errors.New("injected claimed-snapshot promotion failure")
		}
		return previousRename(oldPath, newPath)
	}
	t.Cleanup(func() { configDirRename = previousRename })

	result, err := NewStudio().RestorePreImportBackup(name)
	if err == nil || result != nil || !strings.Contains(err.Error(), "previous data restored") {
		t.Fatalf("result=%+v error=%v, want rollback report", result, err)
	}
	active, readErr := os.ReadFile(filepath.Join(cfgDir, "config.yaml"))
	if readErr != nil || !bytes.Equal(active, current) {
		t.Fatalf("active config=%q err=%v, want original current data", active, readErr)
	}
	returned, readErr := os.ReadFile(filepath.Join(target, "config.yaml"))
	if readErr != nil || !bytes.Equal(returned, selected) {
		t.Fatalf("returned snapshot=%q err=%v, want selected data", returned, readErr)
	}
	if info, statErr := os.Stat(target); statErr != nil {
		t.Fatal(statErr)
	} else if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("returned snapshot mode=%#o, want original 0755", got)
	}
	claims, globErr := filepath.Glob(filepath.Join(parent, restoreClaimPrefix+"*"))
	if globErr != nil || len(claims) != 0 {
		t.Fatalf("promotion failure leaked restore claims: %v (glob error: %v)", claims, globErr)
	}
}

func TestSnapshotClaimProtectsFromCleanupAndRestoresOriginalAge(t *testing.T) {
	_ = withTempHistoryDir(t)
	parent := filepath.Dir(configDir())
	original := filepath.Join(parent, preImportPrefix+"old-selected")
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	originalTime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(original, originalTime, originalTime); err != nil {
		t.Fatal(err)
	}
	claimedAt := time.Now().Round(time.Second)
	previousNow := archivePathNow
	archivePathNow = func() time.Time { return claimedAt }
	t.Cleanup(func() { archivePathNow = previousNow })

	claim, err := claimSnapshotDir(original, parent)
	if err != nil {
		t.Fatalf("claimSnapshotDir: %v", err)
	}
	claimedInfo, err := os.Stat(claim.path)
	if err != nil {
		t.Fatal(err)
	}
	if !claimedInfo.ModTime().Equal(claimedAt) {
		t.Fatalf("claim mtime=%v, want fresh protection time %v", claimedInfo.ModTime(), claimedAt)
	}
	// Even if the mtime refresh is lost (for example the process dies between
	// Rename and Chtimes), the timestamp embedded in the claim name protects it
	// from another process's age-based cleanup.
	if err := os.Chtimes(claim.path, originalTime, originalTime); err != nil {
		t.Fatal(err)
	}
	cleanup, err := NewStudio().CleanupOldData(CleanupParams{PreImportDays: 1})
	if err != nil {
		t.Fatalf("CleanupOldData: %v", err)
	}
	if cleanup.PreImportDirsRemoved != 0 {
		t.Fatalf("cleanup removed a live restore claim: %+v", cleanup)
	}
	if _, err := os.Stat(claim.path); err != nil {
		t.Fatalf("cleanup touched live restore claim: %v", err)
	}
	cause := errors.New("injected validation failure")
	if got := returnClaimedSnapshot(claim, cause); !errors.Is(got, cause) {
		t.Fatalf("returnClaimedSnapshot error=%v, want wrapped cause", got)
	}
	returnedInfo, err := os.Stat(original)
	if err != nil {
		t.Fatal(err)
	}
	if !returnedInfo.ModTime().Equal(originalTime) {
		t.Fatalf("returned snapshot mtime=%v, want original %v", returnedInfo.ModTime(), originalTime)
	}
	if _, err := os.Stat(claim.path); !os.IsNotExist(err) {
		t.Fatalf("claim path remained after return: %v", err)
	}
}

func TestRestorePreImportBackup_RejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	_ = withTempHistoryDir(t)
	parent := filepath.Dir(configDir())
	external := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(external, 0o700); err != nil {
		t.Fatal(err)
	}
	externalTime := time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(external, externalTime, externalTime); err != nil {
		t.Fatal(err)
	}
	name := preImportPrefix + "symlink"
	link := filepath.Join(parent, name)
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	result, err := NewStudio().RestorePreImportBackup(name)
	if err == nil || result != nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("result=%+v error=%v, want symlink rejection", result, err)
	}
	info, statErr := os.Stat(external)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !info.ModTime().Equal(externalTime) {
		t.Fatalf("external symlink target mtime=%v, want untouched %v", info.ModTime(), externalTime)
	}
	returned, lstatErr := os.Lstat(link)
	if lstatErr != nil {
		t.Fatalf("rejected snapshot symlink was not returned: %v", lstatErr)
	}
	if returned.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("rejected snapshot path mode=%v, want symlink", returned.Mode())
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
