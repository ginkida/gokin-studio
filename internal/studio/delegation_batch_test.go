package studio

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// One bad target rejects the WHOLE batch, and nothing is left half-started: a
// partially-started fan-out is a spend commitment the user never approved.
func TestBatchRejectsWholeRequestOnOneBadTarget(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	_, err := s.StartDelegationBatch(from.ID, "default", []DelegationTarget{
		{ProjectID: to.ID, Task: "real work"},
		{ProjectID: "ghost", Task: "work"},
	}, "goal", "")
	if err == nil {
		t.Fatal("a batch with an unknown target was accepted")
	}
	assertNoDelegationSideEffects(t, s, to)
}

func TestBatchRejectsDuplicateAndSelfTargets(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	if _, err := s.StartDelegationBatch(from.ID, "default", []DelegationTarget{
		{ProjectID: to.ID, Task: "a"}, {ProjectID: to.ID, Task: "b"},
	}, "", ""); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate target accepted: %v", err)
	}
	if _, err := s.StartDelegationBatch(from.ID, "default", []DelegationTarget{
		{ProjectID: from.ID, Task: "a"},
	}, "", ""); DelegationErrorType(err) != DelegationErrorCycle {
		t.Fatalf("self target accepted: %v", err)
	}
	assertNoDelegationSideEffects(t, s, to)
}

func TestBatchCapsTargetCount(t *testing.T) {
	s, from, _, _ := delegationTestStudio(t)
	targets := make([]DelegationTarget, 0, maxDelegationBatchTargets+1)
	for i := 0; i <= maxDelegationBatchTargets; i++ {
		targets = append(targets, DelegationTarget{ProjectID: string(rune('a' + i)), Task: "t"})
	}
	if _, err := s.StartDelegationBatch(from.ID, "default", targets, "", ""); err == nil ||
		!strings.Contains(err.Error(), "at most") {
		t.Fatalf("oversized batch accepted: %v", err)
	}
}

func TestBatchRequiresATaskPerTargetOrAShared(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	if _, err := s.StartDelegationBatch(from.ID, "default", []DelegationTarget{
		{ProjectID: to.ID},
	}, "", ""); err == nil || !strings.Contains(err.Error(), "no task") {
		t.Fatalf("target with no task accepted: %v", err)
	}
	if _, err := s.StartDelegationBatch(from.ID, "default", nil, "", "shared"); err == nil {
		t.Fatal("empty target list accepted")
	}
}

func TestBatchReservationIsAllOrNothingAcrossTargets(t *testing.T) {
	s, from, first, _ := delegationTestStudio(t)
	second := addTestProject(t, s, "Docs")
	targets := []DelegationTarget{{ProjectID: first.ID}, {ProjectID: second.ID}}

	if err := s.claimDelegation("existing", delegationHandle{
		toProjectID: second.ID, toSessionID: "session", cancel: func() {},
	}); err != nil {
		t.Fatal(err)
	}
	if reservations, err := s.reserveDelegationBatch(from.ID, "blocked-batch", targets); err == nil || len(reservations) != 0 {
		t.Fatalf("partially reserved blocked batch: reservations=%v err=%v", reservations, err)
	}
	s.delegationMu.Lock()
	if len(s.delegations) != 1 || s.delegations["existing"].toProjectID != second.ID {
		t.Fatalf("failed batch changed registry: %+v", s.delegations)
	}
	s.delegationMu.Unlock()
	s.releaseDelegation("existing")

	reservations, err := s.reserveDelegationBatch(from.ID, "batch", targets)
	if err != nil || len(reservations) != len(targets) {
		t.Fatalf("reserve batch = %v, %v", reservations, err)
	}
	for _, target := range targets {
		runID := reservations[target.ProjectID]
		s.delegationMu.Lock()
		handle, ok := s.delegations[runID]
		s.delegationMu.Unlock()
		if !ok || !handle.reserved || handle.toProjectID != target.ProjectID {
			t.Fatalf("target %s reservation = %+v, ok=%v", target.ProjectID, handle, ok)
		}
		if err := s.claimDelegation("outsider-"+target.ProjectID, delegationHandle{
			toProjectID: target.ProjectID, toSessionID: "session", cancel: func() {},
		}); DelegationErrorType(err) != DelegationErrorBusy {
			t.Fatalf("reservation did not block competing run for %s: %v", target.ProjectID, err)
		}
		s.releaseDelegationReservation(runID)
	}
}

func TestBatchReservationsHaveExactActivationOwnership(t *testing.T) {
	s, from, target, _ := delegationTestStudio(t)
	reservations, err := s.reserveDelegationBatch(from.ID, "batch", []DelegationTarget{{ProjectID: target.ID}})
	if err != nil {
		t.Fatal(err)
	}
	runID := reservations[target.ID]
	wrong := delegationHandle{
		fromProjectID: "other", toProjectID: target.ID, toSessionID: "session", batchID: "batch", cancel: func() {},
	}
	run := DelegationRun{
		ID: runID, BatchID: "batch", Kind: "run", FromProjectID: from.ID,
		FromSessionID: "default", ToProjectID: target.ID, ToSessionID: "session",
		Task: "work", Status: "running", StartedAt: time.Now().UnixMilli(),
	}
	if _, err := s.appendAndActivateDelegationReservation(run, wrong); err == nil {
		t.Fatal("foreign owner activated a batch reservation")
	}
	correct := wrong
	correct.fromProjectID = from.ID
	if _, err := s.appendAndActivateDelegationReservation(run, correct); err != nil {
		t.Fatalf("exact owner could not activate reservation: %v", err)
	}
	// Deferred reservation cleanup must never delete an activated live handle.
	s.releaseDelegationReservation(runID)
	s.delegationMu.Lock()
	handle, ok := s.delegations[runID]
	s.delegationMu.Unlock()
	if !ok || handle.reserved || handle.toSessionID != "session" {
		t.Fatalf("activated handle was lost: %+v, ok=%v", handle, ok)
	}
	if stored, ok := mustLoadDelegationRun(t, runID); !ok || stored.Status != "running" {
		t.Fatalf("activated handle has no matching durable row: %+v, ok=%v", stored, ok)
	}
	s.releaseDelegation(runID)
}

func TestCancelDelegationReservationReleasesWithoutDurableRun(t *testing.T) {
	s, from, target, _ := delegationTestStudio(t)
	reservations, err := s.reserveDelegationBatch(from.ID, "batch", []DelegationTarget{{ProjectID: target.ID}})
	if err != nil {
		t.Fatal(err)
	}
	runID := reservations[target.ID]
	if err := s.CancelDelegationRun(runID); err != nil {
		t.Fatalf("cancel reservation: %v", err)
	}
	s.delegationMu.Lock()
	_, alive := s.delegations[runID]
	s.delegationMu.Unlock()
	if alive {
		t.Fatal("cancelled reservation still occupies the target")
	}
	if _, stored := mustLoadDelegationRun(t, runID); stored {
		t.Fatal("reservation cancellation invented a durable run")
	}
}

func TestStopCustomSourceSessionCancelsUnactivatedBatchReservations(t *testing.T) {
	s, from, target, _ := delegationTestStudio(t)
	custom, err := s.CreateChatSession(from.ID)
	if err != nil {
		t.Fatal(err)
	}
	reservations, err := s.reserveDelegationBatchForSession(
		from.ID, custom.ID, "batch", []DelegationTarget{{ProjectID: target.ID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	runID := reservations[target.ID]
	if err := s.StopGeneration(from.ID, custom.ID); err != nil {
		t.Fatalf("stop custom source: %v", err)
	}
	s.delegationMu.Lock()
	_, live := s.delegations[runID]
	s.delegationMu.Unlock()
	if live {
		t.Fatal("custom source Stop left its unactivated batch slot reserved")
	}
	if _, stored := mustLoadDelegationRun(t, runID); stored {
		t.Fatal("cancelling an unactivated slot invented a durable run")
	}
	if _, _, err := s.publishAndStartDelegation(DelegationRun{
		ID: runID, BatchID: "batch", Kind: "run", Status: "running", Task: "work",
		FromProjectID: from.ID, FromSessionID: custom.ID,
		ToProjectID: target.ID, ToSessionID: "child", StartedAt: time.Now().UnixMilli(),
	}, delegationHandle{
		fromProjectID: from.ID, fromSessionID: custom.ID,
		toProjectID: target.ID, toSessionID: "child", batchID: "batch", cancel: func() {},
	}, true, nil); DelegationErrorType(err) != DelegationErrorBusy {
		t.Fatalf("cancelled reservation activated: %v", err)
	}
}

func TestCancelRacingBatchActivationLeavesConsistentState(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		s, from, target, _ := delegationTestStudio(t)
		reservations, err := s.reserveDelegationBatch(from.ID, "batch", []DelegationTarget{{ProjectID: target.ID}})
		if err != nil {
			t.Fatal(err)
		}
		runID := reservations[target.ID]
		run := DelegationRun{
			ID: runID, BatchID: "batch", Kind: "run", FromProjectID: from.ID,
			FromSessionID: "default", ToProjectID: target.ID, ToSessionID: "session",
			Task: "work", Status: "running", StartedAt: time.Now().UnixMilli(),
		}
		handle := delegationHandle{
			fromProjectID: from.ID, toProjectID: target.ID, toSessionID: "session",
			batchID: "batch", cancel: func() {},
		}

		start := make(chan struct{})
		activation := make(chan error, 1)
		cancellation := make(chan error, 1)
		go func() {
			<-start
			_, activateErr := s.appendAndActivateDelegationReservation(run, handle)
			activation <- activateErr
		}()
		go func() {
			<-start
			cancellation <- s.CancelDelegationRun(runID)
		}()
		close(start)
		select {
		case activateErr := <-activation:
			cancelErr := <-cancellation
			stored, storedOK := mustLoadDelegationRun(t, runID)
			s.delegationMu.Lock()
			_, live := s.delegations[runID]
			s.delegationMu.Unlock()
			if activateErr == nil {
				if cancelErr != nil || !storedOK || stored.Status != "stopped" || live {
					t.Fatalf("activation won but cancel was inconsistent: activate=%v cancel=%v stored=%+v ok=%v live=%v",
						activateErr, cancelErr, stored, storedOK, live)
				}
			} else if cancelErr != nil || storedOK || live {
				t.Fatalf("cancelled reservation left partial activation: activate=%v cancel=%v stored=%+v ok=%v live=%v",
					activateErr, cancelErr, stored, storedOK, live)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancel/activation race deadlocked")
		}
	}
}

func TestConcurrentBatchReservationNeverPartiallyWins(t *testing.T) {
	s, from, first, _ := delegationTestStudio(t)
	second := addTestProject(t, s, "Docs")
	targets := []DelegationTarget{{ProjectID: first.ID}, {ProjectID: second.ID}}
	const contenders = 24
	start := make(chan struct{})
	var wg sync.WaitGroup
	var resultMu sync.Mutex
	winners := make([]map[string]string, 0, 1)
	for index := 0; index < contenders; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			reserved, err := s.reserveDelegationBatch(from.ID, fmt.Sprintf("batch-%d", index), targets)
			if err == nil {
				resultMu.Lock()
				winners = append(winners, reserved)
				resultMu.Unlock()
			}
		}(index)
	}
	close(start)
	wg.Wait()
	if len(winners) != 1 || len(winners[0]) != len(targets) {
		t.Fatalf("winners = %+v; exactly one complete batch must reserve", winners)
	}
	s.delegationMu.Lock()
	remaining := len(s.delegations)
	s.delegationMu.Unlock()
	if remaining != len(targets) {
		t.Fatalf("registry contains %d reservations, want %d", remaining, len(targets))
	}
	for _, runID := range winners[0] {
		s.releaseDelegationReservation(runID)
	}
}

// The synthesis carries bounded previews, never full bodies — the caller can
// fetch any answer in full — and it names each project and its status.
func TestBatchSynthesisCarriesPreviewsNotFullBodies(t *testing.T) {
	s, _, to, _ := delegationTestStudio(t)
	body := strings.Repeat("z", 10_000)
	for _, run := range []DelegationRun{
		{
			ID: "r1", Status: "completed", ToProjectID: to.ID, Task: "t",
			Answer: truncateUTF8(body, maxDelegationAnswerBytes), AnswerBytes: len(body),
			Truncated: true, StartedAt: 1, CompletedAt: 2,
		},
		{
			ID: "r2", Status: "error", ToProjectID: "gone", Task: "t",
			ErrorType: DelegationErrorProvider, Error: "provider refused",
			StartedAt: 1, CompletedAt: 2,
		},
	} {
		if _, err := appendDelegationRun(run); err != nil {
			t.Fatal(err)
		}
	}
	message, err := s.batchSynthesisMessage("batch-1", "ship the fix", []string{"r1", "r2"})
	if err != nil {
		t.Fatalf("batchSynthesisMessage: %v", err)
	}
	if message == "" {
		t.Fatal("no synthesis message produced")
	}
	if len(message) > 3*delegationSummaryMaxBytes {
		t.Fatalf("synthesis is %d bytes; it must carry previews, not full bodies", len(message))
	}
	for _, want := range []string{"## Delegation results", "ship the fix", "Infra", "error", "provider refused", "## Task"} {
		if !strings.Contains(message, want) {
			t.Fatalf("synthesis missing %q:\n%s", want, message)
		}
	}
	// A failed member must not poison the batch: the successful one is still
	// summarised.
	if !strings.Contains(message, "fetch") {
		t.Fatal("truncated answers must tell the caller how to read the rest")
	}
	if !strings.Contains(message, crossAgentInjectionFooter) {
		t.Fatal("synthesis must frame delegated output as untrusted context")
	}
}

func TestBatchSynthesisSkippedForASingleRun(t *testing.T) {
	s, from, _, _ := delegationTestStudio(t)
	if s.scheduleBatchSynthesis(from.ID, "default", "b", "", []DelegationRun{{ID: "only"}}) {
		t.Fatal("a one-target fan-out must not pay for a synthesis turn")
	}
}

func TestBatchSynthesisReturnsEmptyWhenNoRunsResolve(t *testing.T) {
	s, _, _, _ := delegationTestStudio(t)
	got, err := s.batchSynthesisMessage("batch", "goal", []string{"missing-1", "missing-2"})
	if err != nil {
		t.Fatalf("batchSynthesisMessage: %v", err)
	}
	if got != "" {
		t.Fatalf("synthesis for unknown runs = %q, want empty", got)
	}
}

func TestWaitForDelegationBatchStopsOnShutdown(t *testing.T) {
	s, _, _, _ := delegationTestStudio(t)
	if _, err := appendDelegationRun(DelegationRun{
		ID: "pending", Status: "running", Task: "t", StartedAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	type waitResult struct {
		ready bool
		err   error
	}
	done := make(chan waitResult, 1)
	go func() {
		ready, err := s.waitForDelegationBatch([]string{"pending"})
		done <- waitResult{ready: ready, err: err}
	}()
	cancel()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("wait error after shutdown: %v", result.err)
		}
		if result.ready {
			t.Fatal("wait reported success after shutdown")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not observe shutdown")
	}
}

func TestWaitForDelegationBatchRejectsRunningRowWithoutLiveOwner(t *testing.T) {
	s, _, _, _ := delegationTestStudio(t)
	if _, err := appendDelegationRun(DelegationRun{
		ID: "orphan-running", Status: "running", Task: "t", StartedAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		ready, err := s.waitForDelegationBatch([]string{"orphan-running"})
		if ready && err == nil {
			err = fmt.Errorf("orphaned running row was reported ready")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "has no live owner") {
			t.Fatalf("orphaned batch wait error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("batch wait hung on a non-terminal row with no live owner")
	}
}
