package studio

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportAllDataToFile_WritesRestorableArchiveWithoutBridgePayload(t *testing.T) {
	config := t.TempDir()
	t.Setenv("GOKIN_CONFIG_DIR", config)
	if err := os.WriteFile(filepath.Join(config, "config.yaml"), []byte("projects: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(config, "history"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "history", "chat.json"), []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "chosen-backup.tar.gz")
	if err := os.WriteFile(destination, []byte("old backup must survive until publish"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewStudio()
	s.testBackupSaveDialog = func(defaultName string) (string, error) {
		if !strings.HasPrefix(defaultName, "gokin-studio-backup-") || !strings.HasSuffix(defaultName, ".tar.gz") {
			t.Fatalf("unexpected default filename %q", defaultName)
		}
		return destination, nil
	}
	result, err := s.ExportAllDataToFile()
	if err != nil {
		t.Fatalf("ExportAllDataToFile: %v", err)
	}
	if result.Canceled || result.Path != destination || result.Filename != filepath.Base(destination) {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.FilesCount != 2 || result.Size <= 0 {
		t.Fatalf("unexpected archive accounting: %+v", result)
	}
	if info, err := os.Stat(destination); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode=%#o, want 0600", info.Mode().Perm())
	}

	f, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	defer gz.Close()
	entries := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries[hdr.Name] = string(data)
	}
	if entries["config.yaml"] != "projects: []\n" || entries["history/chat.json"] != `{"ok":true}` {
		t.Fatalf("unexpected archive entries: %#v", entries)
	}
}

func TestExportAllDataToFile_CancelDoesNotTouchConfig(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", filepath.Join(t.TempDir(), "missing"))
	s := NewStudio()
	s.testBackupSaveDialog = func(string) (string, error) { return "", nil }
	result, err := s.ExportAllDataToFile()
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !result.Canceled || result.Path != "" || result.Size != 0 || result.FilesCount != 0 {
		t.Fatalf("unexpected cancel result: %+v", result)
	}
}

func TestExportAllDataToFile_DialogErrorIsReported(t *testing.T) {
	s := NewStudio()
	s.testBackupSaveDialog = func(string) (string, error) { return "", errors.New("dialog unavailable") }
	_, err := s.ExportAllDataToFile()
	if err == nil || !strings.Contains(err.Error(), "choose backup destination") {
		t.Fatalf("err=%v", err)
	}
}

func TestWriteManualBackupFile_FailurePreservesExistingDestination(t *testing.T) {
	source := t.TempDir() // deliberately lacks mandatory config.yaml
	destinationDir := t.TempDir()
	destination := filepath.Join(destinationDir, "backup.tar.gz")
	original := []byte("known-good previous backup")
	if err := os.WriteFile(destination, original, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := writeManualBackupFile(destination, source); err == nil || !strings.Contains(err.Error(), "config.yaml") {
		t.Fatalf("err=%v", err)
	}
	if got, err := os.ReadFile(destination); err != nil {
		t.Fatal(err)
	} else if string(got) != string(original) {
		t.Fatalf("existing destination changed to %q", got)
	}
	partials, err := filepath.Glob(filepath.Join(destinationDir, ".backup.tar.gz.partial-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(partials) != 0 {
		t.Fatalf("orphan candidates: %v", partials)
	}
}

func TestWriteManualBackupFile_RejectsDestinationInsideConfigTree(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "config.yaml"), []byte("projects: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(source, "manual.tar.gz")
	if _, err := writeManualBackupFile(inside, source); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("direct inside destination err=%v", err)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatalf("inside destination should not exist, stat err=%v", err)
	}

	symlinkParent := filepath.Join(t.TempDir(), "config-link")
	if err := os.Symlink(source, symlinkParent); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	viaLink := filepath.Join(symlinkParent, "linked.tar.gz")
	if _, err := writeManualBackupFile(viaLink, source); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("symlinked inside destination err=%v", err)
	}
}

func TestWriteManualBackupFile_PublishFailureKeepsTargetAndRemovesCandidate(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "config.yaml"), []byte("projects: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	destination := filepath.Join(parent, "existing-directory.tar.gz")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := writeManualBackupFile(destination, source); err == nil || !strings.Contains(err.Error(), "publish backup") {
		t.Fatalf("err=%v", err)
	}
	if info, err := os.Stat(destination); err != nil {
		t.Fatal(err)
	} else if !info.IsDir() {
		t.Fatalf("existing target was replaced: %v", info.Mode())
	}
	partials, err := filepath.Glob(filepath.Join(parent, ".existing-directory.tar.gz.partial-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(partials) != 0 {
		t.Fatalf("orphan candidates: %v", partials)
	}
}
