//go:build !windows

package studio

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFinalizeDelegationRunFailsClosedOnWriteError(t *testing.T) {
	s, from, to, events := delegationTestStudio(t)
	s.mu.RLock()
	project := s.projects[to.ID]
	s.mu.RUnlock()
	session := project.GetSession("default")
	cancelled := make(chan struct{})
	session.mu.Lock()
	session.mutatedThisTurn = true
	session.cancelFn = func() { close(cancelled) }
	session.mu.Unlock()

	run := DelegationRun{
		ID: "terminal-write-failure", Kind: "run",
		FromProjectID: from.ID, FromSessionID: "default",
		ToProjectID: to.ID, ToSessionID: session.ID,
		Task: "work", Status: "running", StartedAt: time.Now().UnixMilli(),
	}
	if _, err := appendDelegationRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.claimDelegation(run.ID, delegationHandle{
		fromProjectID: from.ID, fromSessionID: "default",
		toProjectID: to.ID, toSessionID: session.ID, session: session,
		cancel: func() { session.Stop() }, run: run,
	}); err != nil {
		t.Fatal(err)
	}
	dir := configDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	s.finalizeDelegationRun(run, project, session, "Infra", "finished answer", nil, true, "completed", "", nil)

	select {
	case <-cancelled:
	default:
		t.Fatal("terminal persistence failure did not stop the exact child session")
	}
	select {
	case event := <-events:
		if event.RunID != run.ID || event.FromProjectID != from.ID || event.FromSessionID != "default" ||
			event.ToProjectID != to.ID || event.ToSessionID != session.ID || event.Status != "error" ||
			event.ErrorType != DelegationErrorStorage || !strings.Contains(event.Error, "persist delegation completion") ||
			!event.MutatedBeforeStop {
			t.Fatalf("terminal write-failure event = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal persistence failure was not surfaced")
	}

	stored, found, err := loadDelegationRun(run.ID)
	if err != nil || !found || stored.Status != "running" {
		t.Fatalf("failed terminal write changed durable evidence: run=%+v found=%v err=%v", stored, found, err)
	}
}

func TestDelegateCancelReportsStoppedWhenTerminalWriteFails(t *testing.T) {
	s, from, to, events := delegationTestStudio(t)
	s.mu.RLock()
	project := s.projects[to.ID]
	s.mu.RUnlock()
	session := project.GetSession("default")
	cancelled := make(chan struct{})
	session.mu.Lock()
	session.mutatedThisTurn = true
	session.cancelFn = func() { close(cancelled) }
	session.mu.Unlock()

	run := DelegationRun{
		ID: "cancel-write-failure", Kind: "run",
		FromProjectID: from.ID, FromSessionID: "default",
		ToProjectID: to.ID, ToSessionID: session.ID,
		Task: "work", Status: "running", StartedAt: time.Now().UnixMilli(),
	}
	if _, err := appendDelegationRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.claimDelegation(run.ID, delegationHandle{
		fromProjectID: from.ID, fromSessionID: "default",
		toProjectID: to.ID, toSessionID: session.ID, session: session,
		cancel: func() { session.Stop() }, run: run,
	}); err != nil {
		t.Fatal(err)
	}
	dir := configDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	handler := s.makeDelegateHandler()
	result, err := handler(withAskUserRouting(context.Background(), from.ID, "default"), "cancel", map[string]any{"run_id": run.ID})
	if err != nil || !result.Success {
		t.Fatalf("cancel result = %+v, err=%v", result, err)
	}
	data, _ := result.Data.(map[string]any)
	if data["status"] != "stopped" || data["error_type"] != DelegationErrorStorage ||
		data["mutated_before_stop"] != true || !strings.Contains(result.Content, "was stopped") {
		t.Fatalf("cancel did not report its real outcome: content=%q data=%#v", result.Content, data)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("cancel storage failure left the child running")
	}
	select {
	case event := <-events:
		if event.RunID != run.ID || event.Status != "error" || event.ErrorType != DelegationErrorStorage ||
			!event.MutatedBeforeStop {
			t.Fatalf("cancel storage event = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel storage failure emitted no terminal event")
	}
}

func TestPublicCancelSucceedsAfterStoppingChildWhenTerminalWriteFails(t *testing.T) {
	s, from, to, events := delegationTestStudio(t)
	s.mu.RLock()
	project := s.projects[to.ID]
	s.mu.RUnlock()
	session := project.GetSession("default")
	cancelled := make(chan struct{})
	session.mu.Lock()
	session.cancelFn = func() { close(cancelled) }
	session.mu.Unlock()

	run := DelegationRun{
		ID: "public-cancel-write-failure", Kind: "run",
		FromProjectID: from.ID, FromSessionID: "default",
		ToProjectID: to.ID, ToSessionID: session.ID,
		Task: "work", Status: "running", StartedAt: time.Now().UnixMilli(),
	}
	if _, err := appendDelegationRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.claimDelegation(run.ID, delegationHandle{
		fromProjectID: from.ID, fromSessionID: "default",
		toProjectID: to.ID, toSessionID: session.ID, session: session,
		cancel: func() { session.Stop() }, run: run,
	}); err != nil {
		t.Fatal(err)
	}
	dir := configDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := s.CancelDelegationRun(run.ID); err != nil {
		t.Fatalf("public cancel returned an error after stopping the child: %v", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("public cancel storage failure left the child running")
	}
	select {
	case event := <-events:
		if event.RunID != run.ID || event.Status != "error" || event.ErrorType != DelegationErrorStorage {
			t.Fatalf("public cancel storage event = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("public cancel storage failure emitted no terminal event")
	}
}

func TestCancelWriteFailureAndMonitorEmitOneTerminalEvent(t *testing.T) {
	s, from, to, events := delegationTestStudio(t)
	s.mu.RLock()
	project := s.projects[to.ID]
	s.mu.RUnlock()
	session := project.GetSession("default")
	session.mu.Lock()
	session.active = true
	session.cancelFn = func() {}
	session.mu.Unlock()

	run := DelegationRun{
		ID: "cancel-monitor-write-failure", Kind: "run",
		FromProjectID: from.ID, FromSessionID: "default",
		ToProjectID: to.ID, ToSessionID: session.ID,
		Task: "work", Status: "running", StartedAt: time.Now().UnixMilli(),
	}
	if _, err := appendDelegationRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.claimDelegation(run.ID, delegationHandle{
		fromProjectID: from.ID, fromSessionID: "default",
		toProjectID: to.ID, toSessionID: session.ID, session: session,
		cancel: func() { session.Stop() }, run: run,
	}); err != nil {
		t.Fatal(err)
	}
	monitorDone := make(chan struct{})
	go func() {
		s.monitorDelegationRun(run, project, session, "Infra")
		close(monitorDone)
	}()

	dir := configDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	handler := s.makeDelegateHandler()
	result, err := handler(withAskUserRouting(context.Background(), from.ID, "default"), "cancel", map[string]any{"run_id": run.ID})
	if err != nil || !result.Success {
		t.Fatalf("cancel result = %+v, err=%v", result, err)
	}
	select {
	case <-monitorDone:
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not observe the released exact owner")
	}

	terminal := 0
	progress := 0
	for {
		select {
		case event := <-events:
			if event.RunID != run.ID {
				continue
			}
			if event.Status == "running" {
				progress++
			} else if event.Status == "error" && event.ErrorType == DelegationErrorStorage {
				terminal++
			}
		default:
			if terminal != 1 || progress != 0 {
				t.Fatalf("events after cancel/write failure: terminal=%d progress=%d", terminal, progress)
			}
			return
		}
	}
}
