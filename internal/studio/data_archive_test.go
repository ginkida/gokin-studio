package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedConfigDirForArchive populates the temp config dir with a representative
// mix of files so an export/import round-trip exercises subdirs, hidden
// names, and the skip list.
func seedConfigDirForArchive(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"config.yaml":                          "projects: []\nsettings:\n  theme: dark\n",
		"history/p1_default.json":              `{"version":2,"name":"Chat 1","entries":[]}`,
		"history/p1_default.replay.jsonl":      `{"type":"user","text":"hello"}` + "\n",
		"drafts/p1_default.txt":                "unsent typing…",
		"pins/p1_default.json":                 `[{"id":"x","role":"user","content":"pinned"}]`,
		"session-pins/p1.json":                 `["default"]`,
		"session-order/p1.json":                `["default"]`,
		"user_prompt_templates.json":           `[]`,
		"user_snippets.json":                   `[]`,
		"memory/abc123.json":                   `[]`,
		// These should be skipped:
		".gokin-write-probe":                   "ok",
		"history/.DS_Store":                    "junk",
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExportAllDataBase64_RoundTrip(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	seedConfigDirForArchive(t, cfgDir)

	s := NewStudio()
	result, err := s.ExportAllDataBase64()
	if err != nil {
		t.Fatalf("ExportAllDataBase64 failed: %v", err)
	}
	if result.Base64 == "" {
		t.Fatal("Base64 is empty")
	}
	if result.FilesCount == 0 {
		t.Fatal("FilesCount is 0")
	}
	if !strings.HasPrefix(result.Filename, "gokin-studio-backup-") {
		t.Errorf("Filename=%q, expected gokin-studio-backup- prefix", result.Filename)
	}
	if !strings.HasSuffix(result.Filename, ".tar.gz") {
		t.Errorf("Filename=%q, expected .tar.gz suffix", result.Filename)
	}

	// Decode + decompress + inspect entries.
	raw, err := base64.StdEncoding.DecodeString(result.Base64)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip open failed: %v", err)
	}
	tr := tar.NewReader(gz)
	seen := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag == tar.TypeReg {
			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(tr)
			seen[hdr.Name] = buf.String()
		}
	}
	keysOf := func() []string {
		out := make([]string, 0, len(seen))
		for k := range seen {
			out = append(out, k)
		}
		return out
	}
	// config.yaml must be present.
	if _, ok := seen["config.yaml"]; !ok {
		t.Errorf("archive missing config.yaml; have: %v", keysOf())
	}
	// Skip list must not appear.
	if _, ok := seen[".gokin-write-probe"]; ok {
		t.Error("write-probe sentinel was archived (should be skipped)")
	}
	if _, ok := seen["history/.DS_Store"]; ok {
		t.Error(".DS_Store was archived (should be skipped)")
	}
	// History/drafts/pins/etc. should be present.
	for _, want := range []string{
		"history/p1_default.json",
		"history/p1_default.replay.jsonl",
		"drafts/p1_default.txt",
		"pins/p1_default.json",
		"session-pins/p1.json",
		"session-order/p1.json",
		"user_prompt_templates.json",
		"user_snippets.json",
		"memory/abc123.json",
	} {
		if _, ok := seen[want]; !ok {
			t.Errorf("archive missing %q; have: %v", want, keysOf())
		}
	}
}

func TestExportAllDataBase64_MissingConfigDir(t *testing.T) {
	// Point GOKIN_CONFIG_DIR at something that doesn't exist.
	missing := filepath.Join(t.TempDir(), "nope-not-created")
	prev := os.Getenv("GOKIN_CONFIG_DIR")
	_ = os.Setenv("GOKIN_CONFIG_DIR", missing)
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", prev) })

	s := NewStudio()
	_, err := s.ExportAllDataBase64()
	if err == nil {
		t.Fatal("expected error for missing config dir, got nil")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error=%q, want mention of 'does not exist'", err.Error())
	}
}

func TestImportAllDataBase64_RoundTrip(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	seedConfigDirForArchive(t, cfgDir)

	s := NewStudio()
	// Export.
	result, err := s.ExportAllDataBase64()
	if err != nil {
		t.Fatal(err)
	}
	// Mutate the source — delete a file — so we can verify import restores it.
	if err := os.Remove(filepath.Join(cfgDir, "drafts/p1_default.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "drafts/p1_default.txt")); !os.IsNotExist(err) {
		t.Fatal("setup precondition: file should be missing pre-import")
	}

	// Import the archive back.
	imp, err := s.ImportAllDataBase64(result.Base64)
	if err != nil {
		t.Fatalf("ImportAllDataBase64 failed: %v", err)
	}
	if imp.FilesImported == 0 {
		t.Error("FilesImported is 0")
	}
	if !imp.RestartRequired {
		t.Error("expected RestartRequired=true")
	}
	if imp.PreBackupPath == "" {
		t.Error("expected non-empty PreBackupPath")
	}
	if _, err := os.Stat(imp.PreBackupPath); err != nil {
		t.Errorf("pre-backup path not found: %v", err)
	}

	// Verify the deleted file is back.
	got, err := os.ReadFile(filepath.Join(cfgDir, "drafts/p1_default.txt"))
	if err != nil {
		t.Fatalf("restored file read failed: %v", err)
	}
	if string(got) != "unsent typing…" {
		t.Errorf("file content=%q, want 'unsent typing…'", string(got))
	}
}

func TestImportAllDataBase64_EmptyPayload(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	_, err := s.ImportAllDataBase64("")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("err=%v, want 'empty' error", err)
	}
	_, err = s.ImportAllDataBase64("   \n\t  ")
	if err == nil {
		t.Error("whitespace-only payload accepted")
	}
}

func TestImportAllDataBase64_InvalidBase64(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	_, err := s.ImportAllDataBase64("!!!not-base64!!!")
	if err == nil || !strings.Contains(err.Error(), "base64") {
		t.Errorf("err=%v, want 'base64' error", err)
	}
}

func TestImportAllDataBase64_NotGzip(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	// Valid base64, but not gzip content.
	plain := base64.StdEncoding.EncodeToString([]byte("hello world"))
	_, err := s.ImportAllDataBase64(plain)
	if err == nil || !strings.Contains(err.Error(), "gzip") {
		t.Errorf("err=%v, want 'gzip' error", err)
	}
}

func TestImportAllDataBase64_MissingConfigYAML(t *testing.T) {
	_ = withTempHistoryDir(t)

	// Build a tar.gz with only random files (no config.yaml).
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("data")
	hdr := &tar.Header{Name: "randomfile.txt", Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())

	s := NewStudio()
	_, err := s.ImportAllDataBase64(encoded)
	if err == nil || !strings.Contains(err.Error(), "config.yaml") {
		t.Errorf("err=%v, want mention of missing config.yaml", err)
	}
}

func TestImportAllDataBase64_PathTraversalRejected(t *testing.T) {
	_ = withTempHistoryDir(t)

	// Build an archive containing a path with `..` — should be rejected.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("attack")
	hdr := &tar.Header{Name: "../etc/passwd", Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())

	s := NewStudio()
	_, err := s.ImportAllDataBase64(encoded)
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Errorf("err=%v, want 'unsafe path' rejection", err)
	}
}

func TestImportAllDataBase64_DirEntryHandled(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	seedConfigDirForArchive(t, cfgDir)

	// Export → import to verify directories in the archive are processed.
	s := NewStudio()
	exp, err := s.ExportAllDataBase64()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportAllDataBase64(exp.Base64); err != nil {
		t.Fatalf("import failed: %v", err)
	}
	// Subdirs should exist.
	for _, want := range []string{"history", "drafts", "pins", "memory"} {
		if info, err := os.Stat(filepath.Join(cfgDir, want)); err != nil || !info.IsDir() {
			t.Errorf("expected subdir %q to exist after import; err=%v", want, err)
		}
	}
}
