package studio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestArchiveRestorePreservesDataSuspendsSchedulesAndWake(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Archive me")
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()

	history := []*genai.Content{{
		Role: "user", Parts: []*genai.Part{genai.NewPartFromText("preserve this conversation")},
	}}
	if err := SaveHistoryWithName(projectSessionStorageKey(info.ID, "default"), "Preserved chat", history); err != nil {
		t.Fatal(err)
	}
	session := project.GetSession("default")
	session.mu.Lock()
	session.Name = "Preserved chat"
	session.history = history
	session.mu.Unlock()

	lease := &fakeWakeLease{}
	s.testWakeAcquire = func(string) (wakeLease, error) { return lease, nil }
	s.wakeEnabled.Store(true)
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Weekday routine",
		Prompt: "Review the project.", Schedule: "weekdays", TimeOfDay: "09:00",
		Enabled: true, Provider: "glm", Model: "glm-5.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !s.GetWakeStatus().Active {
		t.Fatal("enabled active-project schedule did not acquire wake lease")
	}

	if err := s.ArchiveProject(info.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.ListProjects()) != 0 || len(s.ListArchivedProjects()) != 1 {
		t.Fatalf("archive lists: active=%#v archived=%#v", s.ListProjects(), s.ListArchivedProjects())
	}
	if status := s.GetWakeStatus(); status.Active || status.ScheduledTasks || lease.closeCount() != 1 {
		t.Fatalf("archived wake status = %#v, closes=%d", status, lease.closeCount())
	}
	if cfg := LoadConfig(); len(cfg.Projects) != 0 {
		t.Fatalf("archived project remained active in config: %#v", cfg.Projects)
	}
	archived, err := loadArchivedProjectsRaw()
	if err != nil || archived[info.ID].Project.Name != "Archive me" {
		t.Fatalf("archived metadata = %#v, %v", archived, err)
	}
	if _, err := s.AddProject("Duplicate", info.Directory); err == nil || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("archived directory duplicate error = %v", err)
	}

	// Even a stale/corrupt due timestamp cannot dispatch while archived.
	scheduledTasksMu.Lock()
	tasks, err := loadScheduledTasksRaw()
	if err == nil {
		tasks[0].NextRunAt = time.Now().Add(-time.Minute).UnixMilli()
		err = saveScheduledTasksRaw(tasks)
	}
	scheduledTasksMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s.dispatchDueScheduledTasks(time.Now())
	runs, err := s.ListScheduledTaskRuns(info.ID, task.ID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("archived task dispatched: %#v, %v", runs, err)
	}

	beforeRestore := time.Now()
	restored, err := s.RestoreProject(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != info.ID || len(s.ListProjects()) != 1 || len(s.ListArchivedProjects()) != 0 {
		t.Fatalf("restore state: restored=%#v active=%#v archived=%#v", restored, s.ListProjects(), s.ListArchivedProjects())
	}
	restoredProject := s.projects[info.ID]
	restoredSession := restoredProject.GetSession("default")
	restoredSession.mu.RLock()
	gotHistory := restoredSession.history
	gotName := restoredSession.Name
	restoredSession.mu.RUnlock()
	if gotName != "Preserved chat" || len(gotHistory) != 1 || len(gotHistory[0].Parts) != 1 ||
		gotHistory[0].Parts[0].Text != "preserve this conversation" {
		t.Fatalf("restored session name=%q history=%#v", gotName, gotHistory)
	}
	tasks, err = s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || tasks[0].NextRunAt <= beforeRestore.UnixMilli() {
		t.Fatalf("restored schedule was not rebased: %#v, %v", tasks, err)
	}
	if !s.GetWakeStatus().Active {
		t.Fatal("restored automatic schedule did not reacquire wake lease")
	}
	if _, err := loadArchivedProjectsRaw(); err != nil {
		t.Fatal(err)
	}
	s.setWakeEnabled(false)
}

func TestArchiveProjectRejectsActiveRun(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Busy")
	session := s.projects[info.ID].GetSession("default")
	session.mu.Lock()
	session.active = true
	session.mu.Unlock()
	t.Cleanup(func() {
		session.mu.Lock()
		session.active = false
		session.mu.Unlock()
	})

	if err := s.ArchiveProject(info.ID); err == nil || !strings.Contains(err.Error(), "stop") {
		t.Fatalf("archive active error = %v", err)
	}
	if len(s.ListProjects()) != 1 || len(s.ListArchivedProjects()) != 0 {
		t.Fatalf("active archive mutated state: active=%#v archived=%#v", s.ListProjects(), s.ListArchivedProjects())
	}
}

func TestArchiveProjectRejectsClaimedQueueWorker(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Claimed")
	session := s.projects[info.ID].GetSession("default")
	session.mu.Lock()
	session.queueWorker = true
	session.mu.Unlock()
	t.Cleanup(func() {
		session.mu.Lock()
		session.queueWorker = false
		session.mu.Unlock()
	})

	if err := s.ArchiveProject(info.ID); err == nil || !strings.Contains(err.Error(), "stop") {
		t.Fatalf("archive claimed queue worker error = %v", err)
	}
	if len(s.ListProjects()) != 1 || len(s.ListArchivedProjects()) != 0 {
		t.Fatalf("claimed archive mutated state: active=%#v archived=%#v", s.ListProjects(), s.ListArchivedProjects())
	}
}

func TestStartupActiveProjectWinsArchiveCrashDuplicate(t *testing.T) {
	withTempConfigDir(t)
	dir := t.TempDir()
	pc := ProjectConfig{ID: "duplicate", Name: "Active wins", Directory: dir, Provider: "glm", Model: "glm-5.2"}
	cfg := defaultConfig()
	cfg.Projects = []ProjectConfig{pc}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := saveArchivedProjectsRaw(map[string]ArchivedProjectRecord{
		pc.ID: {Project: pc, ArchivedAt: time.Now().UnixMilli()},
	}); err != nil {
		t.Fatal(err)
	}

	s := NewStudio()
	s.Startup(context.Background())
	defer s.Shutdown(context.Background())
	if len(s.ListProjects()) != 1 || len(s.ListArchivedProjects()) != 0 {
		t.Fatalf("startup reconciliation: active=%#v archived=%#v", s.ListProjects(), s.ListArchivedProjects())
	}
	records, err := loadArchivedProjectsRaw()
	if err != nil || len(records) != 0 {
		t.Fatalf("reconciled archive file = %#v, %v", records, err)
	}
}

func TestArchivePersistenceFailureDoesNotHideProject(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Still active")
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := os.Getenv("GOKIN_CONFIG_DIR")
	if err := os.Setenv("GOKIN_CONFIG_DIR", blocked); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", previous) })

	if err := s.ArchiveProject(info.ID); err == nil {
		t.Fatal("archive unexpectedly succeeded with unwritable metadata path")
	}
	if len(s.ListProjects()) != 1 || len(s.ListArchivedProjects()) != 0 {
		t.Fatalf("failed archive mutated state: active=%#v archived=%#v", s.ListProjects(), s.ListArchivedProjects())
	}
	if _, err := s.GetProject(info.ID); err != nil {
		t.Fatalf("project unusable after failed archive: %v", err)
	}
}
