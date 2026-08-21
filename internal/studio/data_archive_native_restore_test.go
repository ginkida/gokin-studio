package studio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeNativeRestoreTestArchive(t *testing.T, sourceDir, destination string) {
	t.Helper()
	f, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := writeConfigArchive(f, sourceDir)
	closeErr := f.Close()
	if writeErr != nil {
		t.Fatalf("write archive: %v", writeErr)
	}
	if closeErr != nil {
		t.Fatalf("close archive: %v", closeErr)
	}
}

func TestNativeRestore_StagesSelectionAndConfirmsExactBytes(t *testing.T) {
	live := t.TempDir()
	t.Setenv("GOKIN_CONFIG_DIR", live)
	if err := os.WriteFile(filepath.Join(live, "config.yaml"), []byte("projects: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "current.txt"), []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "config.yaml"), []byte("projects: []\nsettings:\n  theme: dark\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "restored.txt"), []byte("staged bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "selected.tar.gz")
	writeNativeRestoreTestArchive(t, source, archive)

	s := NewStudio()
	s.testRestoreOpenDialog = func() (string, error) { return archive, nil }
	review, err := s.SelectRestoreArchiveFile()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if review.Canceled || review.Token == "" || review.Filename != "selected.tar.gz" || review.Size <= 0 {
		t.Fatalf("unexpected review: %+v", review)
	}
	// Replace the user-visible path after review. Confirmation must consume the
	// private staged copy, never reopen this now-invalid path.
	if err := os.WriteFile(archive, []byte("replacement after review"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := s.ConfirmSelectedRestoreArchive(review.Token)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if result.FilesImported != 2 || !result.RestartRequired || result.PreBackupPath == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if got, err := os.ReadFile(filepath.Join(live, "restored.txt")); err != nil || string(got) != "staged bytes" {
		t.Fatalf("restored file=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(live, "current.txt")); !os.IsNotExist(err) {
		t.Fatalf("old live file remained, stat err=%v", err)
	}
	if _, err := s.ConfirmSelectedRestoreArchive(review.Token); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("second confirm err=%v", err)
	}
}

func TestNativeRestore_CancelSelectionClearsPreviousCandidate(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "config.yaml"), []byte("projects: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "selected.tar.gz")
	writeNativeRestoreTestArchive(t, source, archive)

	s := NewStudio()
	s.testRestoreOpenDialog = func() (string, error) { return archive, nil }
	review, err := s.SelectRestoreArchiveFile()
	if err != nil {
		t.Fatal(err)
	}
	s.nativeRestoreMu.Lock()
	oldFile := s.nativeRestoreCandidate.file
	s.nativeRestoreMu.Unlock()
	s.testRestoreOpenDialog = func() (string, error) { return "", nil }
	canceled, err := s.SelectRestoreArchiveFile()
	if err != nil || !canceled.Canceled {
		t.Fatalf("cancel=%+v err=%v", canceled, err)
	}
	if _, err := oldFile.Stat(); err == nil {
		t.Fatal("previous staged descriptor remained open")
	}
	if _, err := s.ConfirmSelectedRestoreArchive(review.Token); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("old token err=%v", err)
	}
}

func TestNativeRestore_DiscardRequiresExactTokenAndClosesCandidate(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "config.yaml"), []byte("projects: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "selected.tar.gz")
	writeNativeRestoreTestArchive(t, source, archive)
	s := NewStudio()
	s.testRestoreOpenDialog = func() (string, error) { return archive, nil }
	review, err := s.SelectRestoreArchiveFile()
	if err != nil {
		t.Fatal(err)
	}
	s.nativeRestoreMu.Lock()
	stagedFile := s.nativeRestoreCandidate.file
	s.nativeRestoreMu.Unlock()
	if err := s.DiscardSelectedRestoreArchive("wrong-token"); err == nil {
		t.Fatal("wrong token unexpectedly discarded candidate")
	}
	if _, err := stagedFile.Stat(); err != nil {
		t.Fatalf("wrong token closed candidate: %v", err)
	}
	if err := s.DiscardSelectedRestoreArchive(review.Token); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := stagedFile.Stat(); err == nil {
		t.Fatal("discard left staged descriptor open")
	}
}

func TestNativeRestore_InvalidArchiveConsumesReviewWithoutChangingLiveData(t *testing.T) {
	live := t.TempDir()
	t.Setenv("GOKIN_CONFIG_DIR", live)
	if err := os.WriteFile(filepath.Join(live, "config.yaml"), []byte("projects: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(live, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "invalid.tar.gz")
	if err := os.WriteFile(archive, []byte("not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStudio()
	s.testRestoreOpenDialog = func() (string, error) { return archive, nil }
	review, err := s.SelectRestoreArchiveFile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmSelectedRestoreArchive(review.Token); err == nil || !strings.Contains(err.Error(), "not a gzip") {
		t.Fatalf("confirm err=%v", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "keep" {
		t.Fatalf("live marker=%q err=%v", got, err)
	}
	if _, err := s.ConfirmSelectedRestoreArchive(review.Token); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("reused token err=%v", err)
	}
}

func TestNativeRestore_RejectsDialogErrorsAndInvalidFileKinds(t *testing.T) {
	s := NewStudio()
	s.testRestoreOpenDialog = func() (string, error) { return "", errors.New("dialog failed") }
	if _, err := s.SelectRestoreArchiveFile(); err == nil || !strings.Contains(err.Error(), "choose restore archive") {
		t.Fatalf("dialog err=%v", err)
	}

	for name, prepare := range map[string]func(string) error{
		"empty":     func(path string) error { return os.WriteFile(path, nil, 0o600) },
		"directory": func(path string) error { return os.Mkdir(path, 0o700) },
		"oversized": func(path string) error {
			f, err := os.Create(path)
			if err != nil {
				return err
			}
			err = f.Truncate(ManualBackupArchiveMaxBytes + 1)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			return closeErr
		},
	} {
		t.Run(name, func(t *testing.T) {
			selected := filepath.Join(t.TempDir(), name)
			if err := prepare(selected); err != nil {
				t.Fatal(err)
			}
			s := NewStudio()
			s.testRestoreOpenDialog = func() (string, error) { return selected, nil }
			if _, err := s.SelectRestoreArchiveFile(); err == nil {
				t.Fatalf("%s unexpectedly accepted", name)
			}
		})
	}
}

func TestNativeRestore_RefusesSelectionDuringShutdown(t *testing.T) {
	s := NewStudio()
	dialogCalled := false
	s.testRestoreOpenDialog = func() (string, error) {
		dialogCalled = true
		return "", nil
	}
	s.lifecycleMu.Lock()
	s.shuttingDown = true
	s.lifecycleMu.Unlock()
	if _, err := s.SelectRestoreArchiveFile(); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("err=%v", err)
	}
	if dialogCalled {
		t.Fatal("restore dialog opened during shutdown")
	}
}

func TestNativeRestore_ShutdownDuringSelectionCleansStagedCandidate(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "config.yaml"), []byte("projects: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "selected.tar.gz")
	writeNativeRestoreTestArchive(t, source, archive)
	s := NewStudio()
	s.testRestoreOpenDialog = func() (string, error) {
		s.lifecycleMu.Lock()
		s.shuttingDown = true
		s.lifecycleMu.Unlock()
		return archive, nil
	}
	if _, err := s.SelectRestoreArchiveFile(); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("err=%v", err)
	}
	s.nativeRestoreMu.Lock()
	candidate := s.nativeRestoreCandidate
	s.nativeRestoreMu.Unlock()
	if candidate != nil {
		t.Fatal("candidate survived shutdown race")
	}
}

func TestCleanupStaleNativeRestoreCandidates_IsAgeAndTypeBounded(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := filepath.Join(dir, nativeRestoreTempPrefix+"old"+nativeRestoreTempSuffix)
	fresh := filepath.Join(dir, nativeRestoreTempPrefix+"fresh"+nativeRestoreTempSuffix)
	unrelated := filepath.Join(dir, "unrelated.tar.gz")
	matchingDir := filepath.Join(dir, nativeRestoreTempPrefix+"directory"+nativeRestoreTempSuffix)
	for _, path := range []string{old, fresh, unrelated} {
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(matchingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldTime := now.Add(-nativeRestoreMaxAge - time.Minute)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unrelated, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	removed, cleanupErrors := cleanupStaleNativeRestoreCandidates(now, dir)
	if removed != 1 || len(cleanupErrors) != 0 {
		t.Fatalf("removed=%d errors=%v", removed, cleanupErrors)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old candidate survived, stat err=%v", err)
	}
	for _, path := range []string{fresh, unrelated, matchingDir} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("protected path %q removed: %v", path, err)
		}
	}
}

func TestCleanupStaleNativeRestoreCandidates_ReportsUnreadableDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	removed, cleanupErrors := cleanupStaleNativeRestoreCandidates(time.Now(), missing)
	if removed != 0 || len(cleanupErrors) != 1 {
		t.Fatalf("removed=%d errors=%v", removed, cleanupErrors)
	}
}
