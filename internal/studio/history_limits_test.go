package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestHistoryPathCannotEscapeStorageDirectory(t *testing.T) {
	_ = withTempHistoryDir(t)
	path := historyPath("../../outside/session")
	rel, err := filepath.Rel(historyDir(), path)
	if err != nil {
		t.Fatal(err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("history path escaped storage directory: %q", path)
	}
	if historyPath("a/b") == historyPath("a_b") {
		t.Fatal("distinct unsafe history keys collided")
	}
}

func TestUnsafeProjectIDHistoryRemainsDiscoverable(t *testing.T) {
	_ = withTempHistoryDir(t)
	projectID := "../../hand-edited/project"
	key := projectSessionStorageKey(projectID, "default")
	history := []*genai.Content{{
		Role:  "user",
		Parts: []*genai.Part{genai.NewPartFromText("survives restart")},
	}}
	if err := SaveHistoryWithName(key, "Recovered", history); err != nil {
		t.Fatal(err)
	}
	ids := ListHistoryFilesForProject(projectID)
	if len(ids) != 1 || ids[0] != "default" {
		t.Fatalf("discovered session IDs = %v, want [default]", ids)
	}
	p := NewProject(ProjectConfig{ID: projectID, Name: "P", Directory: t.TempDir()})
	session := p.GetSession("default")
	if session == nil {
		t.Fatal("safe history was not restored for unsafe project ID")
	}
	session.mu.RLock()
	entries := len(session.history)
	session.mu.RUnlock()
	if entries != 1 {
		t.Fatalf("restored history entries = %d, want 1", entries)
	}
	rel, err := filepath.Rel(historyDir(), replayPath(projectID, "default"))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("replay path escaped storage: rel=%q err=%v", rel, err)
	}
}

func TestLoadHistoryRejectsOversizedAndSymlinkedFiles(t *testing.T) {
	_ = withTempHistoryDir(t)
	if err := os.MkdirAll(historyDir(), 0700); err != nil {
		t.Fatal(err)
	}

	t.Run("oversized", func(t *testing.T) {
		path := historyPath("oversized")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(MaxHistoryFileBytes + 1); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadHistory("oversized"); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("expected size error, got %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte(`[{"role":"user","text":"secret"}]`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, historyPath("linked")); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadHistory("linked"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("expected symlink rejection, got %v", err)
		}
	})
}

func TestSaveHistoryRejectsExcessiveEntryCount(t *testing.T) {
	_ = withTempHistoryDir(t)
	history := make([]*genai.Content, MaxHistoryEntries+1)
	if err := SaveHistoryWithName("too-many", "Session", history); err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("expected entry-count error, got %v", err)
	}
	if _, err := os.Stat(historyPath("too-many")); !os.IsNotExist(err) {
		t.Fatalf("oversized history was persisted: %v", err)
	}
}

func TestLoadHistoryRejectsExcessiveEntryCount(t *testing.T) {
	_ = withTempHistoryDir(t)
	if err := os.MkdirAll(historyDir(), 0700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.Grow((MaxHistoryEntries + 1) * 3)
	b.WriteByte('[')
	for i := 0; i <= MaxHistoryEntries; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{}`)
	}
	b.WriteByte(']')
	if err := os.WriteFile(historyPath("too-many-load"), []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHistory("too-many-load"); err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Fatalf("expected entry-count error, got %v", err)
	}
}
