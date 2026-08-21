package studio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
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

func TestNewScheduledTaskCannotInjectSchedulerOwnedRunSummary(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled summary input")
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "No forged summary",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
		LastRunAt: time.Now().UnixMilli(), LastRunID: "forged-run",
		LastStatus: "running", LastError: "forged error",
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.LastRunAt != 0 || task.LastRunID != "" || task.LastStatus != "" || task.LastError != "" {
		t.Fatalf("new task retained client-owned run summary: %+v", task)
	}
	tasks, err := s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || tasks[0].LastRunAt != 0 || tasks[0].LastRunID != "" ||
		tasks[0].LastStatus != "" || tasks[0].LastError != "" {
		t.Fatalf("persisted forged run summary: %#v err=%v", tasks, err)
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

func TestScheduledDispatchRollsBackChildWhenRunIndexIsUnreadable(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	repo := prepareSessionWorktreeRepo(t)
	info, err := s.AddProject("Scheduled append rollback", repo)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Rollback",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
		Provider: "glm", Model: "glm-5.2", ApprovalMode: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	project.mu.RLock()
	sessionsBefore := len(project.sessions)
	project.mu.RUnlock()
	worktreesBefore := runGit(repo, "worktree", "list", "--porcelain")
	historiesBefore, err := os.ReadDir(historyDir())
	if err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{not-json")
	if err := os.WriteFile(scheduledTaskRunsPath(), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.dispatchScheduledTask(task); err == nil || !strings.Contains(err.Error(), "persist scheduled run") {
		t.Fatalf("dispatch error = %v", err)
	}

	project.mu.RLock()
	sessionsAfter := len(project.sessions)
	project.mu.RUnlock()
	if sessionsAfter != sessionsBefore {
		t.Fatalf("unpublished scheduled child leaked: sessions before=%d after=%d", sessionsBefore, sessionsAfter)
	}
	if worktreesAfter := runGit(repo, "worktree", "list", "--porcelain"); worktreesAfter != worktreesBefore {
		t.Fatalf("unpublished scheduled worktree leaked:\nbefore:\n%s\nafter:\n%s", worktreesBefore, worktreesAfter)
	}
	historiesAfter, err := os.ReadDir(historyDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(historiesAfter) != len(historiesBefore) {
		t.Fatalf("unpublished scheduled history leaked: before=%d after=%d", len(historiesBefore), len(historiesAfter))
	}
	if got, err := os.ReadFile(scheduledTaskRunsPath()); err != nil || string(got) != string(corrupt) {
		t.Fatalf("run-index evidence changed: %q, %v", got, err)
	}
	tasks, err := s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || tasks[0].LastStatus != "error" ||
		!strings.Contains(tasks[0].LastError, "persist scheduled run") {
		t.Fatalf("task after append failure = %#v, %v", tasks, err)
	}
}

func TestScheduledMonitorNeverReportsCompletedWhenTerminalStoreIsUnreadable(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled terminal store")
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Terminal failure",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
		Provider: "glm", Model: "glm-5.2", ApprovalMode: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	project.testEmitter = (&recorder{}).emit
	session, err := createScheduledRunSession(project, task, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	run := ScheduledTaskRun{
		ID: "terminal-store-unreadable", TaskID: task.ID, ProjectID: info.ID,
		SessionID: session.ID, StartedAt: time.Now().UnixMilli(), Status: "running",
		Provider: "glm", Model: "glm-5.2", ApprovalMode: "manual",
	}
	if _, err := appendScheduledTaskRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.updateScheduledTaskResult(task.ID, time.Now(), "running", nil); err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	session.history = append(session.history, &genai.Content{
		Role: "model", Parts: []*genai.Part{genai.NewPartFromText("done")},
	})
	session.queueWorker = false
	session.mu.Unlock()
	corrupt := []byte("{broken-run-index")
	if err := os.WriteFile(scheduledTaskRunsPath(), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	s.monitorScheduledTaskRun(task, run, project, session)

	if got, err := os.ReadFile(scheduledTaskRunsPath()); err != nil || string(got) != string(corrupt) {
		t.Fatalf("terminal failure overwrote recoverable bytes: %q, %v", got, err)
	}
	tasks, err := s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || tasks[0].LastStatus != "error" ||
		!strings.Contains(tasks[0].LastError, "lost durable tracking") {
		t.Fatalf("task falsely reported terminal success: %#v, %v", tasks, err)
	}
	logs := s.GetRecentLogs()
	found := false
	for _, entry := range logs {
		if entry.Source == "scheduler" && strings.Contains(entry.Message, run.ID) &&
			strings.Contains(entry.Message, "lost durable tracking") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("terminal storage failure missing from diagnostics: %#v", logs)
	}
}

func TestFinishScheduledTaskRunRefusesInvalidOrSecondTerminalTransition(t *testing.T) {
	withTempConfigDir(t)
	run := ScheduledTaskRun{
		ID: "terminal-once", TaskID: "task", ProjectID: "project", SessionID: "session",
		StartedAt: time.Now().UnixMilli(), Status: "running",
	}
	if _, err := appendScheduledTaskRun(run); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := finishScheduledTaskRun(run.ID, "running", nil); err == nil || changed {
		t.Fatalf("nonterminal finish = changed=%v err=%v", changed, err)
	}
	finished, changed, err := finishScheduledTaskRun(run.ID, "completed", nil)
	if err != nil || !changed || finished.Status != "completed" || finished.CompletedAt == 0 {
		t.Fatalf("first terminal = %+v changed=%v err=%v", finished, changed, err)
	}
	finished, changed, err = finishScheduledTaskRun(run.ID, "stopped", fmt.Errorf("late stop"))
	if err != nil || changed || finished.Status != "completed" || finished.Error != "" {
		t.Fatalf("second terminal overwrote winner: %+v changed=%v err=%v", finished, changed, err)
	}
}

func TestScheduledRunRetentionNeverEvictsLiveOwner(t *testing.T) {
	withTempConfigDir(t)
	runs := make([]ScheduledTaskRun, 0, maxRunsPerScheduledTask+5)
	for i := 0; i < maxRunsPerScheduledTask; i++ {
		runs = append(runs, ScheduledTaskRun{
			ID: fmt.Sprintf("live-%02d", i), TaskID: "task", ProjectID: "project",
			SessionID: fmt.Sprintf("session-%02d", i), StartedAt: int64(i + 1), Status: "running",
		})
	}
	for i := 0; i < 5; i++ {
		runs = append(runs, ScheduledTaskRun{
			ID: fmt.Sprintf("terminal-%02d", i), TaskID: "task", ProjectID: "project",
			SessionID: fmt.Sprintf("old-session-%02d", i), StartedAt: int64(100 + i), Status: "completed",
		})
	}
	kept, evicted, err := fitScheduledTaskRuns(runs)
	if err != nil || len(kept) != maxRunsPerScheduledTask || len(evicted) != 5 {
		t.Fatalf("fit live ownership: kept=%d evicted=%d err=%v", len(kept), len(evicted), err)
	}
	for _, run := range kept {
		if run.Status != "running" {
			t.Fatalf("terminal history displaced a live owner: %+v", run)
		}
	}
	for _, run := range evicted {
		if !scheduledTaskRunTerminal(run.Status) {
			t.Fatalf("retention evicted live run: %+v", run)
		}
	}

	if err := saveScheduledTaskRunsRaw(kept); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(scheduledTaskRunsPath())
	if err != nil {
		t.Fatal(err)
	}
	_, err = appendScheduledTaskRun(ScheduledTaskRun{
		ID: "one-live-too-many", TaskID: "task", ProjectID: "project",
		SessionID: "new-session", StartedAt: 1000, Status: "running",
	})
	if err == nil || !strings.Contains(err.Error(), "live scheduled runs") {
		t.Fatalf("live-cap append error = %v", err)
	}
	after, readErr := os.ReadFile(scheduledTaskRunsPath())
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("rejected live append changed durable owners: same=%v err=%v", string(after) == string(before), readErr)
	}
}

func TestScheduledTaskAllowsOnlyOneLiveRunAndRunIDsStayUnique(t *testing.T) {
	withTempConfigDir(t)
	task := ScheduledTask{ID: "single-flight", ProjectID: "project"}
	if err := saveScheduledTasksRaw([]ScheduledTask{task}); err != nil {
		t.Fatal(err)
	}
	first := ScheduledTaskRun{
		ID: "first-live", TaskID: task.ID, ProjectID: task.ProjectID,
		SessionID: "first-child", StartedAt: 1, Status: "running",
	}
	if _, err := appendScheduledTaskRunForTask(task, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "second-live"
	second.SessionID = "second-child"
	second.StartedAt = 2
	if _, err := appendScheduledTaskRunForTask(task, second); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second live append error = %v", err)
	}
	duplicate := first
	duplicate.Status = "completed"
	if _, err := appendScheduledTaskRun(duplicate); err == nil || !strings.Contains(err.Error(), "ID already exists") {
		t.Fatalf("duplicate run ID error = %v", err)
	}
	runs, err := loadScheduledTaskRunsRaw()
	if err != nil || len(runs) != 1 || runs[0].ID != first.ID || runs[0].Status != "running" {
		t.Fatalf("durable runs after rejected appends = %#v, %v", runs, err)
	}
}

func TestScheduledRunRefusesStaleExecutionSnapshotBeforePublicationAndStart(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled stale snapshot")
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Original",
		Prompt: "Original prompt", Schedule: "manual", Enabled: true,
		Provider: "glm", Model: "glm-5.2", ApprovalMode: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := ScheduledTaskRun{
		ID: "stale-before-publish", TaskID: task.ID, ProjectID: info.ID,
		SessionID: "child", StartedAt: time.Now().UnixMilli(), Status: "running",
	}
	updated := task
	updated.Prompt = "Edited prompt"
	if _, err := s.SaveScheduledTask(updated); err != nil {
		t.Fatal(err)
	}
	if _, err := appendScheduledTaskRunForTask(task, run); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale publish error = %v", err)
	}

	// Publish from the current snapshot, then change it before the queue claim.
	run.ID = "stale-before-start"
	run.SessionID = "child-2"
	if _, err := appendScheduledTaskRunForTask(updated, run); err != nil {
		t.Fatal(err)
	}
	newer := updated
	newer.ApprovalMode = "skip"
	if _, err := s.SaveScheduledTask(newer); err != nil {
		t.Fatal(err)
	}
	if err := s.claimScheduledTaskRunStart(updated, run, time.Now()); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale start error = %v", err)
	}
}

func TestScheduledSummaryAndCadenceRemainOwnedByExactLiveRun(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled exact summary owner")
	now := time.Now()
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Single flight",
		Prompt: "Check.", Schedule: "interval", IntervalMinutes: minScheduleIntervalMins,
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := ScheduledTaskRun{
		ID: "exact-live-owner", TaskID: task.ID, ProjectID: info.ID,
		SessionID: "child", StartedAt: now.Add(-time.Minute).UnixMilli(), Status: "running",
	}
	if _, err := appendScheduledTaskRunForTask(task, run); err != nil {
		t.Fatal(err)
	}
	scheduledTasksMu.Lock()
	tasks, err := loadScheduledTasksRaw()
	if err == nil {
		tasks[0].NextRunAt = now.Add(-time.Second).UnixMilli()
		tasks[0].LastRunAt = run.StartedAt
		tasks[0].LastRunID = run.ID
		tasks[0].LastStatus = "running"
		err = saveScheduledTasksRaw(tasks)
	}
	scheduledTasksMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	// An uncorrelated preflight error and a due tick must not steal the card
	// from the exact live run. The tick still advances cadence.
	if err := s.updateScheduledTaskResult(task.ID, now, "error", fmt.Errorf("second launch refused")); err != nil {
		t.Fatal(err)
	}
	s.dispatchDueScheduledTasks(now)
	tasks, err = s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || tasks[0].LastRunID != run.ID || tasks[0].LastStatus != "running" ||
		tasks[0].LastError != "" || tasks[0].NextRunAt <= now.UnixMilli() {
		t.Fatalf("live summary/cadence after overlap = %#v err=%v", tasks, err)
	}

	// A callback from any other run ID is stale even if its timestamp is newer.
	if err := s.updateScheduledTaskRunResult(task.ID, "older-run", now.Add(time.Hour), "completed", nil); err != nil {
		t.Fatal(err)
	}
	tasks, err = s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || tasks[0].LastRunID != run.ID || tasks[0].LastStatus != "running" {
		t.Fatalf("stale terminal callback replaced live summary = %#v err=%v", tasks, err)
	}
}

func TestScheduledTerminalErrorsAreBoundedInRunAndTaskStores(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled bounded errors")
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Bound errors",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := ScheduledTaskRun{
		ID: "bounded-terminal-error", TaskID: task.ID, ProjectID: info.ID,
		SessionID: "child", StartedAt: time.Now().UnixMilli(), Status: "running",
	}
	if _, err := appendScheduledTaskRun(run); err != nil {
		t.Fatal(err)
	}
	huge := fmt.Errorf("%sTAIL", strings.Repeat("x", maxScheduledTaskError+maxScheduledRunFile))
	finished, changed, err := finishScheduledTaskRun(run.ID, "error", huge)
	if err != nil || !changed || len(finished.Error) != maxScheduledTaskError || strings.Contains(finished.Error, "TAIL") {
		t.Fatalf("bounded run error: bytes=%d changed=%v err=%v", len(finished.Error), changed, err)
	}
	if err := s.updateScheduledTaskResult(task.ID, time.Now(), "error", huge); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || len(tasks[0].LastError) != maxScheduledTaskError || strings.Contains(tasks[0].LastError, "TAIL") {
		t.Fatalf("bounded task error: %#v err=%v", tasks, err)
	}
}

func TestScheduledRunCannotStartAfterOwningTaskWasDeleted(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled delete race")
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Delete race",
		Prompt: "Must not run.", Schedule: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	project.testEmitter = (&recorder{}).emit
	session, err := createScheduledRunSession(project, task, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	run := ScheduledTaskRun{
		ID: "deleted-owner", TaskID: task.ID, ProjectID: info.ID,
		SessionID: session.ID, StartedAt: time.Now().UnixMilli(), Status: "running",
	}
	if _, err := appendScheduledTaskRunForTask(task, run); err != nil {
		t.Fatal(err)
	}
	if _, _, err := removeScheduledTaskRecords(info.ID, task.ID, nil); err != nil {
		t.Fatal(err)
	}

	if err := s.claimScheduledTaskRunStart(task, run, time.Now()); err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Fatalf("start after delete error = %v", err)
	}
	session.mu.RLock()
	claimed := session.queueWorker
	history := append([]*genai.Content(nil), session.history...)
	session.mu.RUnlock()
	if claimed || len(history) != 0 {
		t.Fatalf("deleted task started paid work: claimed=%v history=%#v", claimed, history)
	}
}

func TestScheduledMonitorStopsChildWhenDurableRowDisappears(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled missing owner")
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Missing owner",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	project.testEmitter = (&recorder{}).emit
	session, err := createScheduledRunSession(project, task, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan struct{})
	session.mu.Lock()
	session.queueWorker = true
	session.cancelFn = func() { close(cancelled) }
	session.mu.Unlock()
	run := ScheduledTaskRun{
		ID: "missing-durable-owner", TaskID: task.ID, ProjectID: info.ID,
		SessionID: session.ID, StartedAt: time.Now().UnixMilli(), Status: "running",
	}
	if _, err := appendScheduledTaskRunForTask(task, run); err != nil {
		t.Fatal(err)
	}
	scheduledTasksMu.Lock()
	if err := saveScheduledTaskRunsRaw(nil); err != nil {
		scheduledTasksMu.Unlock()
		t.Fatal(err)
	}
	scheduledTasksMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.monitorScheduledTaskRun(task, run, project, session)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not fail closed after losing its durable row")
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("monitor left untracked scheduled child running")
	}
	tasks, err := s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || tasks[0].LastStatus != "error" ||
		!strings.Contains(tasks[0].LastError, "lost durable tracking") {
		t.Fatalf("task after lost run owner = %#v, %v", tasks, err)
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
		status := "completed"
		if i == maxRunsPerScheduledTask+4 {
			status = "running"
		}
		if _, err := appendScheduledTaskRun(ScheduledTaskRun{
			ID: fmt.Sprintf("run-%02d", i), TaskID: task.ID, ProjectID: info.ID,
			SessionID: "default", StartedAt: int64(i + 1), Status: status,
			Provider: "glm", Model: "glm-5.2", ApprovalMode: "manual",
		}); err != nil {
			t.Fatal(err)
		}
	}
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

func TestScheduledStartupReconcileRepairsStaleSummaryFromTerminalRun(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled summary repair")
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Repair summary",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Minute).UnixMilli()
	run := ScheduledTaskRun{
		ID: "terminal-before-summary-crash", TaskID: task.ID, ProjectID: info.ID,
		SessionID: "child", StartedAt: started, Status: "running",
	}
	if _, err := appendScheduledTaskRunForTask(task, run); err != nil {
		t.Fatal(err)
	}
	finished, changed, err := finishScheduledTaskRun(run.ID, "error", fmt.Errorf("provider failed"))
	if err != nil || !changed {
		t.Fatalf("terminal commit = %+v changed=%v err=%v", finished, changed, err)
	}
	if err := s.updateScheduledTaskResult(task.ID, time.UnixMilli(started), "running", nil); err != nil {
		t.Fatal(err)
	}

	s.reconcileInterruptedScheduledRuns()

	tasks, err := s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || tasks[0].LastStatus != "error" ||
		tasks[0].LastError != "provider failed" || tasks[0].LastRunAt != finished.CompletedAt {
		t.Fatalf("repaired summary = %#v err=%v", tasks, err)
	}
}

func TestScheduledStartupReconcilePreservesNewerDispatchFailureWithoutRun(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled newer summary")
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Keep newer summary",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Hour).UnixMilli()
	run := ScheduledTaskRun{
		ID: "older-terminal", TaskID: task.ID, ProjectID: info.ID,
		SessionID: "child", StartedAt: started, Status: "completed", CompletedAt: started + 1000,
	}
	if _, err := appendScheduledTaskRunForTask(task, run); err != nil {
		t.Fatal(err)
	}
	newer := time.Now()
	if err := s.updateScheduledTaskResult(task.ID, newer, "error", fmt.Errorf("model unavailable")); err != nil {
		t.Fatal(err)
	}

	s.reconcileInterruptedScheduledRuns()

	tasks, err := s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || tasks[0].LastStatus != "error" ||
		tasks[0].LastError != "model unavailable" || tasks[0].LastRunAt != newer.UnixMilli() {
		t.Fatalf("newer summary was overwritten = %#v err=%v", tasks, err)
	}
}

func TestScheduledStartupReconcileStopsInterruptedDispatchWithoutRun(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled interrupted dispatch")
	_, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Interrupted dispatch",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatchedAt := time.Now().UnixMilli()
	scheduledTasksMu.Lock()
	tasks, err := loadScheduledTasksRaw()
	if err == nil {
		tasks[0].LastRunAt = dispatchedAt
		tasks[0].LastStatus = "dispatching"
		tasks[0].LastError = ""
		err = saveScheduledTasksRaw(tasks)
	}
	scheduledTasksMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	s.reconcileInterruptedScheduledRuns()

	tasks, err = s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || tasks[0].LastRunAt != dispatchedAt ||
		tasks[0].LastStatus != "stopped" || !strings.Contains(tasks[0].LastError, "before this scheduled run started") {
		t.Fatalf("interrupted dispatch summary = %#v err=%v", tasks, err)
	}
}

func TestScheduledStartupReconcileFailsClosedOnUnknownRunStatus(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled unknown status")
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Unknown status",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := ScheduledTaskRun{
		ID: "unknown-status", TaskID: task.ID, ProjectID: info.ID,
		SessionID: "child", StartedAt: time.Now().UnixMilli(), Status: "mystery",
	}
	if _, err := appendScheduledTaskRunForTask(task, run); err != nil {
		t.Fatal(err)
	}
	if err := s.updateScheduledTaskRunResult(task.ID, run.ID, time.Now(), "running", nil); err != nil {
		t.Fatal(err)
	}

	s.reconcileInterruptedScheduledRuns()

	runs, err := s.ListScheduledTaskRuns(info.ID, task.ID)
	if err != nil || len(runs) != 1 || runs[0].Status != "error" ||
		!strings.Contains(runs[0].Error, `invalid scheduled run status "mystery"`) {
		t.Fatalf("unknown run reconciliation = %#v err=%v", runs, err)
	}
	tasks, err := s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || tasks[0].LastStatus != "error" ||
		!strings.Contains(tasks[0].LastError, `invalid scheduled run status "mystery"`) {
		t.Fatalf("unknown task summary = %#v err=%v", tasks, err)
	}
}

func TestScheduledStartupReconcileStopsLegacyRunningSummaryWithoutRun(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled legacy missing run")
	_, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Legacy missing run",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	scheduledTasksMu.Lock()
	tasks, err := loadScheduledTasksRaw()
	if err == nil {
		tasks[0].LastRunAt = time.Now().Add(-time.Minute).UnixMilli()
		tasks[0].LastRunID = ""
		tasks[0].LastStatus = "running"
		err = saveScheduledTasksRaw(tasks)
	}
	scheduledTasksMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	s.reconcileInterruptedScheduledRuns()

	tasks, err = s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 1 || tasks[0].LastStatus != "error" ||
		!strings.Contains(tasks[0].LastError, "lost durable tracking") {
		t.Fatalf("legacy missing-run summary = %#v err=%v", tasks, err)
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

func TestDeleteSourceSessionStopsActiveScheduledChildBeforeRemovingRunOwner(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled source cleanup")
	source, err := s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: source.ID, Name: "Source-owned task",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	runSession := addScheduledRunChat(t, s, info.ID, task, "running")
	cancelled := make(chan struct{})
	runSession.mu.Lock()
	runSession.active = true
	runSession.queueWorker = true
	runSession.cancelFn = func() { close(cancelled) }
	runSession.mu.Unlock()

	if err := s.DeleteChatSession(info.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("deleting the source chat left its scheduled child running")
	}
	tasks, err := s.ListScheduledTasks(info.ID)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("source-owned tasks after deletion = %#v, %v", tasks, err)
	}
	runs, err := s.ListScheduledTaskRuns(info.ID, task.ID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("source-owned run rows after deletion = %#v, %v", runs, err)
	}
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	project.mu.RLock()
	keptChild := project.sessions[runSession.ID]
	_, sourceExists := project.sessions[source.ID]
	project.mu.RUnlock()
	if keptChild != runSession || sourceExists {
		t.Fatalf("chat ownership after source deletion: child_kept=%v source_exists=%v", keptChild == runSession, sourceExists)
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
			nil,
		)
	}()

	select {
	case <-claimed:
	case <-time.After(10 * time.Second):
		t.Fatal("queue claim blocked behind a pending writer: s.mu was read-locked recursively")
	}
}
