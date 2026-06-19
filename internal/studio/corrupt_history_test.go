package studio

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/genai"
)

// TestNewProject_QuarantinesCorruptHistory is the regression for the audit
// finding: a corrupt/unreadable session-history JSON used to be silently
// `continue`d during project load — the session tab vanished with no warning
// and the bad file kept shadowing the slot on every boot, never cleaned. Now
// the file is quarantined aside (slot freed, bytes preserved) and recorded for
// the event log.
func TestNewProject_QuarantinesCorruptHistory(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid := "proj-corrupt"

	// A healthy default session.
	hist := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hi")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
	}
	if err := SaveHistoryWithName(pid+"_default", "Chat 1", hist); err != nil {
		t.Fatalf("seed default: %v", err)
	}

	// A second session with a TRUNCATED/garbled history file (malformed JSON).
	if err := os.MkdirAll(historyDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	corruptPath := historyPath(pid + "_s2")
	if err := os.WriteFile(corruptPath, []byte(`{"version":1,"messages":[{"role":`), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewProject(ProjectConfig{ID: pid, Name: "Corrupt Test", Directory: t.TempDir()})

	// The valid session loads; the corrupt one does NOT become a live tab.
	if _, ok := p.sessions["default"]; !ok {
		t.Error("valid default session should load")
	}
	if _, ok := p.sessions["s2"]; ok {
		t.Error("corrupt session s2 must NOT be loaded as a live session")
	}

	// It was recorded for the event log.
	if len(p.corruptHistory) == 0 {
		t.Error("expected corruptHistory to record the quarantined session")
	}

	// The original .json was moved aside (so it stops shadowing the slot)...
	if _, err := os.Stat(corruptPath); !os.IsNotExist(err) {
		t.Error("corrupt .json should have been quarantined (renamed), but still exists")
	}
	// ...to a .corrupt-* file that ListHistoryFilesForProject won't re-match.
	matches, _ := filepath.Glob(corruptPath + ".corrupt-*")
	if len(matches) == 0 {
		t.Error("expected a .corrupt-* quarantine file preserving the bytes")
	}
	for _, sid := range ListHistoryFilesForProject(pid) {
		if sid == "s2" {
			t.Error("quarantined file must not be re-listed as session s2")
		}
	}
}
