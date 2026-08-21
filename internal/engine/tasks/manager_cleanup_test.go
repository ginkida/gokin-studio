package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestManagerCleanupWaitsForCancelledTaskDone(t *testing.T) {
	task := NewTask("task-cancelled", "long running", t.TempDir())
	task.Status = StatusCancelled
	task.EndTime = time.Now().Add(-time.Hour)
	manager := NewManager(task.WorkDir)
	manager.tasks[task.ID] = task

	if removed := manager.Cleanup(0); removed != 0 || manager.Count() != 1 {
		t.Fatalf("Cleanup before Done removed=%d count=%d", removed, manager.Count())
	}
	task.doneOnce.Do(func() { close(task.done) })
	if removed := manager.Cleanup(0); removed != 1 || manager.Count() != 0 {
		t.Fatalf("Cleanup after Done removed=%d count=%d", removed, manager.Count())
	}
}

func TestManagerCleanupRetainsTaskWhenOutputRemovalFails(t *testing.T) {
	workDir := t.TempDir()
	task := NewTask("task-complete", "done", workDir)
	path := filepath.Join(workDir, ".gokin", "task-output", task.ID+".log")
	if err := task.Output.SetOutputFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Output.Write([]byte("preserve until cleanup succeeds")); err != nil {
		t.Fatal(err)
	}
	if err := task.Output.Close(); err != nil {
		t.Fatal(err)
	}
	task.Status = StatusCompleted
	task.EndTime = time.Now().Add(-time.Hour)
	task.doneOnce.Do(func() { close(task.done) })

	manager := NewManager(workDir)
	manager.tasks[task.ID] = task
	previousRemove := removeTaskOutputFile
	removeTaskOutputFile = func(string) error { return errors.New("injected remove failure") }
	t.Cleanup(func() { removeTaskOutputFile = previousRemove })

	if removed := manager.Cleanup(0); removed != 0 || manager.Count() != 1 {
		t.Fatalf("failed cleanup removed=%d count=%d", removed, manager.Count())
	}
	if _, err := os.Stat(task.Output.FilePath()); err != nil {
		t.Fatalf("output disappeared after failed cleanup: %v", err)
	}

	removeTaskOutputFile = previousRemove
	if removed := manager.Cleanup(0); removed != 1 || manager.Count() != 0 {
		t.Fatalf("retry cleanup removed=%d count=%d", removed, manager.Count())
	}
	if _, err := os.Stat(task.Output.FilePath()); !os.IsNotExist(err) {
		t.Fatalf("output survived successful cleanup: %v", err)
	}
}

func TestManagerListsTasksNewestFirstWithStableIDTieBreak(t *testing.T) {
	manager := NewManager(t.TempDir())
	base := time.Now().Add(-time.Minute)
	manager.tasks["older"] = &Task{ID: "older", Status: StatusRunning, StartTime: base, done: make(chan struct{})}
	manager.tasks["z-tie"] = &Task{ID: "z-tie", Status: StatusCompleted, StartTime: base.Add(time.Second), done: make(chan struct{})}
	manager.tasks["a-tie"] = &Task{ID: "a-tie", Status: StatusRunning, StartTime: base.Add(time.Second), done: make(chan struct{})}

	ids := func(infos []Info) []string {
		result := make([]string, len(infos))
		for index, info := range infos {
			result[index] = info.ID
		}
		return result
	}
	if got := ids(manager.List()); !reflect.DeepEqual(got, []string{"a-tie", "z-tie", "older"}) {
		t.Fatalf("List order=%v", got)
	}
	if got := ids(manager.ListRunning()); !reflect.DeepEqual(got, []string{"a-tie", "older"}) {
		t.Fatalf("ListRunning order=%v", got)
	}
	if got := ids(manager.ListCompleted()); !reflect.DeepEqual(got, []string{"z-tie"}) {
		t.Fatalf("ListCompleted order=%v", got)
	}
}
