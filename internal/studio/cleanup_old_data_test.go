package studio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

func seedOldDelegationChild(t *testing.T, s *Studio, age time.Duration) (*Project, *ChatSession, DelegationRun) {
	t.Helper()
	completedAt := time.Now().Add(-age).UnixMilli()
	project := NewProject(ProjectConfig{ID: "cleanup-target", Name: "Target", Directory: t.TempDir()})
	project.studio = s
	child := NewChatSession("Delegation · Caller · Jan 02 15:04")
	child.ID = "delegation-child"
	child.delegateChild = true
	child.ArchivedAt = completedAt
	child.lastUsedAt = completedAt - 2_000
	if err := SaveNewHistoryWithMetadata(
		projectSessionStorageKey(project.ID, child.ID), child.Name, "", nil,
	); err != nil {
		t.Fatal(err)
	}
	history := historyPath(projectSessionStorageKey(project.ID, child.ID))
	beforeCompletion := time.UnixMilli(completedAt - 1_000)
	if err := os.Chtimes(history, beforeCompletion, beforeCompletion); err != nil {
		t.Fatal(err)
	}
	project.mu.Lock()
	project.sessions[child.ID] = child
	project.mu.Unlock()
	s.mu.Lock()
	s.projects[project.ID] = project
	s.mu.Unlock()
	run := DelegationRun{
		ID: "cleanup-run", Kind: "ask", Status: "completed",
		FromProjectID: "caller", FromSessionID: "default",
		ToProjectID: project.ID, ToSessionID: child.ID,
		Task: "old bounded question", StartedAt: completedAt - 60_000, CompletedAt: completedAt,
	}
	if _, err := appendDelegationRun(run); err != nil {
		t.Fatal(err)
	}
	return project, child, run
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

func TestCleanupOldData_FailedDeletesAreNotReportedAsRemoved(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	stale := seedStaleReplay(t, filepath.Join(cfgDir, "history"), "blocked.replay.jsonl", 10*24*time.Hour)
	snapshot := seedPreImportDir(t, filepath.Dir(cfgDir), "blocked", 31*24*time.Hour)

	previousRemoveFile, previousRemoveTree := cleanupRemoveFile, cleanupRemoveTree
	cleanupRemoveFile = func(string) error { return errors.New("injected file removal failure") }
	cleanupRemoveTree = func(string) error { return errors.New("injected tree removal failure") }
	t.Cleanup(func() {
		cleanupRemoveFile, cleanupRemoveTree = previousRemoveFile, previousRemoveTree
	})

	s := NewStudio()
	result, err := s.CleanupOldData(CleanupParams{ReplayAgeDays: 7, PreImportDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if result.StaleReplaysRemoved != 0 || result.PreImportDirsRemoved != 0 || result.BytesFreed != 0 {
		t.Fatalf("failed deletes were counted as successful: %+v", result)
	}
	if len(result.Errors) != 2 {
		t.Fatalf("cleanup errors=%v, want both failed operations", result.Errors)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("failed replay delete did not preserve its file: %v", err)
	}
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatalf("failed snapshot delete did not preserve its directory: %v", err)
	}
	foundWarning := false
	for _, entry := range s.GetRecentLogs() {
		if entry.Source == "cleanup" && entry.Level == "warn" && strings.Contains(entry.Message, "2 warning") {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("failed cleanup was not written to the event log: %+v", s.GetRecentLogs())
	}
}

func TestCleanupOldData_SerializesConcurrentSweeps(t *testing.T) {
	_ = withTempHistoryDir(t)
	stale := seedStaleReplay(t, filepath.Join(configDir(), "history"), "concurrent.replay.jsonl", 10*24*time.Hour)
	previousRemove := cleanupRemoveFile
	var removeCalls atomic.Int32
	removeStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRemove := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRemove()
	cleanupRemoveFile = func(path string) error {
		if removeCalls.Add(1) == 1 {
			close(removeStarted)
			<-release
		}
		return previousRemove(path)
	}
	t.Cleanup(func() { cleanupRemoveFile = previousRemove })

	type outcome struct {
		result *CleanupResult
		err    error
	}
	s := NewStudio()
	params := CleanupParams{ReplayAgeDays: 7}
	firstDone := make(chan outcome, 1)
	go func() {
		result, err := s.CleanupOldData(params)
		firstDone <- outcome{result: result, err: err}
	}()
	<-removeStarted
	secondStarted := make(chan struct{})
	secondDone := make(chan outcome, 1)
	go func() {
		close(secondStarted)
		result, err := s.CleanupOldData(params)
		secondDone <- outcome{result: result, err: err}
	}()
	<-secondStarted
	select {
	case second := <-secondDone:
		t.Fatalf("second cleanup bypassed sweep serialization: %+v, %v", second.result, second.err)
	case <-time.After(50 * time.Millisecond):
	}
	if calls := removeCalls.Load(); calls != 1 {
		t.Fatalf("concurrent sweeps attempted the same removal %d times", calls)
	}

	releaseRemove()
	first, second := <-firstDone, <-secondDone
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent cleanup errors: first=%v second=%v", first.err, second.err)
	}
	if first.result.StaleReplaysRemoved != 1 || second.result.StaleReplaysRemoved != 0 {
		t.Fatalf("concurrent cleanup results: first=%+v second=%+v", first.result, second.result)
	}
	if len(first.result.Errors) != 0 || len(second.result.Errors) != 0 || removeCalls.Load() != 1 {
		t.Fatalf("serialized cleanup produced duplicate work/errors: first=%+v second=%+v calls=%d", first.result, second.result, removeCalls.Load())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("serialized cleanup did not remove the stale replay: %v", err)
	}
}

func TestCleanupOldData_RemovesOldDelegationAfterChildChat(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	project, child, run := seedOldDelegationChild(t, s, 31*24*time.Hour)

	result, err := s.CleanupOldData(CleanupParams{DelegationAgeDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if result.DelegationRunsRemoved != 1 || result.DelegationRunsSkipped != 0 {
		t.Fatalf("unexpected delegation cleanup result: %+v", result)
	}
	if result.BytesFreed == 0 {
		t.Error("delegation record removal should contribute durable-store bytes")
	}
	project.mu.RLock()
	_, childExists := project.sessions[child.ID]
	project.mu.RUnlock()
	if childExists {
		t.Fatal("child chat still exists after its delegation record was removed")
	}
	if _, exists := mustLoadDelegationRun(t, run.ID); exists {
		t.Fatal("old delegation record still exists after child cleanup succeeded")
	}
	if _, err := os.Stat(historyPath(projectSessionStorageKey(project.ID, child.ID))); !os.IsNotExist(err) {
		t.Fatalf("child history was not removed: %v", err)
	}
}

func TestCleanupOldData_DelegationDryRunPreservesChatAndRecord(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	project, child, run := seedOldDelegationChild(t, s, 31*24*time.Hour)

	result, err := s.CleanupOldData(CleanupParams{DelegationAgeDays: 30, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.DelegationRunsRemoved != 1 || !result.DryRun {
		t.Fatalf("dry-run did not report the safe delegation candidate: %+v", result)
	}
	project.mu.RLock()
	_, childExists := project.sessions[child.ID]
	project.mu.RUnlock()
	if !childExists {
		t.Fatal("dry-run deleted the child chat")
	}
	if _, exists := mustLoadDelegationRun(t, run.ID); !exists {
		t.Fatal("dry-run deleted the delegation record")
	}
}

func TestCleanupOldData_RetainsProtectedDelegationAndDurableLink(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	project, child, run := seedOldDelegationChild(t, s, 31*24*time.Hour)
	if err := s.SaveDraft(project.ID, child.ID, "keep this follow-up"); err != nil {
		t.Fatal(err)
	}

	result, err := s.CleanupOldData(CleanupParams{DelegationAgeDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if result.DelegationRunsRemoved != 0 || result.DelegationRunsSkipped != 1 {
		t.Fatalf("protected delegation should be retained: %+v", result)
	}
	project.mu.RLock()
	_, childExists := project.sessions[child.ID]
	project.mu.RUnlock()
	if !childExists {
		t.Fatal("cleanup deleted a delegation chat with an unsent draft")
	}
	if _, exists := mustLoadDelegationRun(t, run.ID); !exists {
		t.Fatal("cleanup dropped the durable link for a protected child chat")
	}
}

func TestCleanupOldData_DoesNotStopActiveDelegationChat(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	project, child, run := seedOldDelegationChild(t, s, 31*24*time.Hour)
	child.mu.Lock()
	child.active = true
	child.mu.Unlock()

	result, err := s.CleanupOldData(CleanupParams{DelegationAgeDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if result.DelegationRunsRemoved != 0 || result.DelegationRunsSkipped != 1 {
		t.Fatalf("active delegation should be retained: %+v", result)
	}
	child.mu.RLock()
	stillActive := child.active
	child.mu.RUnlock()
	if !stillActive {
		t.Fatal("cleanup stopped the active child chat")
	}
	if _, exists := mustLoadDelegationRun(t, run.ID); !exists {
		t.Fatal("cleanup dropped the durable link for an active child chat")
	}
	project.mu.RLock()
	_, childExists := project.sessions[child.ID]
	project.mu.RUnlock()
	if !childExists {
		t.Fatal("cleanup deleted the active child chat")
	}
}

func TestCleanupOldData_RetainsDelegationHistoryChangedAfterCompletion(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	project, child, run := seedOldDelegationChild(t, s, 31*24*time.Hour)
	// Simulate a restart (lastUsedAt is ephemeral) followed by later activity.
	child.mu.Lock()
	child.lastUsedAt = 0
	child.mu.Unlock()
	now := time.Now()
	if err := os.Chtimes(historyPath(projectSessionStorageKey(project.ID, child.ID)), now, now); err != nil {
		t.Fatal(err)
	}

	result, err := s.CleanupOldData(CleanupParams{DelegationAgeDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if result.DelegationRunsRemoved != 0 || result.DelegationRunsSkipped != 1 {
		t.Fatalf("subsequently-used delegation should be retained: %+v", result)
	}
	if _, exists := mustLoadDelegationRun(t, run.ID); !exists {
		t.Fatal("cleanup dropped the durable link after later chat activity")
	}
}

func TestCleanupOldData_RetainsDelegationForArchivedProject(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	project, child, run := seedOldDelegationChild(t, s, 31*24*time.Hour)
	if err := s.ArchiveProject(project.ID); err != nil {
		t.Fatal(err)
	}

	preview, err := s.CleanupOldData(CleanupParams{DelegationAgeDays: 30, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if preview.DelegationRunsRemoved != 0 || preview.DelegationRunsSkipped != 1 {
		t.Fatalf("archived target should be protected in preview: %+v", preview)
	}
	result, err := s.CleanupOldData(CleanupParams{DelegationAgeDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if result.DelegationRunsRemoved != 0 || result.DelegationRunsSkipped != 1 {
		t.Fatalf("archived target should be protected during cleanup: %+v", result)
	}
	if _, exists := mustLoadDelegationRun(t, run.ID); !exists {
		t.Fatal("cleanup dropped the durable link for an archived project's child chat")
	}
	if _, err := os.Stat(historyPath(projectSessionStorageKey(project.ID, child.ID))); err != nil {
		t.Fatalf("cleanup removed archived child history: %v", err)
	}

	if _, err := s.RestoreProject(project.ID); err != nil {
		t.Fatal(err)
	}
	restored := s.projects[project.ID]
	restored.mu.RLock()
	_, childExists := restored.sessions[child.ID]
	restored.mu.RUnlock()
	if !childExists {
		t.Fatal("delegation child chat did not survive archive, cleanup, and restore")
	}
}

func TestCleanupOldData_RemovesOrphanedDelegationRecord(t *testing.T) {
	_ = withTempHistoryDir(t)
	completedAt := time.Now().Add(-31 * 24 * time.Hour).UnixMilli()
	if _, err := appendDelegationRun(DelegationRun{
		ID: "orphaned-run", Kind: "run", Status: "error",
		ToProjectID: "missing-project", ToSessionID: "missing-session",
		Task: "old task", StartedAt: completedAt - 1_000, CompletedAt: completedAt,
	}); err != nil {
		t.Fatal(err)
	}
	s := NewStudio()
	result, err := s.CleanupOldData(CleanupParams{DelegationAgeDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if result.DelegationRunsRemoved != 1 {
		t.Fatalf("orphaned durable row was not removed: %+v", result)
	}
	if _, exists := mustLoadDelegationRun(t, "orphaned-run"); exists {
		t.Fatal("orphaned delegation record still exists")
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
	_, err = s.CleanupOldData(CleanupParams{DelegationAgeDays: -1})
	if err == nil || !strings.Contains(err.Error(), "delegationAgeDays") {
		t.Errorf("expected delegationAgeDays validation error, got %v", err)
	}
}

func TestCleanupOldData_ZeroAgeSkipsCategory(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	histDir := filepath.Join(cfgDir, "history")
	// Very old replay — should normally be deleted.
	stale := seedStaleReplay(t, histDir, "x.replay.jsonl", 100*24*time.Hour)
	snapshot := seedPreImportDir(t, filepath.Dir(cfgDir), "zero-disabled", 100*24*time.Hour)

	s := NewStudio()
	// Zero → skip both age-gated categories entirely.
	result, err := s.CleanupOldData(CleanupParams{ReplayAgeDays: 0, PreImportDays: 0})
	if err != nil {
		t.Fatal(err)
	}
	if result.StaleReplaysRemoved != 0 {
		t.Errorf("ReplayAgeDays=0 should skip replays; got removed=%d", result.StaleReplaysRemoved)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("stale replay was removed despite ReplayAgeDays=0: %v", err)
	}
	if result.PreImportDirsRemoved != 0 {
		t.Errorf("PreImportDays=0 should skip snapshots; got removed=%d", result.PreImportDirsRemoved)
	}
	if _, err := os.Stat(snapshot); err != nil {
		t.Errorf("old snapshot was removed despite PreImportDays=0: %v", err)
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
