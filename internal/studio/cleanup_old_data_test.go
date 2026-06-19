package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedStaleReplay writes a replay file with the given age. Returns full path.
func seedStaleReplay(t *testing.T, histDir, name string, age time.Duration) string {
	t.Helper()
	if err := os.MkdirAll(histDir, 0o755); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(histDir, name)
	if err := os.WriteFile(full, []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(full, when, when); err != nil {
		t.Fatal(err)
	}
	return full
}

// seedPreImportDir creates a sibling pre-import directory with the given
// age. Returns full path.
func seedPreImportDir(t *testing.T, parent, suffix string, age time.Duration) string {
	t.Helper()
	full := filepath.Join(parent, ".gokin-studio.pre-import-"+suffix)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	// Put something inside so dirSize reports > 0.
	if err := os.WriteFile(filepath.Join(full, "marker.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(full, when, when); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestCleanupOldData_RemovesStaleReplays(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	histDir := filepath.Join(cfgDir, "history")

	// 8-day-old replay → should be removed.
	stale := seedStaleReplay(t, histDir, "p_default.replay.jsonl", 8*24*time.Hour)
	// 1-day-old → should be kept.
	fresh := seedStaleReplay(t, histDir, "p_other.replay.jsonl", 1*24*time.Hour)
	// Non-replay file → never touched.
	other := filepath.Join(histDir, "p_default.json")
	if err := os.WriteFile(other, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewStudio()
	result, err := s.CleanupOldData(DefaultCleanupParams())
	if err != nil {
		t.Fatal(err)
	}
	if result.StaleReplaysRemoved != 1 {
		t.Errorf("StaleReplaysRemoved=%d, want 1", result.StaleReplaysRemoved)
	}
	if result.BytesFreed == 0 {
		t.Error("BytesFreed should be > 0")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file not removed: err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh file was incorrectly removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("non-replay file was touched: %v", err)
	}
}

func TestCleanupOldData_DryRunPreservesFiles(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	histDir := filepath.Join(cfgDir, "history")
	stale := seedStaleReplay(t, histDir, "x.replay.jsonl", 30*24*time.Hour)

	s := NewStudio()
	params := DefaultCleanupParams()
	params.DryRun = true
	result, err := s.CleanupOldData(params)
	if err != nil {
		t.Fatal(err)
	}
	if result.StaleReplaysRemoved != 1 {
		t.Errorf("DryRun should still COUNT what would be removed; got %d", result.StaleReplaysRemoved)
	}
	if !result.DryRun {
		t.Error("DryRun flag not propagated to result")
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("DryRun deleted the file (shouldn't): %v", err)
	}
}

func TestCleanupOldData_RemovesOldPreImportDirs(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	parent := filepath.Dir(cfgDir)

	// 31-day-old pre-import → should be removed.
	old := seedPreImportDir(t, parent, "20240101", 31*24*time.Hour)
	// 5-day-old → should be kept.
	recent := seedPreImportDir(t, parent, "20250105", 5*24*time.Hour)

	s := NewStudio()
	result, err := s.CleanupOldData(DefaultCleanupParams())
	if err != nil {
		t.Fatal(err)
	}
	if result.PreImportDirsRemoved != 1 {
		t.Errorf("PreImportDirsRemoved=%d, want 1", result.PreImportDirsRemoved)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old pre-import not removed: err=%v", err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("recent pre-import was incorrectly removed: %v", err)
	}
}

func TestCleanupOldData_RemovesOrphanStagingDirs(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	parent := filepath.Dir(cfgDir)

	// An ORPHANED staging dir (older than the grace window — a crashed import
	// from a while ago) should be cleared.
	staging := filepath.Join(parent, ".gokin-studio.import-staging-1234")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "partial.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-stagingGraceWindow - time.Hour)
	if err := os.Chtimes(staging, old, old); err != nil {
		t.Fatal(err)
	}

	s := NewStudio()
	result, err := s.CleanupOldData(DefaultCleanupParams())
	if err != nil {
		t.Fatal(err)
	}
	if result.StagingDirsRemoved != 1 {
		t.Errorf("StagingDirsRemoved=%d, want 1", result.StagingDirsRemoved)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging dir not removed: err=%v", err)
	}
}

// TestCleanupOldData_PreservesFreshStagingDir is the regression for the audit
// race: a freshly-created import-staging dir is the live extract target of an
// in-progress import. Cleanup must NOT sweep it within the grace window, or a
// manual Cleanup racing an active import could RemoveAll it mid-extract.
func TestCleanupOldData_PreservesFreshStagingDir(t *testing.T) {
	_ = withTempHistoryDir(t)
	parent := filepath.Dir(configDir())

	// Fresh staging dir (mtime ~now) — simulates an import currently extracting.
	staging := filepath.Join(parent, ".gokin-studio.import-staging-9999")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "partial.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewStudio()
	result, err := s.CleanupOldData(DefaultCleanupParams())
	if err != nil {
		t.Fatal(err)
	}
	if result.StagingDirsRemoved != 0 {
		t.Errorf("StagingDirsRemoved=%d, want 0 (a fresh staging dir may be a live import)", result.StagingDirsRemoved)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Errorf("fresh staging dir was swept (could abort a live import): %v", err)
	}
}

func TestCleanupOldData_ValidationRejectsNegative(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()

	_, err := s.CleanupOldData(CleanupParams{ReplayAgeDays: -1})
	if err == nil || !strings.Contains(err.Error(), "replayAgeDays") {
		t.Errorf("expected replayAgeDays validation error, got %v", err)
	}
	_, err = s.CleanupOldData(CleanupParams{PreImportDays: -1})
	if err == nil || !strings.Contains(err.Error(), "preImportDays") {
		t.Errorf("expected preImportDays validation error, got %v", err)
	}
}

func TestCleanupOldData_ZeroAgeSkipsCategory(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	histDir := filepath.Join(cfgDir, "history")
	// Very old replay — should normally be deleted.
	stale := seedStaleReplay(t, histDir, "x.replay.jsonl", 100*24*time.Hour)

	s := NewStudio()
	// ReplayAgeDays=0 → skip the replay category entirely.
	result, err := s.CleanupOldData(CleanupParams{ReplayAgeDays: 0, PreImportDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	if result.StaleReplaysRemoved != 0 {
		t.Errorf("ReplayAgeDays=0 should skip replays; got removed=%d", result.StaleReplaysRemoved)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("stale replay was removed despite ReplayAgeDays=0: %v", err)
	}
}

func TestCleanupOldData_MissingConfigDir(t *testing.T) {
	// Point at nonexistent dir.
	tmp := filepath.Join(t.TempDir(), "missing-config-dir")
	prev := os.Getenv("GOKIN_CONFIG_DIR")
	_ = os.Setenv("GOKIN_CONFIG_DIR", tmp)
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", prev) })

	s := NewStudio()
	result, err := s.CleanupOldData(DefaultCleanupParams())
	if err != nil {
		t.Fatalf("missing dir should not be an error, got %v", err)
	}
	if result.StaleReplaysRemoved != 0 || result.PreImportDirsRemoved != 0 {
		t.Errorf("missing dir should yield zero counts; got %+v", result)
	}
}

func TestCleanupPreviewDefaults_RoundTrip(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	histDir := filepath.Join(cfgDir, "history")
	seedStaleReplay(t, histDir, "old.replay.jsonl", 10*24*time.Hour)

	s := NewStudio()
	preview, err := s.CleanupPreviewDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if !preview.DryRun {
		t.Error("CleanupPreviewDefaults should set DryRun=true")
	}
	if preview.StaleReplaysRemoved != 1 {
		t.Errorf("preview count wrong: %+v", preview)
	}
	// File still on disk.
	if _, err := os.Stat(filepath.Join(histDir, "old.replay.jsonl")); err != nil {
		t.Errorf("preview should not delete; file gone: %v", err)
	}
}

func TestCleanupOldData_LogsToEventLog(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	histDir := filepath.Join(cfgDir, "history")
	seedStaleReplay(t, histDir, "old.replay.jsonl", 10*24*time.Hour)

	s := NewStudio()
	_, err := s.CleanupOldData(DefaultCleanupParams())
	if err != nil {
		t.Fatal(err)
	}
	// Should have logged the cleanup action.
	logs := s.GetRecentLogs()
	found := false
	for _, l := range logs {
		if l.Source == "cleanup" && strings.Contains(l.Message, "removed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("cleanup not logged to event log; logs=%+v", logs)
	}
}

func TestCleanupOldData_DryRunDoesNotLog(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	histDir := filepath.Join(cfgDir, "history")
	seedStaleReplay(t, histDir, "old.replay.jsonl", 10*24*time.Hour)

	s := NewStudio()
	params := DefaultCleanupParams()
	params.DryRun = true
	_, err := s.CleanupOldData(params)
	if err != nil {
		t.Fatal(err)
	}
	logs := s.GetRecentLogs()
	for _, l := range logs {
		if l.Source == "cleanup" {
			t.Errorf("dry run should not write event log; got %+v", l)
		}
	}
}
