package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

func TestRunAutoBackupIfDue_NeverReplacesOccupiedNames(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedRealConfig(t)
	if err := os.MkdirAll(autoBackupDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 8, 13, 10, 11, 12, 0, time.UTC)
	previousNow := autoBackupNow
	autoBackupNow = func() time.Time { return fixed }
	t.Cleanup(func() { autoBackupNow = previousNow })

	daily := filepath.Join(autoBackupDir(), autoBackupFilename(fixed, 0))
	second := filepath.Join(autoBackupDir(), autoBackupFilename(fixed, 1))
	occupied := map[string][]byte{
		daily:  []byte("existing daily backup"),
		second: []byte("existing same-second backup"),
	}
	for path, content := range occupied {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.AutoBackupEnabled = true
	result, err := s.RunAutoBackupIfDue()
	if err != nil {
		t.Fatalf("RunAutoBackupIfDue: %v", err)
	}
	wantPath := filepath.Join(autoBackupDir(), autoBackupFilename(fixed, 2))
	if result == nil || result.Skipped || result.BackupPath != wantPath {
		t.Fatalf("result=%+v, want unique path %q", result, wantPath)
	}
	for path, want := range occupied {
		got, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("occupied backup %q changed to %q (error: %v)", path, got, readErr)
		}
	}
	f, err := os.Open(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := gzip.NewReader(f); err != nil {
		t.Fatalf("newly published backup is not a gzip archive: %v", err)
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

func TestRunAutoBackupIfDue_PublishesAtomicallyAndCoalescesConcurrentCalls(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedRealConfig(t)
	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.AutoBackupEnabled = true

	previousWriter := autoBackupArchiveWriter
	var writerCalls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWriter := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWriter()
	autoBackupArchiveWriter = func(out io.Writer, dir string) (int, error) {
		if writerCalls.Add(1) == 1 {
			close(started)
			<-release
		}
		return previousWriter(out, dir)
	}
	t.Cleanup(func() { autoBackupArchiveWriter = previousWriter })

	type outcome struct {
		result *AutoBackupResult
		err    error
	}
	firstDone := make(chan outcome, 1)
	go func() {
		result, err := s.RunAutoBackupIfDue()
		firstDone <- outcome{result: result, err: err}
	}()
	<-started

	// The writer has a real open file containing an incomplete gzip stream, but
	// only its hidden temp name exists. User-facing discovery must not expose it.
	listed, err := s.ListAutoBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("in-progress backup became visible before atomic publish: %+v", listed)
	}

	secondStarted := make(chan struct{})
	secondDone := make(chan outcome, 1)
	go func() {
		close(secondStarted)
		result, err := s.RunAutoBackupIfDue()
		secondDone <- outcome{result: result, err: err}
	}()
	<-secondStarted
	select {
	case second := <-secondDone:
		t.Fatalf("concurrent backup bypassed the lifecycle lock: %+v, %v", second.result, second.err)
	case <-time.After(50 * time.Millisecond):
	}
	if calls := writerCalls.Load(); calls != 1 {
		t.Fatalf("concurrent backup entered the archive writer %d times before publish", calls)
	}

	releaseWriter()
	first, second := <-firstDone, <-secondDone
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent backup results: first=%+v/%v second=%+v/%v", first.result, first.err, second.result, second.err)
	}
	if first.result == nil || first.result.Skipped {
		t.Fatalf("first backup did not publish: %+v", first.result)
	}
	if second.result == nil || !second.result.Skipped || !strings.Contains(second.result.SkipReason, "throttle") {
		t.Fatalf("second backup was not coalesced by the fresh sentinel: %+v", second.result)
	}
	if calls := writerCalls.Load(); calls != 1 {
		t.Fatalf("coalesced backup invoked archive writer %d times", calls)
	}
	listed, err = s.ListAutoBackups()
	if err != nil || len(listed) != 1 || listed[0].Path != first.result.BackupPath {
		t.Fatalf("published backup list=%+v err=%v first=%+v", listed, err, first.result)
	}
	entries, err := os.ReadDir(autoBackupDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".auto-backup-writing-") {
			t.Fatalf("temporary backup survived successful publish: %s", entry.Name())
		}
	}
}

func TestRunAutoBackupIfDue_FailedWriterLeavesNoPartialBackup(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedRealConfig(t)
	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.AutoBackupEnabled = true

	previousWriter := autoBackupArchiveWriter
	autoBackupArchiveWriter = func(out io.Writer, _ string) (int, error) {
		_, _ = out.Write([]byte("partial archive bytes"))
		return 0, errors.New("injected archive failure")
	}
	t.Cleanup(func() { autoBackupArchiveWriter = previousWriter })

	result, err := s.RunAutoBackupIfDue()
	if err == nil || !strings.Contains(err.Error(), "injected archive failure") || result != nil {
		t.Fatalf("failed writer result=%+v err=%v", result, err)
	}
	listed, listErr := s.ListAutoBackups()
	if listErr != nil || len(listed) != 0 {
		t.Fatalf("failed writer published a user-visible backup: %+v, %v", listed, listErr)
	}
	entries, readErr := os.ReadDir(autoBackupDir())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed writer left backup artifacts: %+v", entries)
	}
	if _, statErr := os.Stat(autoBackupSentinelPath()); !os.IsNotExist(statErr) {
		t.Fatalf("failed writer advanced the throttle sentinel: %v", statErr)
	}
}

func TestRunAutoBackupIfDue_MissingConfigLeavesNoPublishedBackup(t *testing.T) {
	_ = withTempHistoryDir(t)
	if err := os.MkdirAll(filepath.Join(configDir(), "history"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir(), "history", "chat.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.AutoBackupEnabled = true

	result, err := s.RunAutoBackupIfDue()
	if err == nil || !strings.Contains(err.Error(), "config.yaml") || result != nil {
		t.Fatalf("result=%+v error=%v, want missing-config failure", result, err)
	}
	entries, readErr := os.ReadDir(autoBackupDir())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed backup left artifacts: %+v", entries)
	}
	if _, statErr := os.Stat(autoBackupSentinelPath()); !os.IsNotExist(statErr) {
		t.Fatalf("failed backup advanced throttle sentinel: %v", statErr)
	}
}

func TestAutoBackupAndCleanupDoNotMutateConfigTreeConcurrently(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedRealConfig(t)
	stale := seedStaleReplay(t, filepath.Join(configDir(), "history"), "backup-race.replay.jsonl", 10*24*time.Hour)
	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.AutoBackupEnabled = true

	previousWriter := autoBackupArchiveWriter
	writerStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWriter := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWriter()
	autoBackupArchiveWriter = func(out io.Writer, dir string) (int, error) {
		close(writerStarted)
		<-release
		return previousWriter(out, dir)
	}
	t.Cleanup(func() { autoBackupArchiveWriter = previousWriter })

	type backupOutcome struct {
		result *AutoBackupResult
		err    error
	}
	backupDone := make(chan backupOutcome, 1)
	go func() {
		result, err := s.RunAutoBackupIfDue()
		backupDone <- backupOutcome{result: result, err: err}
	}()
	<-writerStarted

	type cleanupOutcome struct {
		result *CleanupResult
		err    error
	}
	cleanupStarted := make(chan struct{})
	cleanupDone := make(chan cleanupOutcome, 1)
	go func() {
		close(cleanupStarted)
		result, err := s.CleanupOldData(CleanupParams{ReplayAgeDays: 7})
		cleanupDone <- cleanupOutcome{result: result, err: err}
	}()
	<-cleanupStarted
	select {
	case outcome := <-cleanupDone:
		t.Fatalf("cleanup mutated the config tree during backup read: %+v, %v", outcome.result, outcome.err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("cleanup touched replay while archive reader held the tree: %v", err)
	}

	releaseWriter()
	backup, cleanup := <-backupDone, <-cleanupDone
	if backup.err != nil || backup.result == nil || backup.result.Skipped {
		t.Fatalf("backup result=%+v err=%v", backup.result, backup.err)
	}
	if cleanup.err != nil || cleanup.result == nil || cleanup.result.StaleReplaysRemoved != 1 {
		t.Fatalf("cleanup result=%+v err=%v", cleanup.result, cleanup.err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("cleanup did not run after backup released the tree: %v", err)
	}
}

func TestImportWaitsForActiveAutoBackupRead(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedRealConfig(t)
	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.AutoBackupEnabled = true

	// Capture the import payload before adding a marker. Once the import is
	// allowed to swap the tree, that marker disappearing proves the writer ran.
	exported, err := s.ExportAllDataBase64()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(configDir(), "created-after-export")
	if err := os.WriteFile(marker, []byte("keep while backup reads"), 0o600); err != nil {
		t.Fatal(err)
	}

	previousWriter := autoBackupArchiveWriter
	writerStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWriter := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWriter()
	autoBackupArchiveWriter = func(out io.Writer, dir string) (int, error) {
		close(writerStarted)
		<-release
		return previousWriter(out, dir)
	}
	t.Cleanup(func() { autoBackupArchiveWriter = previousWriter })

	backupDone := make(chan error, 1)
	go func() {
		_, err := s.RunAutoBackupIfDue()
		backupDone <- err
	}()
	<-writerStarted

	type importOutcome struct {
		result *ImportResult
		err    error
	}
	importStarted := make(chan struct{})
	importDone := make(chan importOutcome, 1)
	go func() {
		close(importStarted)
		result, err := s.ImportAllDataBase64(exported.Base64)
		importDone <- importOutcome{result: result, err: err}
	}()
	<-importStarted
	select {
	case outcome := <-importDone:
		t.Fatalf("import swapped the config tree during backup read: %+v, %v", outcome.result, outcome.err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("import touched config tree while backup held the read gate: %v", err)
	}

	releaseWriter()
	if err := <-backupDone; err != nil {
		t.Fatalf("auto-backup failed: %v", err)
	}
	imported := <-importDone
	if imported.err != nil || imported.result == nil || imported.result.FilesImported == 0 {
		t.Fatalf("import result=%+v err=%v", imported.result, imported.err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("import did not replace the post-export marker after backup completed: %v", err)
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
	invalidPrefixed := filepath.Join(dir, "auto-backup-user-notes.txt")
	if err := os.WriteFile(invalidPrefixed, []byte("also important"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "auto-backup-user-link.tar.gz")
	symlinkCreated := os.Symlink(stray, symlink) == nil
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
	if _, err := os.Stat(invalidPrefixed); err != nil {
		t.Errorf("invalid prefixed file should not be touched: %v", err)
	}
	if symlinkCreated {
		if _, err := os.Lstat(symlink); err != nil {
			t.Errorf("backup-shaped symlink should not be touched: %v", err)
		}
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
