//go:build !windows

package studio

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestScheduledStartupReconcileDoesNotAdvanceSummaryAfterRunWriteFailure(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Scheduled reconcile storage")
	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Interrupted",
		Prompt: "Check.", Schedule: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := ScheduledTaskRun{
		ID: "interrupted-write-failure", TaskID: task.ID, ProjectID: info.ID,
		SessionID: "child", StartedAt: time.Now().UnixMilli(), Status: "running",
	}
	if _, err := appendScheduledTaskRunForTask(task, run); err != nil {
		t.Fatal(err)
	}
	if err := s.updateScheduledTaskResult(task.ID, time.Now(), "running", nil); err != nil {
		t.Fatal(err)
	}
	runsBefore, err := os.ReadFile(scheduledTaskRunsPath())
	if err != nil {
		t.Fatal(err)
	}
	tasksBefore, err := os.ReadFile(scheduledTasksPath())
	if err != nil {
		t.Fatal(err)
	}
	dir := configDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	s.reconcileInterruptedScheduledRuns()

	runsAfter, err := os.ReadFile(scheduledTaskRunsPath())
	if err != nil || string(runsAfter) != string(runsBefore) {
		t.Fatalf("failed reconcile changed run store: same=%v err=%v", string(runsAfter) == string(runsBefore), err)
	}
	tasksAfter, err := os.ReadFile(scheduledTasksPath())
	if err != nil || string(tasksAfter) != string(tasksBefore) {
		t.Fatalf("failed reconcile advanced task summary: same=%v err=%v", string(tasksAfter) == string(tasksBefore), err)
	}
	logs := s.GetRecentLogs()
	found := false
	for _, entry := range logs {
		if entry.Source == "scheduler" && entry.Level == "error" &&
			strings.Contains(entry.Message, "permission denied") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("reconcile write failure missing from diagnostics: %#v", logs)
	}
}
