package studio

import (
	"errors"
	"sync"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

type fakeWakeLease struct {
	mu     sync.Mutex
	closed int
}

func (l *fakeWakeLease) Close() error {
	l.mu.Lock()
	l.closed++
	l.mu.Unlock()
	return nil
}

func (l *fakeWakeLease) closeCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

func TestWakeManagerReferenceCountsRunsAndScheduledNeed(t *testing.T) {
	s := newStudioForTest(t)
	acquires := 0
	lease := &fakeWakeLease{}
	s.testWakeAcquire = func(reason string) (wakeLease, error) {
		acquires++
		if reason == "" {
			t.Fatal("wake reason is empty")
		}
		return lease, nil
	}
	s.setWakeEnabled(true)
	if status := s.GetWakeStatus(); status.Active || status.ActiveRuns != 0 {
		t.Fatalf("idle wake status = %#v", status)
	}

	releaseFirst := s.beginWakeRun()
	releaseSecond := s.beginWakeRun()
	if status := s.GetWakeStatus(); !status.Active || status.ActiveRuns != 2 || acquires != 1 {
		t.Fatalf("active wake status = %#v, acquires=%d", status, acquires)
	}
	releaseFirst()
	releaseFirst() // idempotent
	if lease.closeCount() != 0 {
		t.Fatal("shared wake lease closed while another run was active")
	}
	s.wakeScheduled.Store(true)
	s.wakeMu.Lock()
	s.reconcileWakeLocked()
	s.wakeMu.Unlock()
	releaseSecond()
	if status := s.GetWakeStatus(); !status.Active || !status.ScheduledTasks || status.ActiveRuns != 0 {
		t.Fatalf("scheduled wake status = %#v", status)
	}
	s.wakeScheduled.Store(false)
	s.wakeMu.Lock()
	s.reconcileWakeLocked()
	s.wakeMu.Unlock()
	if status := s.GetWakeStatus(); status.Active || lease.closeCount() != 1 {
		t.Fatalf("released wake status = %#v, closes=%d", status, lease.closeCount())
	}
}

func TestWakeManagerReportsAcquireFailureAndDisableReleases(t *testing.T) {
	s := newStudioForTest(t)
	s.testWakeAcquire = func(string) (wakeLease, error) {
		return nil, errors.New("inhibitor unavailable")
	}
	s.setWakeEnabled(true)
	release := s.beginWakeRun()
	status := s.GetWakeStatus()
	if status.Active || status.Error != "inhibitor unavailable" {
		t.Fatalf("failed wake status = %#v", status)
	}
	release()

	lease := &fakeWakeLease{}
	s.testWakeAcquire = func(string) (wakeLease, error) { return lease, nil }
	release = s.beginWakeRun()
	if !s.GetWakeStatus().Active {
		t.Fatal("wake lease was not acquired after retry")
	}
	s.setWakeEnabled(false)
	if s.GetWakeStatus().Active || lease.closeCount() != 1 {
		t.Fatalf("disable did not close lease: %#v closes=%d", s.GetWakeStatus(), lease.closeCount())
	}
	release()
}

func TestScheduledTaskStateDrivesWakeLease(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	lease := &fakeWakeLease{}
	s.testWakeAcquire = func(string) (wakeLease, error) { return lease, nil }
	s.setWakeEnabled(true)
	if err := saveScheduledTasksRaw([]ScheduledTask{{ID: "task", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshScheduledWakeNeed(); err != nil {
		t.Fatal(err)
	}
	if status := s.GetWakeStatus(); !status.Active || !status.ScheduledTasks {
		t.Fatalf("enabled scheduled task wake status = %#v", status)
	}
	if err := saveScheduledTasksRaw(nil); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshScheduledWakeNeed(); err != nil {
		t.Fatal(err)
	}
	if status := s.GetWakeStatus(); status.Active || status.ScheduledTasks || lease.closeCount() != 1 {
		t.Fatalf("cleared scheduled task wake status = %#v, closes=%d", status, lease.closeCount())
	}
}

func TestManualScheduledTaskDoesNotHoldWakeLease(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	s.wakeEnabled.Store(true)
	acquires := 0
	s.testWakeAcquire = func(string) (wakeLease, error) {
		acquires++
		return &fakeWakeLease{}, nil
	}

	scheduledTasksMu.Lock()
	err := saveScheduledTasksRaw([]ScheduledTask{{
		ID: "manual", ProjectID: "project", SessionID: "default",
		Name: "Manual", Prompt: "Run only when requested.", Schedule: "manual", Enabled: true,
	}})
	scheduledTasksMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.refreshScheduledWakeNeed(); err != nil {
		t.Fatal(err)
	}
	status := s.GetWakeStatus()
	if status.Active || status.ScheduledTasks || acquires != 0 {
		t.Fatalf("manual task incorrectly requested wake: %#v, acquires=%d", status, acquires)
	}
}

func TestWakeSettingPersistsAndReconcilesScheduledLease(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	lease := &fakeWakeLease{}
	s.testWakeAcquire = func(string) (wakeLease, error) { return lease, nil }
	if err := saveScheduledTasksRaw([]ScheduledTask{{ID: "task", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshScheduledWakeNeed(); err != nil {
		t.Fatal(err)
	}
	cfg := *s.GetSettings()
	cfg.Settings.KeepAwakeEnabled = true
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatal(err)
	}
	if !s.GetSettings().Settings.KeepAwakeEnabled || !s.GetWakeStatus().Active {
		t.Fatalf("enabled persisted wake status = %#v", s.GetWakeStatus())
	}
	reloaded := LoadConfig()
	if !reloaded.Settings.KeepAwakeEnabled {
		t.Fatal("keep-awake preference was not persisted")
	}
	cfg = *s.GetSettings()
	cfg.Settings.KeepAwakeEnabled = false
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatal(err)
	}
	if s.GetWakeStatus().Active || lease.closeCount() != 1 {
		t.Fatalf("disabled wake status = %#v, closes=%d", s.GetWakeStatus(), lease.closeCount())
	}
}

func TestAgentLoopHoldsWakeLeaseForAcceptedRun(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "awake"}}}
	reg := tools.NewRegistry()
	p, _ := newTestProject(t, mc, reg)
	s := newStudioForTest(t)
	lease := &fakeWakeLease{}
	s.testWakeAcquire = func(string) (wakeLease, error) { return lease, nil }
	s.setWakeEnabled(true)
	p.studio = s

	runAgent(p, "keep this run awake")

	if lease.closeCount() != 1 {
		t.Fatalf("agent wake lease closes=%d, want 1", lease.closeCount())
	}
	if status := s.GetWakeStatus(); status.Active || status.ActiveRuns != 0 {
		t.Fatalf("agent left wake state active: %#v", status)
	}
}
