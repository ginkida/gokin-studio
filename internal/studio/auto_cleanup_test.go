package studio

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAutoCleanupParams_Conservative(t *testing.T) {
	p := AutoCleanupParams()
	if p.ReplayAgeDays != 30 {
		t.Errorf("ReplayAgeDays=%d, want 30 (more conservative than manual 7)", p.ReplayAgeDays)
	}
	if p.PreImportDays != 90 {
		t.Errorf("PreImportDays=%d, want 90 (more conservative than manual 30)", p.PreImportDays)
	}
	if p.DelegationAgeDays != 90 {
		t.Errorf("DelegationAgeDays=%d, want 90 (more conservative than manual 30)", p.DelegationAgeDays)
	}
	if p.DryRun {
		t.Error("DryRun must be false for actual cleanup")
	}
}

func TestShouldRunAutoCleanup_NoSentinel(t *testing.T) {
	_ = withTempHistoryDir(t)
	// Sentinel doesn't exist on a fresh config dir → should run.
	if !shouldRunAutoCleanup() {
		t.Error("expected shouldRunAutoCleanup=true when sentinel missing")
	}
}

func TestShouldRunAutoCleanup_FreshSentinelSkips(t *testing.T) {
	_ = withTempHistoryDir(t)
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	// Touch the sentinel now → recent → should NOT run.
	touchAutoCleanupSentinel()
	if shouldRunAutoCleanup() {
		t.Error("expected shouldRunAutoCleanup=false when sentinel is fresh")
	}
}

func TestShouldRunAutoCleanup_OldSentinelRuns(t *testing.T) {
	_ = withTempHistoryDir(t)
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	path := autoCleanupSentinelPath()
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	// Backdate to 25h ago — past the 24h throttle.
	old := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if !shouldRunAutoCleanup() {
		t.Error("expected shouldRunAutoCleanup=true when sentinel is older than 24h")
	}
}

func TestRunAutoCleanupIfDue_RunsAndTouchesSentinel(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	histDir := filepath.Join(cfgDir, "history")
	// Seed an aged replay file (35 days → past the conservative 30d threshold).
	stale := seedStaleReplay(t, histDir, "old.replay.jsonl", 35*24*time.Hour)

	s := NewStudio()
	s.config = defaultConfig()
	if err := s.RunAutoCleanupIfDue(); err != nil {
		t.Fatal(err)
	}
	// Stale replay removed.
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale replay not removed by auto-cleanup: err=%v", err)
	}
	// Sentinel created.
	if _, err := os.Stat(autoCleanupSentinelPath()); err != nil {
		t.Errorf("sentinel not created: %v", err)
	}
}

func TestRunAutoCleanupIfDue_KeepsConservativeThreshold(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	histDir := filepath.Join(cfgDir, "history")
	// Seed an 8-day-old replay → would be removed by MANUAL defaults but
	// NOT by conservative auto defaults (30d threshold).
	survivor := seedStaleReplay(t, histDir, "eight-days.replay.jsonl", 8*24*time.Hour)

	s := NewStudio()
	s.config = defaultConfig()
	if err := s.RunAutoCleanupIfDue(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(survivor); err != nil {
		t.Errorf("8-day replay should survive auto-cleanup's 30d threshold; err=%v", err)
	}
}

func TestRunAutoCleanupIfDue_DisabledByFlag(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	histDir := filepath.Join(cfgDir, "history")
	// Even with a very old replay, AutoCleanupDisabled should skip the pass.
	survivor := seedStaleReplay(t, histDir, "ancient.replay.jsonl", 365*24*time.Hour)

	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.AutoCleanupDisabled = true
	if err := s.RunAutoCleanupIfDue(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(survivor); err != nil {
		t.Errorf("ancient replay should survive when AutoCleanupDisabled=true; err=%v", err)
	}
	// Sentinel should NOT be touched when disabled.
	if _, err := os.Stat(autoCleanupSentinelPath()); !os.IsNotExist(err) {
		t.Errorf("sentinel should not be created when auto-cleanup disabled; err=%v", err)
	}
}

func TestRunAutoCleanupIfDue_ThrottleSkipsRecentRun(t *testing.T) {
	_ = withTempHistoryDir(t)
	cfgDir := configDir()
	histDir := filepath.Join(cfgDir, "history")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Sentinel exists and is fresh → throttle should skip the work.
	touchAutoCleanupSentinel()
	survivor := seedStaleReplay(t, histDir, "old.replay.jsonl", 100*24*time.Hour)

	s := NewStudio()
	s.config = defaultConfig()
	if err := s.RunAutoCleanupIfDue(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(survivor); err != nil {
		t.Errorf("100-day replay should survive when throttle skips run; err=%v", err)
	}
}

func TestRunAutoCleanupIfDue_CoalescesConcurrentTriggers(t *testing.T) {
	_ = withTempHistoryDir(t)
	stale := seedStaleReplay(t, filepath.Join(configDir(), "history"), "concurrent-auto.replay.jsonl", 35*24*time.Hour)
	s := NewStudio()
	s.config = defaultConfig()

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

	firstDone := make(chan error, 1)
	go func() { firstDone <- s.RunAutoCleanupIfDue() }()
	<-removeStarted
	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- s.RunAutoCleanupIfDue()
	}()
	<-secondStarted
	select {
	case err := <-secondDone:
		t.Fatalf("second auto-cleanup bypassed trigger serialization: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseRemove()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if calls := removeCalls.Load(); calls != 1 {
		t.Fatalf("coalesced auto-cleanup attempted removal %d times", calls)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("auto-cleanup did not remove stale replay: %v", err)
	}
	if shouldRunAutoCleanup() {
		t.Fatal("coalesced auto-cleanup did not advance the throttle sentinel")
	}
}

func TestRunAutoCleanupIfDue_LogsWhenNothingToRemove(t *testing.T) {
	_ = withTempHistoryDir(t)
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		t.Fatal(err)
	}

	s := NewStudio()
	s.config = defaultConfig()
	if err := s.RunAutoCleanupIfDue(); err != nil {
		t.Fatal(err)
	}
	// Should have logged "nothing to remove" at info level.
	found := false
	for _, l := range s.GetRecentLogs() {
		if l.Source == "cleanup" && l.Level == "info" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected info-level cleanup log when nothing was removed; got logs=%+v", s.GetRecentLogs())
	}
}

func TestTouchAutoCleanupSentinel_CreatesFile(t *testing.T) {
	_ = withTempHistoryDir(t)
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	touchAutoCleanupSentinel()
	info, err := os.Stat(autoCleanupSentinelPath())
	if err != nil {
		t.Fatalf("sentinel not created: %v", err)
	}
	// Mtime should be very recent (within 5 seconds).
	if time.Since(info.ModTime()) > 5*time.Second {
		t.Errorf("sentinel mtime is stale: %v", info.ModTime())
	}
}

func TestTouchAutoCleanupSentinel_UpdatesExisting(t *testing.T) {
	_ = withTempHistoryDir(t)
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	path := autoCleanupSentinelPath()
	// Create a backdated sentinel.
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	touchAutoCleanupSentinel()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("sentinel disappeared: %v", err)
	}
	if time.Since(info.ModTime()) > 5*time.Second {
		t.Errorf("touch did not update mtime; got %v", info.ModTime())
	}
}
