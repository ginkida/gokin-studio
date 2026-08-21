package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exportedArchiveBytes(t *testing.T, s *Studio) []byte {
	t.Helper()
	result, err := s.ExportAllDataBase64()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(result.Base64)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertRejectedImportPreservesConfig(t *testing.T, s *Studio, raw []byte, wantError string) {
	t.Helper()
	configPath := filepath.Join(configDir(), "config.yaml")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ImportAllDataBase64(base64.StdEncoding.EncodeToString(raw))
	if err == nil || !strings.Contains(err.Error(), wantError) {
		t.Fatalf("result=%+v error=%v, want rejection containing %q", result, err, wantError)
	}
	after, readErr := os.ReadFile(configPath)
	if readErr != nil || !bytes.Equal(after, before) {
		t.Fatalf("current config changed after rejected archive: before=%q after=%q err=%v", before, after, readErr)
	}
	staging, globErr := filepath.Glob(filepath.Join(filepath.Dir(configDir()), ".gokin-studio.import-staging-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(staging) != 0 {
		t.Fatalf("rejected archive leaked staging dirs: %v", staging)
	}
}

func archiveWithConfig(t *testing.T, config []byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "config.yaml", Mode: 0o600, Size: int64(len(config)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(config); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func TestImportAllDataBase64_ValidatesConfigBeforeSwap(t *testing.T) {
	tests := []struct {
		name      string
		config    func() []byte
		wantError string
	}{
		{
			name:      "invalid yaml",
			config:    func() []byte { return []byte("projects: [unterminated\n") },
			wantError: "invalid config.yaml",
		},
		{
			name: "too many projects",
			config: func() []byte {
				var yaml strings.Builder
				yaml.WriteString("projects:\n")
				for i := 0; i <= StudioConfigMaxProjects; i++ {
					fmt.Fprintf(&yaml, "  - id: p%d\n    directory: /tmp/p%d\n", i, i)
				}
				return []byte(yaml.String())
			},
			wantError: "too many projects",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = withTempHistoryDir(t)
			seedConfigDirForArchive(t, configDir())
			assertRejectedImportPreservesConfig(t, NewStudio(), archiveWithConfig(t, tt.config()), tt.wantError)
		})
	}
}

func TestImportAllDataBase64_PromotionFailureRollsBackCurrentConfig(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedConfigDirForArchive(t, configDir())
	s := NewStudio()
	raw := exportedArchiveBytes(t, s)
	current := []byte("projects: []\nsettings:\n  theme: light\n")
	if err := os.WriteFile(filepath.Join(configDir(), "config.yaml"), current, 0o600); err != nil {
		t.Fatal(err)
	}

	previousRename := configDirRename
	calls := 0
	configDirRename = func(oldPath, newPath string) error {
		calls++
		if calls == 2 {
			return errors.New("injected promotion failure")
		}
		return previousRename(oldPath, newPath)
	}
	t.Cleanup(func() { configDirRename = previousRename })

	result, err := s.ImportAllDataBase64(base64.StdEncoding.EncodeToString(raw))
	if err == nil || !strings.Contains(err.Error(), "previous data restored") || result != nil {
		t.Fatalf("result=%+v error=%v, want successful rollback report", result, err)
	}
	got, readErr := os.ReadFile(filepath.Join(configDir(), "config.yaml"))
	if readErr != nil || !bytes.Equal(got, current) {
		t.Fatalf("rollback did not restore current config: content=%q err=%v", got, readErr)
	}
	staging, _ := filepath.Glob(filepath.Join(filepath.Dir(configDir()), ".gokin-studio.import-staging-*"))
	if len(staging) != 0 {
		t.Fatalf("promotion failure leaked staging dirs: %v", staging)
	}
}

func TestImportAllDataBase64_RollbackFailureReportsRecoveryPath(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedConfigDirForArchive(t, configDir())
	s := NewStudio()
	raw := exportedArchiveBytes(t, s)
	current := []byte("projects: []\nsettings:\n  theme: light\n")
	if err := os.WriteFile(filepath.Join(configDir(), "config.yaml"), current, 0o600); err != nil {
		t.Fatal(err)
	}

	previousRename := configDirRename
	calls := 0
	configDirRename = func(oldPath, newPath string) error {
		calls++
		if calls == 2 {
			return errors.New("injected promotion failure")
		}
		if calls == 3 {
			return errors.New("injected rollback failure")
		}
		return previousRename(oldPath, newPath)
	}
	t.Cleanup(func() { configDirRename = previousRename })

	result, err := s.ImportAllDataBase64(base64.StdEncoding.EncodeToString(raw))
	if err == nil || !strings.Contains(err.Error(), "rollback failed") || !strings.Contains(err.Error(), preImportPrefix) || result != nil {
		t.Fatalf("result=%+v error=%v, want explicit recovery path", result, err)
	}
	if _, statErr := os.Stat(configDir()); !os.IsNotExist(statErr) {
		t.Fatalf("active config unexpectedly exists after injected rollback failure: %v", statErr)
	}
	snapshots, globErr := filepath.Glob(filepath.Join(filepath.Dir(configDir()), preImportPrefix+"*"))
	if globErr != nil || len(snapshots) != 1 {
		t.Fatalf("recovery snapshots=%v err=%v, want one", snapshots, globErr)
	}
	got, readErr := os.ReadFile(filepath.Join(snapshots[0], "config.yaml"))
	if readErr != nil || !bytes.Equal(got, current) {
		t.Fatalf("recovery snapshot content=%q err=%v", got, readErr)
	}
}

func TestImportAllDataBase64_VerifiesGzipChecksum(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedConfigDirForArchive(t, configDir())
	s := NewStudio()
	raw := exportedArchiveBytes(t, s)
	if len(raw) < 8 {
		t.Fatal("exported gzip is unexpectedly short")
	}
	raw[len(raw)-1] ^= 0xff // corrupt trailer ISIZE without touching tar payload

	assertRejectedImportPreservesConfig(t, s, raw, "gzip integrity check failed")
}

func TestImportAllDataBase64_RejectsConcatenatedGzipMember(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedConfigDirForArchive(t, configDir())
	s := NewStudio()
	raw := exportedArchiveBytes(t, s)

	var second bytes.Buffer
	gz := gzip.NewWriter(&second)
	if _, err := gz.Write([]byte("hidden second member")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	raw = append(raw, second.Bytes()...)

	assertRejectedImportPreservesConfig(t, s, raw, "multiple gzip members")
}

func TestImportAllDataBase64_RejectsDataAfterTarEnd(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedConfigDirForArchive(t, configDir())

	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	config := []byte("projects: []\n")
	if err := tw.WriteHeader(&tar.Header{Name: "config.yaml", Mode: 0o600, Size: int64(len(config)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(config); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := gz.Write([]byte("data after tar end")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	assertRejectedImportPreservesConfig(t, NewStudio(), raw.Bytes(), "data after the tar end")
}

func TestImportAllDataBase64_AcceptsZeroTarRecordPadding(t *testing.T) {
	_ = withTempHistoryDir(t)
	seedConfigDirForArchive(t, configDir())

	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	config := []byte("projects: []\n")
	if err := tw.WriteHeader(&tar.Header{Name: "config.yaml", Mode: 0o600, Size: int64(len(config)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(config); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := gz.Write(make([]byte, 20*512)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := NewStudio().ImportAllDataBase64(base64.StdEncoding.EncodeToString(raw.Bytes()))
	if err != nil || result == nil || result.FilesImported != 1 {
		t.Fatalf("zero-padded tar result=%+v error=%v", result, err)
	}
}

func TestBoundedArchiveReader_AcceptsExactLimitAndRejectsExpansion(t *testing.T) {
	exact := newBoundedArchiveReader(strings.NewReader("12345678"), 8)
	got, err := io.ReadAll(exact)
	if err != nil || string(got) != "12345678" {
		t.Fatalf("exact limit: content=%q error=%v", got, err)
	}

	over := newBoundedArchiveReader(strings.NewReader("123456789"), 8)
	got, err = io.ReadAll(over)
	if !errors.Is(err, errArchiveExpandedLimit) || string(got) != "12345678" {
		t.Fatalf("over limit: content=%q error=%v", got, err)
	}
}
