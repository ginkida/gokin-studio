//go:build !windows

package studio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRestore_UnlinksPrivateCandidateWhileDescriptorIsOpen(t *testing.T) {
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
	candidate := s.nativeRestoreCandidate
	s.nativeRestoreMu.Unlock()
	if candidate == nil {
		t.Fatal("candidate missing after selection")
	}
	if candidate.path != "" {
		t.Fatalf("candidate path=%q, expected immediate unlink", candidate.path)
	}
	if _, err := candidate.file.Stat(); err != nil {
		t.Fatalf("unlinked descriptor is not readable: %v", err)
	}
	if err := s.DiscardSelectedRestoreArchive(review.Token); err != nil {
		t.Fatal(err)
	}
}
