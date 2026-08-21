package tasks

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerCancelOwnsOnlyRunningTransition(t *testing.T) {
	manager := NewManager(t.TempDir())
	task := NewTask("task-running", "unused", manager.workDir)
	ctx, cancel := context.WithCancel(context.Background())
	task.Status = StatusRunning
	task.cancelFunc = cancel
	manager.tasks[task.ID] = task

	if err := manager.Cancel(task.ID); err != nil {
		t.Fatalf("first Cancel: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("task cancellation context was not cancelled")
	}
	if task.GetStatus() != StatusCancelled {
		t.Fatalf("status = %s, want cancelled", task.GetStatus())
	}
	if err := manager.Cancel(task.ID); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("second Cancel error = %v", err)
	}

	completed := NewTask("task-completed", "unused", manager.workDir)
	completed.Status = StatusCompleted
	manager.tasks[completed.ID] = completed
	if err := manager.Cancel(completed.ID); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("completed task Cancel error = %v", err)
	}
}

func TestManagerCompletionHandlerIsLateBoundAndPanicSafe(t *testing.T) {
	manager := NewManager(t.TempDir())
	task := NewTask("task-complete", "unused", manager.workDir)
	var staleCalls atomic.Int32
	manager.SetCompletionHandler(func(*Task) { staleCalls.Add(1) })

	monitorDone := make(chan struct{})
	manager.monitorWG.Add(1)
	go func() {
		manager.monitorTask(task)
		close(monitorDone)
	}()

	var currentCalls atomic.Int32
	manager.SetCompletionHandler(func(completed *Task) {
		if completed != task {
			t.Errorf("completion task = %p, want %p", completed, task)
		}
		currentCalls.Add(1)
		panic("observer failure")
	})
	task.doneOnce.Do(func() { close(task.done) })

	select {
	case <-monitorDone:
	case <-time.After(time.Second):
		t.Fatal("completion monitor did not survive handler panic")
	}
	if got := staleCalls.Load(); got != 0 {
		t.Fatalf("stale completion handler calls = %d", got)
	}
	if got := currentCalls.Load(); got != 1 {
		t.Fatalf("current completion handler calls = %d", got)
	}
}

func TestManagerWaitUsesDoneInsteadOfEagerCancelledStatus(t *testing.T) {
	manager := NewManager(t.TempDir())
	task := NewTask("task-settling", "unused", manager.workDir)
	task.Status = StatusCancelled
	task.EndTime = time.Now()
	manager.tasks[task.ID] = task

	type waitResult struct {
		info Info
		err  error
	}
	waited := make(chan waitResult, 1)
	go func() {
		info, err := manager.Wait(context.Background(), task.ID)
		waited <- waitResult{info: info, err: err}
	}()
	select {
	case result := <-waited:
		t.Fatalf("Wait returned before Done closed: %+v, %v", result.info, result.err)
	case <-time.After(20 * time.Millisecond):
	}

	task.doneOnce.Do(func() { close(task.done) })
	select {
	case result := <-waited:
		if result.err != nil || result.info.Status != StatusCancelled.String() {
			t.Fatalf("Wait result = %+v, %v", result.info, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not finish after Done closed")
	}
}

func TestManagerWaitReturnsSnapshotOnContextCancellation(t *testing.T) {
	manager := NewManager(t.TempDir())
	task := NewTask("task-running", "unused", manager.workDir)
	task.Status = StatusRunning
	manager.tasks[task.ID] = task
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	info, err := manager.Wait(ctx, task.ID)
	if err != context.Canceled || info.ID != task.ID || info.Status != StatusRunning.String() {
		t.Fatalf("Wait cancellation = %+v, %v", info, err)
	}
}

func TestManagerWaitPrefersAlreadyCompletedTaskOverCancelledContext(t *testing.T) {
	manager := NewManager(t.TempDir())
	task := NewTask("task-completed", "unused", manager.workDir)
	task.Status = StatusCompleted
	task.EndTime = time.Now()
	task.doneOnce.Do(func() { close(task.done) })
	manager.tasks[task.ID] = task
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	info, err := manager.Wait(ctx, task.ID)
	if err != nil || info.Status != StatusCompleted.String() {
		t.Fatalf("Wait = %+v, %v; want completed result", info, err)
	}
}

func TestTaskWithArgsOwnsCallerSlice(t *testing.T) {
	args := []string{"first", "second"}
	task := NewTaskWithArgs("task-args", "command", args, t.TempDir())
	args[0] = "mutated"
	if task.Args[0] != "first" {
		t.Fatalf("task args = %v", task.Args)
	}
}

func TestManagerCloseWaitsForDoneAndRejectsNewTasks(t *testing.T) {
	manager := NewManager(t.TempDir())
	task := NewTask("task-closing", "unused", manager.workDir)
	cancelled, cancel := context.WithCancel(context.Background())
	task.Status = StatusRunning
	task.cancelFunc = cancel
	manager.tasks[task.ID] = task

	closed := make(chan error, 1)
	go func() { closed <- manager.Close(context.Background()) }()
	select {
	case <-cancelled.Done():
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the running task")
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned before task Done: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	task.doneOnce.Do(func() { close(task.done) })
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after task Done")
	}

	if _, err := manager.StartWithArgs(context.Background(), "unused", nil); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Start after Close error = %v, want ErrManagerClosed", err)
	}
	if err := manager.Close(nil); err != nil {
		t.Fatalf("repeated Close: %v", err)
	}
}

func TestManagerCloseHonorsContextWhileRetainingClosedGate(t *testing.T) {
	manager := NewManager(t.TempDir())
	task := NewTask("task-stuck", "unused", manager.workDir)
	_, cancel := context.WithCancel(context.Background())
	task.Status = StatusRunning
	task.cancelFunc = cancel
	manager.tasks[task.ID] = task

	ctx, stopWaiting := context.WithCancel(context.Background())
	stopWaiting()
	if err := manager.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close error = %v, want context cancellation", err)
	}
	if _, err := manager.Start(context.Background(), "unused"); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Start after timed-out Close error = %v, want ErrManagerClosed", err)
	}
	task.doneOnce.Do(func() { close(task.done) })
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("Close retry: %v", err)
	}
}

func TestManagerCloseWaitsForCompletionHandler(t *testing.T) {
	manager := NewManager(t.TempDir())
	task := NewTask("task-observer", "unused", manager.workDir)
	task.Status = StatusCompleted
	manager.tasks[task.ID] = task

	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	manager.SetCompletionHandler(func(*Task) {
		close(handlerStarted)
		<-releaseHandler
	})
	manager.monitorWG.Add(1)
	go manager.monitorTask(task)
	task.doneOnce.Do(func() { close(task.done) })
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("completion handler did not start")
	}

	closed := make(chan error, 1)
	go func() { closed <- manager.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned while completion handler was active: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseHandler)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after completion handler returned")
	}
}
