package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestWriteConfigArchive_SkipsUnreadableFiles is the iter 980+ regression
// guard for the tar-corruption bug discovered during the bug hunt.
//
// Pre-fix: writeConfigArchive wrote the tar header BEFORE opening the
// file. If os.Open failed (permission revoked, file deleted, FUSE flaky),
// the function returned nil and silently skipped the body — leaving a
// header that promised N bytes followed by 0 bytes of content. On restore,
// the extractor would either misinterpret the next entry's data as this
// entry's content, or fail with "unexpected EOF". Either way: corrupted
// backup that the user couldn't restore from.
//
// Post-fix: open is attempted FIRST. On failure, the entry is skipped
// cleanly with no header written. The archive remains valid.
//
// We exercise this by chmod-ing a file to 0 so the export goroutine
// hits os.Open EACCES. Linux + Mac honour this; Windows ACL semantics
// differ, so the test is skipped there.
func TestWriteConfigArchive_SkipsUnreadableFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0 doesn't block reads on Windows the same way")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root — chmod 0 doesn't block reads")
	}
	tmp := t.TempDir()
	t.Setenv("GOKIN_CONFIG_DIR", tmp)
	// Seed two files: one readable, one unreadable.
	if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(tmp, "secret.bin")
	if err := os.WriteFile(secret, []byte("super-secret-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secret, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(secret, 0o600) })

	s := NewStudio()
	out, err := s.ExportAllDataBase64()
	if err != nil {
		t.Fatalf("ExportAllDataBase64: %v", err)
	}
	// Decode and inspect the archive — the critical property is that the
	// reader can walk it without erroring. Pre-fix this would fail.
	raw, err := base64.StdEncoding.DecodeString(out.Base64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	names := []string{}
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, hdr.Name)
		// Drain content — pre-fix this would either error or read the
		// wrong bytes (next entry's content masquerading as this one's).
		if _, copyErr := bytes.NewBuffer(nil).ReadFrom(tr); copyErr != nil {
			t.Errorf("body read failed on entry %q: %v", hdr.Name, copyErr)
		}
	}
	// The unreadable file must be absent — that's the whole point: we
	// skipped it cleanly instead of writing a half-entry.
	for _, n := range names {
		if strings.HasSuffix(n, "secret.bin") {
			t.Errorf("unreadable file %q should have been skipped, but appeared in archive", n)
		}
	}
	// The readable file must still be there.
	found := false
	for _, n := range names {
		if strings.HasSuffix(n, "config.yaml") {
			found = true
			break
		}
	}
	if !found {
		t.Error("readable config.yaml missing from archive")
	}
}

// TestWriteConfigArchive_HeaderSizeMatchesContent verifies the iter 980+
// fix where hdr.Size is set from the OPEN file's fstat, not the WalkDir
// d.Info() result. Tests that for files that don't change, header sizes
// match the actual content bytes that follow.
//
// The race window (file shrinks between WalkDir and Open) is small but
// real — log rotation, DB checkpoint, cache eviction can all do it during
// a backup. Pre-fix, mismatch caused archive corruption on restore.
func TestWriteConfigArchive_HeaderSizeMatchesContent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("GOKIN_CONFIG_DIR", tmp)
	contents := map[string]string{
		"config.yaml":   strings.Repeat("a", 100),
		"history/x.json": strings.Repeat("b", 250),
		"drafts/y.txt":   strings.Repeat("c", 50),
	}
	for rel, c := range contents {
		full := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := NewStudio()
	out, err := s.ExportAllDataBase64()
	if err != nil {
		t.Fatalf("ExportAllDataBase64: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(out.Base64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Read the body and assert byte count matches the header.
		buf := bytes.NewBuffer(nil)
		n, err := buf.ReadFrom(tr)
		if err != nil {
			t.Fatalf("body read for %q: %v", hdr.Name, err)
		}
		if n != hdr.Size {
			t.Errorf("entry %q: header Size=%d but %d bytes followed", hdr.Name, hdr.Size, n)
		}
	}
}
