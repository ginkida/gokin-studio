package studio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNextScheduledRunIntervalDailyWeekdaysWeeklyManual(t *testing.T) {
	base := time.Date(2026, time.July, 29, 10, 30, 0, 0, time.Local)

	interval := nextScheduledRun(ScheduledTask{
		Schedule: "interval", IntervalMinutes: 90,
	}, base)
	if want := base.Add(90 * time.Minute); !interval.Equal(want) {
		t.Fatalf("interval next = %v, want %v", interval, want)
	}

	daily := nextScheduledRun(ScheduledTask{
		Schedule: "daily", TimeOfDay: "09:15",
	}, base)
	wantDaily := time.Date(2026, time.July, 30, 9, 15, 0, 0, time.Local)
	if !daily.Equal(wantDaily) {
		t.Fatalf("daily next = %v, want %v", daily, wantDaily)
	}

	weekdaySameDay := nextScheduledRun(ScheduledTask{
		Schedule: "weekdays", TimeOfDay: "11:00",
	}, base)
	wantWeekdaySameDay := time.Date(2026, time.July, 29, 11, 0, 0, 0, time.Local)
	if !weekdaySameDay.Equal(wantWeekdaySameDay) {
		t.Fatalf("weekday same-day next = %v, want %v", weekdaySameDay, wantWeekdaySameDay)
	}

	fridayAfterRun := time.Date(2026, time.July, 31, 17, 30, 0, 0, time.Local)
	weekdayAfterWeekend := nextScheduledRun(ScheduledTask{
		Schedule: "weekdays", TimeOfDay: "09:00",
	}, fridayAfterRun)
	wantMonday := time.Date(2026, time.August, 3, 9, 0, 0, 0, time.Local)
	if !weekdayAfterWeekend.Equal(wantMonday) {
		t.Fatalf("weekday weekend next = %v, want %v", weekdayAfterWeekend, wantMonday)
	}

	weekly := nextScheduledRun(ScheduledTask{
		Schedule: "weekly", TimeOfDay: "11:00", Weekday: int(base.Weekday()),
	}, base)
	wantWeekly := time.Date(2026, time.July, 29, 11, 0, 0, 0, time.Local)
	if !weekly.Equal(wantWeekly) {
		t.Fatalf("weekly next = %v, want %v", weekly, wantWeekly)
	}

	if manual := nextScheduledRun(ScheduledTask{Schedule: "manual"}, base); !manual.IsZero() {
		t.Fatalf("manual next = %v, want zero", manual)
	}
}

func TestScheduledTaskWeekdaysAndManualValidation(t *testing.T) {
	now := time.Date(2026, time.July, 31, 17, 30, 0, 0, time.Local)

	weekdays, err := validateScheduledTask(ScheduledTask{
		ProjectID: "project", SessionID: "default", Prompt: "Weekday briefing.",
		Schedule: "weekdays", TimeOfDay: "09:00", Enabled: true,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	wantMonday := time.Date(2026, time.August, 3, 9, 0, 0, 0, time.Local).UnixMilli()
	if weekdays.NextRunAt != wantMonday || weekdays.IntervalMinutes != 0 || weekdays.Weekday != 0 {
		t.Fatalf("weekdays task = %#v, want next Monday %d", weekdays, wantMonday)
	}

	manual, err := validateScheduledTask(ScheduledTask{
		ProjectID: "project", SessionID: "default", Prompt: "Run on demand.",
		Schedule: "manual", IntervalMinutes: 60, TimeOfDay: "09:00", Weekday: 4, Enabled: true,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if manual.NextRunAt != 0 || manual.IntervalMinutes != 0 || manual.TimeOfDay != "" || manual.Weekday != 0 {
		t.Fatalf("manual task = %#v", manual)
	}
	acceptEdits, err := validateScheduledTask(ScheduledTask{
		ProjectID: "project", SessionID: "default", Prompt: "Edit safely.",
		Schedule: "manual", Enabled: true, ApprovalMode: "acceptEdits",
	}, now)
	if err != nil || acceptEdits.ApprovalMode != "accept_edits" {
		t.Fatalf("Accept edits task = %#v, %v", acceptEdits, err)
	}
}

func TestManualScheduledTaskNeverAutoDispatches(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Manual schedule")
	now := time.Now()
	task := ScheduledTask{
		ID: "manual-task", ProjectID: info.ID, SessionID: "default",
		Name: "On demand", Prompt: "Only run when requested.", Schedule: "manual",
		Enabled: true, CreatedAt: now.Add(-time.Hour).UnixMilli(),
		// A corrupt or old persisted timestamp must not make manual work due.
		NextRunAt: now.Add(-time.Minute).UnixMilli(),
	}
	scheduledTasksMu.Lock()
	if err := saveScheduledTasksRaw([]ScheduledTask{task}); err != nil {
		scheduledTasksMu.Unlock()
		t.Fatal(err)
	}
	scheduledTasksMu.Unlock()

	s.dispatchDueScheduledTasks(now)

	runs, err := s.ListScheduledTaskRuns(info.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("manual task auto-dispatched: %#v", runs)
	}
}

func TestScheduledTaskCRUDAndValidation(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled")

	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID,
		SessionID: "default",
		Name:      "Daily review",
		Prompt:    "Review the repository and summarize new risks.",
		Schedule:  "daily",
		TimeOfDay: "09:30",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("SaveScheduledTask: %v", err)
	}
	if task.ID == "" || task.NextRunAt <= time.Now().UnixMilli() {
		t.Fatalf("saved task = %#v", task)
	}
	if task.Provider != "glm" || task.Model != "glm-5.2" || task.ApprovalMode != "manual" {
		t.Fatalf("task execution defaults = %#v", task)
	}

	list, err := s.ListScheduledTasks(info.ID)
	if err != nil || len(list) != 1 || list[0].ID != task.ID {
		t.Fatalf("ListScheduledTasks = %#v, %v", list, err)
	}
	task.Enabled = false
	task.Name = "Paused review"
	updated, err := s.SaveScheduledTask(task)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || updated.CreatedAt != task.CreatedAt {
		t.Fatalf("updated task = %#v", updated)
	}

	bad := task
	bad.ID = ""
	bad.Schedule = "interval"
	bad.IntervalMinutes = minScheduleIntervalMins - 1
	if _, err := s.SaveScheduledTask(bad); err == nil || !strings.Contains(err.Error(), "interval") {
		t.Fatalf("invalid interval error = %v", err)
	}

	if err := s.DeleteScheduledTask(info.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	list, err = s.ListScheduledTasks(info.ID)
	if err != nil || len(list) != 0 {
		t.Fatalf("after delete = %#v, %v", list, err)
	}
}

func TestScheduledTaskDueWhileSourceBusyCreatesIndependentRunSession(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled queue")
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	rec := &recorder{}
	p.testEmitter = rec.emit
	session := p.GetSession("default")
	session.mu.Lock()
	session.queueWorker = true
	session.mu.Unlock()

	now := time.Now()
	task := ScheduledTask{
		ID:              "due-task",
		ProjectID:       info.ID,
		SessionID:       "default",
		Name:            "Risk scan",
		Prompt:          "Scan for regressions.",
		Schedule:        "interval",
		IntervalMinutes: 60,
		Enabled:         true,
		CreatedAt:       now.Add(-time.Hour).UnixMilli(),
		NextRunAt:       now.Add(-time.Minute).UnixMilli(),
	}
	scheduledTasksMu.Lock()
	if err := saveScheduledTasksRaw([]ScheduledTask{task}); err != nil {
		scheduledTasksMu.Unlock()
		t.Fatal(err)
	}
	scheduledTasksMu.Unlock()

	s.dispatchDueScheduledTasks(now)

	session.mu.RLock()
	if len(session.queuedTurns) != 0 {
		session.mu.RUnlock()
		t.Fatalf("queued turns = %#v", session.queuedTurns)
	}
	session.mu.RUnlock()

	list, err := s.ListScheduledTasks(info.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListScheduledTasks = %#v, %v", list, err)
	}
	if list[0].LastStatus == "queued" || list[0].LastStatus == "" || list[0].LastRunAt == 0 || list[0].NextRunAt <= now.UnixMilli() {
		t.Fatalf("task status after dispatch = %#v", list[0])
	}
	runs, err := s.ListScheduledTaskRuns(info.ID, task.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %#v, %v", runs, err)
	}
	runSession := p.GetSession(runs[0].SessionID)
	if runSession == nil || runSession.ID == task.SessionID || runSession.ParentID != task.SessionID {
		t.Fatalf("independent run session = %#v", runSession)
	}
	runSession.mu.RLock()
	runProvider, runModel, runApproval := runSession.executionProvider, runSession.executionModel, runSession.executionPermissionMode
	runSession.mu.RUnlock()
	if runProvider != "glm" || runModel != "glm-5.2" || runApproval != "manual" {
		t.Fatalf("run overrides = %q/%q %q", runProvider, runModel, runApproval)
	}
	if changed := rec.find(EventSessionsChanged); len(changed) < 1 {
		t.Fatalf("sessions-changed events = %d", len(changed))
	}
}

func TestScheduledTaskRunRetentionReconcileAndDelete(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled retention")
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Retained",
		Prompt: "Check.", Schedule: "daily", TimeOfDay: "09:00", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxRunsPerScheduledTask+5; i++ {
		if _, err := appendScheduledTaskRun(ScheduledTaskRun{
			ID: fmt.Sprintf("run-%02d", i), TaskID: task.ID, ProjectID: info.ID,
			SessionID: "default", StartedAt: int64(i + 1), Status: "completed",
			Provider: "glm", Model: "glm-5.2", ApprovalMode: "manual",
		}); err != nil {
			t.Fatal(err)
		}
	}
	finishScheduledTaskRun("run-54", "running", nil)
	s.updateScheduledTaskResult(task.ID, time.Now(), "running", nil)
	s.reconcileInterruptedScheduledRuns()

	runs, err := s.ListScheduledTaskRuns(info.ID, task.ID)
	if err != nil || len(runs) != maxRunsPerScheduledTask {
		t.Fatalf("retained runs = %d, %v", len(runs), err)
	}
	if runs[0].ID != "run-54" || runs[0].Status != "stopped" || runs[0].CompletedAt == 0 {
		t.Fatalf("newest reconciled run = %#v", runs[0])
	}
	tasks, err := s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || tasks[0].LastStatus != "stopped" {
		t.Fatalf("reconciled task = %#v, %v", tasks, err)
	}
	if err := s.DeleteScheduledTask(info.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	runs, err = s.ListScheduledTaskRuns(info.ID, task.ID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs after task deletion = %#v, %v", runs, err)
	}
}

func addScheduledRunChat(t *testing.T, s *Studio, projectID string, task ScheduledTask, status string) *ChatSession {
	t.Helper()
	s.mu.RLock()
	project := s.projects[projectID]
	s.mu.RUnlock()
	if project == nil {
		t.Fatalf("project %q is not loaded", projectID)
	}
	session, err := createScheduledRunSession(project, task, time.Now())
	if err != nil {
		t.Fatalf("create scheduled run session: %v", err)
	}
	if _, err := appendScheduledTaskRun(ScheduledTaskRun{
		ID: uuidForScheduledTest(t), TaskID: task.ID, ProjectID: projectID,
		SessionID: session.ID, StartedAt: time.Now().UnixMilli(), Status: status,
		Provider: task.Provider, Model: task.Model, ApprovalMode: task.ApprovalMode,
	}); err != nil {
		t.Fatalf("append scheduled run: %v", err)
	}
	return session
}

func uuidForScheduledTest(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("test-run-%s-%d", strings.ReplaceAll(t.Name(), "/", "-"), time.Now().UnixNano())
}

func TestDeleteScheduledTaskPreservesRunChatAndStopsActiveRun(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled preserve")
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Preserve run chat",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
		Provider: "glm", Model: "glm-5.2", ApprovalMode: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	runSession := addScheduledRunChat(t, s, info.ID, task, "running")
	ctx, cancel := context.WithCancel(context.Background())
	runSession.mu.Lock()
	runSession.active = true
	runSession.queueWorker = true
	runSession.cancelFn = cancel
	runSession.mu.Unlock()

	preview, err := s.GetScheduledTaskDeletionPreview(info.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.RunCount != 1 || preview.RunChatCount != 1 || preview.ActiveRunCount != 1 || preview.ProtectedRunChats != 0 {
		t.Fatalf("deletion preview = %+v", preview)
	}
	if err := s.DeleteScheduledTask(info.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("active scheduled run was not cancelled")
	}
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	project.mu.RLock()
	kept := project.sessions[runSession.ID]
	project.mu.RUnlock()
	if kept == nil {
		t.Fatal("safe task deletion removed its run chat")
	}
	if _, err := os.Stat(historyPath(projectSessionStorageKey(info.ID, runSession.ID))); err != nil {
		t.Fatalf("preserved run history: %v", err)
	}
}

func TestDeleteScheduledTaskWithDataRemovesOwnedRunChatsOnly(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled destructive cleanup")
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Remove run data",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
		Provider: "glm", Model: "glm-5.2", ApprovalMode: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	first := addScheduledRunChat(t, s, info.ID, task, "completed")
	second := addScheduledRunChat(t, s, info.ID, task, "stopped")
	// A corrupt row pointing at the source chat must be counted as protected,
	// never interpreted as authority to delete that chat.
	if _, err := appendScheduledTaskRun(ScheduledTaskRun{
		ID: "corrupt-source-row", TaskID: task.ID, ProjectID: info.ID,
		SessionID: "default", StartedAt: time.Now().UnixMilli(), Status: "completed",
		Provider: "glm", Model: "glm-5.2", ApprovalMode: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	preview, err := s.GetScheduledTaskDeletionPreview(info.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.RunCount != 3 || preview.RunChatCount != 2 || preview.ProtectedRunChats != 1 {
		t.Fatalf("deletion preview = %+v", preview)
	}
	if err := s.DeleteScheduledTaskWithData(info.ID, task.ID, true); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	project.mu.RLock()
	_, firstExists := project.sessions[first.ID]
	_, secondExists := project.sessions[second.ID]
	source := project.sessions["default"]
	project.mu.RUnlock()
	if firstExists || secondExists || source == nil {
		t.Fatalf("run cleanup left first=%v second=%v source=%v", firstExists, secondExists, source != nil)
	}
	for _, id := range []string{first.ID, second.ID} {
		if _, err := os.Stat(historyPath(projectSessionStorageKey(info.ID, id))); !os.IsNotExist(err) {
			t.Fatalf("run history %q still exists: %v", id, err)
		}
	}
	if _, err := os.Stat(historyPath(projectSessionStorageKey(info.ID, "default"))); err != nil {
		t.Fatalf("source chat history was removed: %v", err)
	}
}

func TestDeleteScheduledTaskWithDataPreflightKeepsEverythingOnDirtyWorktree(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	repo := prepareSessionWorktreeRepo(t)
	info, err := s.AddProject("Scheduled dirty cleanup", repo)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Dirty run",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
		Provider: "glm", Model: "glm-5.2", ApprovalMode: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	runSession := addScheduledRunChat(t, s, info.ID, task, "completed")
	runSession.mu.RLock()
	worktree := runSession.WorktreePath
	runSession.mu.RUnlock()
	if worktree == "" {
		t.Fatal("scheduled run did not receive an isolated worktree")
	}
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = s.DeleteScheduledTaskWithData(info.ID, task.ID, true)
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty worktree deletion error = %v", err)
	}
	tasks, listErr := s.ListScheduledTasks(info.ID)
	if listErr != nil || len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("task changed after failed preflight: %#v, %v", tasks, listErr)
	}
	runs, listErr := s.ListScheduledTaskRuns(info.ID, task.ID)
	if listErr != nil || len(runs) != 1 {
		t.Fatalf("run index changed after failed preflight: %#v, %v", runs, listErr)
	}
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	project.mu.RLock()
	kept := project.sessions[runSession.ID]
	project.mu.RUnlock()
	if kept == nil {
		t.Fatal("run chat was deleted despite failed preflight")
	}
}

func TestDeleteSessionRemovesItsScheduledTasks(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled cleanup")
	session, err := s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID:       info.ID,
		SessionID:       session.ID,
		Name:            "Temporary session task",
		Prompt:          "Check this branch.",
		Schedule:        "interval",
		IntervalMinutes: 60,
		Enabled:         true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteChatSession(info.ID, session.ID); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("orphaned scheduled tasks = %#v, %v", tasks, err)
	}
}

// A scheduled run deliberately holds s.mu for reading across the whole
// dispatch so ArchiveProject cannot slip between creating the child session
// and claiming its queue worker. sync.RWMutex read locks are NOT reentrant:
// once any goroutine parks on s.mu.Lock, Go blocks new readers so the writer
// can proceed. Taking a second read lock from inside that window therefore
// wedges the scheduler goroutine, the pending writer, Shutdown's wg.Wait, and
// the macOS quit sheet — permanently. Dispatch must use the *Locked claim.
func TestScheduledDispatchClaimDoesNotReadLockStudioRecursively(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Recursive read lock")
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	project.testEmitter = (&recorder{}).emit

	s.mu.RLock()
	defer s.mu.RUnlock()

	writerParked := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		close(writerParked)
		s.mu.Lock()
		s.mu.Unlock()
		close(writerDone)
	}()
	<-writerParked
	// Give the writer time to actually park on the mutex; only then does Go
	// start blocking newly arriving readers.
	time.Sleep(100 * time.Millisecond)

	claimed := make(chan error, 1)
	go func() {
		claimed <- s.startMessageWithQueueEventPermissionLocked(
			info.ID, "scheduled prompt", nil, "default", nil, "",
		)
	}()

	select {
	case <-claimed:
	case <-time.After(10 * time.Second):
		t.Fatal("queue claim blocked behind a pending writer: s.mu was read-locked recursively")
	}
}
