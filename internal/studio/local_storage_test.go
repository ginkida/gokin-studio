package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeStoragePathsStayInsideConfigAndAvoidSanitizerCollisions(t *testing.T) {
	_ = withTempHistoryDir(t)
	base := configDir()
	unsafeID := "../../outside/project"
	paths := []string{
		sessionPinsPath(unsafeID),
		sessionOrderPath(unsafeID),
		draftPath(unsafeID, "../session"),
		pinsPath(unsafeID, "../session"),
	}
	for _, path := range paths {
		rel, err := filepath.Rel(base, path)
		if err != nil {
			t.Fatal(err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("storage path escaped config directory: %q", path)
		}
	}
	if safeStorageKey("a/b") == safeStorageKey("a_b") {
		t.Fatal("distinct unsafe IDs collapsed to the same storage key")
	}
	if got := safeStorageKey("uuid-style_123"); got != "uuid-style_123" {
		t.Fatalf("safe key changed unexpectedly: %q", got)
	}
}

func TestGetDraftRejectsOversizedAndSymlinkedFiles(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := &Studio{}

	t.Run("oversized", func(t *testing.T) {
		path := draftPath("project-large", "default")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Repeat("x", DraftMaxBytes+1)), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetDraft("project-large", "default"); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("expected oversized error, got %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(outside, []byte("secret draft"), 0600); err != nil {
			t.Fatal(err)
		}
		path := draftPath("project-link", "default")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetDraft("project-link", "default"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("expected symlink rejection, got %v", err)
		}
	})
}

func TestMetadataReadersRejectOversizedFiles(t *testing.T) {
	_ = withTempHistoryDir(t)
	checks := []struct {
		name string
		path string
		load func() error
	}{
		{"session pins", sessionPinsPath("project"), func() error { _, err := loadPinnedSessions("project"); return err }},
		{"session order", sessionOrderPath("project"), func() error { _, err := loadSessionOrder("project"); return err }},
		{"project order", projectOrderPath(), func() error { _, err := loadProjectOrder(); return err }},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(tc.path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tc.path, []byte(strings.Repeat("x", (256<<10)+1)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := tc.load(); err == nil || !strings.Contains(err.Error(), "too large") {
				t.Fatalf("expected oversized error, got %v", err)
			}
		})
	}
}
