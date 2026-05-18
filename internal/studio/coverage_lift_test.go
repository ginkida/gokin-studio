package studio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// coverage_lift_test.go (iter 640+) -- focused tests for the per-project
// file helpers that landed below their natural ceiling. Each test targets
// a specific branch (empty-pid noop, corrupt-JSON fallback, file-write
// success path) so coverage of the surrounding helper hits >= 90%.

// TestRemoveProjectSessionOrder_EmptyPID covers the empty-pid early-return.
func TestRemoveProjectSessionOrder_EmptyPID(t *testing.T) {
	// Just confirms no panic + no error. The helper returns void; we're
	// testing that the empty-pid branch executes without touching the FS.
	removeProjectSessionOrder("")
}

// TestRemoveProjectSessionPins_EmptyPID same shape for the pin file.
func TestRemoveProjectSessionPins_EmptyPID(t *testing.T) {
	removeProjectSessionPins("")
}

// TestLoadSessionOrder_CorruptJSONReturnsEmpty exercises the corrupt-file
// branch in loadSessionOrder. We treat malformed JSON as "no order set"
// so a single corrupt file doesn't break project loading.
func TestLoadSessionOrder_CorruptJSONReturnsEmpty(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	pid := "corrupt-test"
	path := sessionOrderPath(pid)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := loadSessionOrder(pid)
	if err != nil {
		t.Fatalf("loadSessionOrder should swallow corrupt JSON, got error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice for corrupt file, got %v", got)
	}
}

// TestLoadSessionOrder_DropsEmptyStrings ensures the defensive filter for
// empty IDs in the JSON array works. Some external tool might write an
// empty string into the array; we must not return it.
func TestLoadSessionOrder_DropsEmptyStrings(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	pid := "empty-strings-test"
	path := sessionOrderPath(pid)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Mix valid and empty IDs.
	data, _ := json.Marshal([]string{"a", "", "b", ""})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := loadSessionOrder(pid)
	if err != nil {
		t.Fatalf("loadSessionOrder: %v", err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("expected [a, b] (empty strings dropped), got %v", got)
	}
}

// TestLoadSessionOrder_EmptyFile treats a zero-byte file as no-order-set.
func TestLoadSessionOrder_EmptyFile(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	pid := "empty-file-test"
	path := sessionOrderPath(pid)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := loadSessionOrder(pid)
	if err != nil {
		t.Fatalf("loadSessionOrder: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice for empty file, got %v", got)
	}
}

// TestLoadPinnedSessions_EmptyPID covers the empty-pid early return path.
func TestLoadPinnedSessions_EmptyPID(t *testing.T) {
	got, err := loadPinnedSessions("")
	if err != nil {
		t.Fatalf("loadPinnedSessions(\"\"): %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("expected empty map for empty projectID, got %v", got)
	}
}

// TestLoadPinnedSessions_CorruptJSONReturnsEmpty same defensive behaviour
// as the order helper — corrupt files don't break the load.
func TestLoadPinnedSessions_CorruptJSONReturnsEmpty(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	pid := "corrupt-pins-test"
	path := sessionPinsPath(pid)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("[broken"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := loadPinnedSessions(pid)
	if err != nil {
		t.Fatalf("loadPinnedSessions corrupt: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map for corrupt file, got %v", got)
	}
}

// TestExportProjectUsageCSV_EmptyProjectAllSessions confirms the CSV
// includes the auto-created default session row even when no usage was
// recorded — matches the behaviour of ProjectUsageStats which lists
// every session, not just those with usage.
func TestExportProjectUsageCSV_EmptyProjectAllSessions(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "AllSessions")

	out, err := s.ExportProjectUsageCSV(info.ID)
	if err != nil {
		t.Fatalf("ExportProjectUsageCSV: %v", err)
	}
	// Should contain the default session name "Chat 1".
	if !strings.Contains(out, "Chat 1") {
		t.Errorf("expected 'Chat 1' (default session) in CSV, got: %s", out)
	}
	// And the TOTAL row.
	if !strings.Contains(out, "TOTAL,") {
		t.Errorf("expected TOTAL row in CSV, got: %s", out)
	}
}

// TestSaveSessionOrder_DirectoryCreation confirms the helper creates the
// session-order/ directory even when configDir() exists but the
// session-order subdir doesn't.
func TestSaveSessionOrder_DirectoryCreation(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	pid := "mkdir-test"
	if err := saveSessionOrder(pid, []string{"sess1", "sess2"}); err != nil {
		t.Fatalf("saveSessionOrder: %v", err)
	}
	if _, err := os.Stat(sessionOrderPath(pid)); err != nil {
		t.Fatalf("expected file written, stat err: %v", err)
	}
}
