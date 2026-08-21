package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedConfigDirForArchive populates the temp config dir with a representative
// mix of files so an export/import round-trip exercises subdirs, hidden
// names, and the skip list.
func seedConfigDirForArchive(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"config.yaml":                     "projects: []\nsettings:\n  theme: dark\n",
		"history/p1_default.json":         `{"version":2,"name":"Chat 1","entries":[]}`,
		"history/p1_default.replay.jsonl": `{"type":"user","text":"hello"}` + "\n",
		"drafts/p1_default.txt":           "unsent typing…",
		"pins/p1_default.json":            `[{"id":"x","role":"user","content":"pinned"}]`,
		"session-pins/p1.json":            `["default"]`,
		"session-order/p1.json":           `["default"]`,
		"user_prompt_templates.json":      `[]`,
		"user_snippets.json":              `[]`,
		"memory/abc123.json":              `[]`,
		// These should be skipped:
		".gokin-write-probe": "ok",
		"history/.DS_Store":  "junk",
		permissionsFileName:  `{"version":1,"origins":["https://trusted.example"]}`,
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
	if _, ok := seen[permissionsFileName]; ok {
		t.Error("device-local browser permissions were archived")
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

func TestExportAllDataBase64_RequiresReadableConfigYAML(t *testing.T) {
	_ = withTempHistoryDir(t)
	if err := os.MkdirAll(filepath.Join(configDir(), "history"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir(), "history", "chat.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := NewStudio().ExportAllDataBase64()
	if err == nil || !strings.Contains(err.Error(), "config.yaml") {
		t.Fatalf("result=%+v error=%v, want missing-config rejection", result, err)
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

func TestImportAllDataBase64_ConsecutiveSameSecondImportsUseDistinctPaths(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	seedConfigDirForArchive(t, cfgDir)
	s := NewStudio()
	exported, err := s.ExportAllDataBase64()
	if err != nil {
		t.Fatal(err)
	}

	fixed := time.Date(2026, 8, 12, 12, 34, 56, 0, time.UTC)
	previousNow := archivePathNow
	archivePathNow = func() time.Time { return fixed }
	t.Cleanup(func() { archivePathNow = previousNow })
	legacyStaging := filepath.Join(filepath.Dir(cfgDir), ".gokin-studio.import-staging-"+fixed.Format("20060102-150405"))
	if err := os.Mkdir(legacyStaging, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(legacyStaging, "owned-by-another-process")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := s.ImportAllDataBase64(exported.Base64)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	second, err := s.ImportAllDataBase64(exported.Base64)
	if err != nil {
		t.Fatalf("second same-second import: %v", err)
	}
	if first.PreBackupPath == second.PreBackupPath {
		t.Fatalf("same-second imports collided at %q", first.PreBackupPath)
	}
	for _, snapshot := range []string{first.PreBackupPath, second.PreBackupPath} {
		if _, err := os.Stat(filepath.Join(snapshot, "config.yaml")); err != nil {
			t.Fatalf("safety snapshot %q is incomplete: %v", snapshot, err)
		}
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "preserve" {
		t.Fatalf("import removed another process's staging dir: content=%q err=%v", got, err)
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

func TestValidateImportArchiveEncodedSize(t *testing.T) {
	const maxDecoded = 8
	maxEncoded := base64.StdEncoding.EncodedLen(maxDecoded)
	if err := validateImportArchiveEncodedSize(maxEncoded, maxDecoded); err != nil {
		t.Fatalf("exact encoded limit rejected: %v", err)
	}
	if err := validateImportArchiveEncodedSize(maxEncoded+1, maxDecoded); err == nil || !strings.Contains(err.Error(), "archive too large") {
		t.Fatalf("oversized encoded payload error=%v, want archive-too-large preflight", err)
	}

	exact := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, maxDecoded))
	if raw, err := decodeImportArchiveBase64(exact, maxDecoded); err != nil || len(raw) != maxDecoded {
		t.Fatalf("exact decoded limit: len=%d err=%v", len(raw), err)
	}
	// Eight bytes encode to 12 base64 bytes, as do nine bytes. This reaches the
	// exact post-decode check and proves padding cannot bypass the hard limit.
	overDecoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, maxDecoded+1))
	if _, err := decodeImportArchiveBase64(overDecoded, maxDecoded); err == nil || !strings.Contains(err.Error(), "archive too large") {
		t.Fatalf("oversized decoded payload error=%v, want archive-too-large check", err)
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

func TestImportAllDataBase64_RejectsEntryCountBombAndPreservesCurrentData(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgPath := filepath.Join(configDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("current-data\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	config := []byte("imported-data\n")
	if err := tw.WriteHeader(&tar.Header{Name: "config.yaml", Mode: 0o600, Size: int64(len(config)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(config); err != nil {
		t.Fatal(err)
	}
	// Together with config.yaml, the final header crosses the entry cap. Link
	// entries are intentionally content-free, demonstrating why the byte limit
	// alone cannot bound this archive.
	for i := 0; i < ImportArchiveMaxEntries; i++ {
		if err := tw.WriteHeader(&tar.Header{
			Name:     fmt.Sprintf("links/%05d", i),
			Mode:     0o600,
			Typeflag: tar.TypeSymlink,
			Linkname: "config.yaml",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := NewStudio().ImportAllDataBase64(base64.StdEncoding.EncodeToString(buf.Bytes()))
	if err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("entry-count bomb error=%v, want bounded rejection", err)
	}
	current, readErr := os.ReadFile(cfgPath)
	if readErr != nil || string(current) != "current-data\n" {
		t.Fatalf("current config changed after rejected import: content=%q err=%v", current, readErr)
	}
	staging, globErr := filepath.Glob(filepath.Join(filepath.Dir(configDir()), ".gokin-studio.import-staging-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(staging) != 0 {
		t.Fatalf("rejected import leaked staging dirs: %v", staging)
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

// TestImportAllDataBase64_RestoresConfigDirTo0700 is the regression for the
// audit finding: extract built the staging tree 0755 and the swap promoted it
// into place without re-hardening, silently downgrading the secret-bearing
// config dir from its 0700 default. Import must leave it 0700.
func TestImportAllDataBase64_RestoresConfigDirTo0700(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	seedConfigDirForArchive(t, cfgDir)

	s := NewStudio()
	result, err := s.ExportAllDataBase64()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportAllDataBase64(result.Base64); err != nil {
		t.Fatalf("ImportAllDataBase64: %v", err)
	}

	info, err := os.Stat(cfgDir)
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir mode after import = %#o, want 0700 (import must not downgrade the secret-bearing dir)", perm)
	}
}
