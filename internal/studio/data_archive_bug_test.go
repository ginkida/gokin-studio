package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"io"
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

func TestExportAllDataBase64_RejectsUnreadableRootInsteadOfPublishingEmptyArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory mode permissions differ on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory mode checks")
	}
	cfgDir := t.TempDir()
	t.Setenv("GOKIN_CONFIG_DIR", cfgDir)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("projects: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfgDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfgDir, 0o700) })

	result, err := NewStudio().ExportAllDataBase64()
	if err == nil {
		t.Fatalf("unreadable config root produced a backup instead of failing: %+v", result)
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
		"config.yaml":    strings.Repeat("a", 100),
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

func TestWriteConfigArchive_SkipsSymlinksOutsideConfigTree(t *testing.T) {
	cfgDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideSecret := filepath.Join(outsideDir, "outside-secret.txt")
	if err := os.WriteFile(outsideSecret, []byte("must-never-enter-backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("projects: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cfgDir, "innocent-looking.txt")
	if err := os.Symlink(outsideSecret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var archive bytes.Buffer
	count, err := writeConfigArchive(&archive, cfgDir)
	if err != nil {
		t.Fatalf("writeConfigArchive: %v", err)
	}
	if count != 1 {
		t.Fatalf("filesCount=%d, want only config.yaml", count)
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		if hdr.Name == "innocent-looking.txt" {
			t.Fatal("archive followed an external symlink")
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte("must-never-enter-backup")) {
			t.Fatalf("entry %q leaked external file contents", hdr.Name)
		}
	}
}

func TestWriteConfigArchiveWithLimits_BoundsPublishedArchiveResources(t *testing.T) {
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), bytes.Repeat([]byte("payload"), 100), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("compressed output", func(t *testing.T) {
		var archive bytes.Buffer
		_, err := writeConfigArchiveWithLimits(&archive, cfgDir, configArchiveLimits{
			maxOutputBytes:   1,
			maxExpandedBytes: 1 << 20,
			maxContentBytes:  1 << 20,
			maxEntries:       10,
		})
		if !errors.Is(err, errArchiveOutputLimit) {
			t.Fatalf("error=%v, want output limit", err)
		}
		if archive.Len() > 1 {
			t.Fatalf("bounded writer emitted %d bytes past 1-byte cap", archive.Len())
		}
	})

	t.Run("expanded tar stream", func(t *testing.T) {
		var archive bytes.Buffer
		_, err := writeConfigArchiveWithLimits(&archive, cfgDir, configArchiveLimits{
			maxOutputBytes:   1 << 20,
			maxExpandedBytes: 1,
			maxContentBytes:  1 << 20,
			maxEntries:       10,
		})
		if !errors.Is(err, errArchiveExpandedLimit) {
			t.Fatalf("error=%v, want expanded-stream limit", err)
		}
	})

	t.Run("extracted content", func(t *testing.T) {
		var archive bytes.Buffer
		_, err := writeConfigArchiveWithLimits(&archive, cfgDir, configArchiveLimits{
			maxOutputBytes:   1 << 20,
			maxExpandedBytes: 1 << 20,
			maxContentBytes:  10,
			maxEntries:       10,
		})
		if err == nil || !strings.Contains(err.Error(), "archive contents exceed") {
			t.Fatalf("error=%v, want content limit", err)
		}
	})

	t.Run("entry count", func(t *testing.T) {
		nestedDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(nestedDir, "history"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nestedDir, "history", "chat.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		var archive bytes.Buffer
		_, err := writeConfigArchiveWithLimits(&archive, nestedDir, configArchiveLimits{
			maxOutputBytes:   1 << 20,
			maxExpandedBytes: 1 << 20,
			maxContentBytes:  1 << 20,
			maxEntries:       1,
		})
		if err == nil || !strings.Contains(err.Error(), "too many entries") {
			t.Fatalf("error=%v, want entry limit", err)
		}
	})
}
