package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func delegationTestStudio(t *testing.T) (*Studio, *ProjectInfo, *ProjectInfo, chan DelegationEvent) {
	t.Helper()
	withTempConfigDir(t)
	s := newStudioForTest(t)
	from := addTestProject(t, s, "Caller")
	to := addTestProject(t, s, "Infra")
	events := make(chan DelegationEvent, 32)
	s.testDelegationEmitter = func(_ string, data DelegationEvent) {
		select {
		case events <- data:
		default:
		}
	}
	// A delegation starts a real turn in the target, which emits chat events.
	// Without a test emitter those reach the Wails runtime and abort the test.
	s.mu.RLock()
	for _, project := range s.projects {
		project.testEmitter = (&recorder{}).emit
	}
	s.mu.RUnlock()
	return s, from, to, events
}

func mustLoadDelegationRun(t *testing.T, id string) (DelegationRun, bool) {
	t.Helper()
	run, found, err := loadDelegationRun(id)
	if err != nil {
		t.Fatalf("loadDelegationRun(%q): %v", id, err)
	}
	return run, found
}

// A refused delegation must leave nothing behind: no run row, and above all no
// child chat and no worktree in the target project.
func assertNoDelegationSideEffects(t *testing.T, s *Studio, target *ProjectInfo) {
	t.Helper()
	runs, err := s.ListDelegationRuns("", "")
	if err != nil {
		t.Fatalf("ListDelegationRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("refused delegation still recorded %d run(s): %+v", len(runs), runs)
	}
	s.mu.RLock()
	project := s.projects[target.ID]
	s.mu.RUnlock()
	project.mu.RLock()
	count := len(project.sessions)
	project.mu.RUnlock()
	if count != 1 {
		t.Fatalf("target has %d sessions; a refused delegation must not create one", count)
	}
}

func TestDelegationRefusesSelfTargetBeforeAnyWork(t *testing.T) {
	s, from, _, _ := delegationTestStudio(t)
	_, err := s.StartDelegation(from.ID, "default", from.ID, "run", "", "do the thing")
	if DelegationErrorType(err) != DelegationErrorCycle {
		t.Fatalf("error_type = %q, want %q (%v)", DelegationErrorType(err), DelegationErrorCycle, err)
	}
	assertNoDelegationSideEffects(t, s, from)
}

func TestDelegationRefusesUnknownTarget(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	_, err := s.StartDelegation(from.ID, "default", "ghost", "run", "", "do the thing")
	if DelegationErrorType(err) != DelegationErrorUnknownTarget {
		t.Fatalf("error_type = %q, want %q (%v)", DelegationErrorType(err), DelegationErrorUnknownTarget, err)
	}
	assertNoDelegationSideEffects(t, s, to)
}

// The target's budget is checked before a child session (and its worktree)
// exists, so a refusal is genuinely free.
func TestDelegationRefusedWhenTargetBudgetExhausted(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	s.mu.RLock()
	target := s.projects[to.ID]
	s.mu.RUnlock()
	target.mu.Lock()
	target.EnforceBudget = true
	target.BudgetUSD = 0.01
	target.mu.Unlock()
	target.bumpTotalCostUSD(1.0)

	_, err := s.StartDelegation(from.ID, "default", to.ID, "run", "", "expensive work")
	if DelegationErrorType(err) != DelegationErrorBudget {
		t.Fatalf("error_type = %q, want %q (%v)", DelegationErrorType(err), DelegationErrorBudget, err)
	}
	assertNoDelegationSideEffects(t, s, to)
}

func TestDelegationRejectsEmptyTask(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	if _, err := s.StartDelegation(from.ID, "default", to.ID, "run", "", "   "); err == nil {
		t.Fatal("empty task accepted")
	}
	assertNoDelegationSideEffects(t, s, to)
}

// A delegated chat may not start further delegation: that is how a two-hop
// limit turns into an unbounded relay.
func TestDelegateChildCannotOriginateDelegation(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	s.mu.RLock()
	caller := s.projects[from.ID]
	s.mu.RUnlock()
	session := caller.GetSession("default")
	session.mu.Lock()
	session.delegateChild = true
	session.mu.Unlock()

	_, err := s.StartDelegation(from.ID, "default", to.ID, "run", "", "chain further")
	if err == nil {
		t.Fatal("a delegation child was allowed to delegate onward")
	}
	assertNoDelegationSideEffects(t, s, to)
}

func TestDelegationRunStoreRoundTripAndTerminalRefusal(t *testing.T) {
	withTempConfigDir(t)
	run := DelegationRun{
		ID: "run-1", Kind: "run", FromProjectID: "a", ToProjectID: "b",
		Task: "work", Status: "running", StartedAt: time.Now().UnixMilli(),
	}
	if _, err := appendDelegationRun(run); err != nil {
		t.Fatalf("appendDelegationRun: %v", err)
	}
	stored, ok := mustLoadDelegationRun(t, "run-1")
	if !ok || stored.Status != "running" {
		t.Fatalf("stored = %+v, ok=%v", stored, ok)
	}

	finished, changed, _, err := finishDelegationRun("run-1", "completed", "", nil, func(r *DelegationRun) {
		r.Answer = "done"
	})
	if err != nil || !changed || finished.Answer != "done" {
		t.Fatalf("finish = %+v changed=%v err=%v", finished, changed, err)
	}
	// A racing completion must not be able to rewrite a terminal row — this is
	// what makes "mark terminal, then cancel" safe.
	_, changedAgain, _, err := finishDelegationRun("run-1", "error", DelegationErrorProvider, fmt.Errorf("late"), nil)
	if err != nil {
		t.Fatalf("second finish errored: %v", err)
	}
	if changedAgain {
		t.Fatal("a terminal delegation row was overwritten by a later completion")
	}
	if again, _ := mustLoadDelegationRun(t, "run-1"); again.Status != "completed" || again.Error != "" {
		t.Fatalf("terminal row mutated: %+v", again)
	}
}

func TestDelegationStoreRejectsDuplicateIDAndSecondLiveTargetOwner(t *testing.T) {
	withTempConfigDir(t)
	first := DelegationRun{
		ID: "owner-1", Status: "running", Task: "first",
		ToProjectID: "target", StartedAt: 1,
	}
	if _, err := appendDelegationRun(first); err != nil {
		t.Fatalf("append first owner: %v", err)
	}
	duplicate := first
	duplicate.Status = "completed"
	if _, err := appendDelegationRun(duplicate); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate run ID accepted: %v", err)
	}
	if _, err := appendDelegationRun(DelegationRun{
		ID: "owner-2", Status: "running", Task: "second",
		ToProjectID: "target", StartedAt: 2,
	}); err == nil || !strings.Contains(err.Error(), "already has live") {
		t.Fatalf("second live target owner accepted: %v", err)
	}
	if _, _, _, err := finishDelegationRun(first.ID, "running", "", nil, nil); err == nil ||
		!strings.Contains(err.Error(), "invalid terminal") {
		t.Fatalf("non-terminal finish accepted: %v", err)
	}
}

func TestDelegationReaderNeverTruncatesLiveOwnerPastRowCap(t *testing.T) {
	withTempConfigDir(t)
	runs := make([]DelegationRun, 0, maxDelegationRuns+1)
	for i := 0; i < maxDelegationRuns; i++ {
		runs = append(runs, DelegationRun{
			ID: fmt.Sprintf("terminal-%03d", i), Status: "completed", Task: "done",
			StartedAt: int64(maxDelegationRuns - i), CompletedAt: int64(maxDelegationRuns - i),
		})
	}
	live := DelegationRun{
		ID: "live-after-cap", Status: "running", Task: "still working",
		ToProjectID: "target", StartedAt: 0,
	}
	runs = append(runs, live)
	data, err := json.Marshal(runs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(delegationRunsPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadDelegationRunsRaw()
	if err != nil {
		t.Fatalf("load oversized store: %v", err)
	}
	if len(loaded) != len(runs) {
		t.Fatalf("reader returned %d rows, want all %d before safe retention", len(loaded), len(runs))
	}
	if got, ok, err := loadDelegationRun(live.ID); err != nil || !ok || got.Status != "running" {
		t.Fatalf("live owner past cap was lost: run=%+v ok=%v err=%v", got, ok, err)
	}

	// The next mutation applies retention deliberately and may evict only a
	// terminal row. The live owner must remain addressable afterward.
	if _, err := appendDelegationRun(DelegationRun{
		ID: "new-terminal", Status: "completed", Task: "new",
		StartedAt: int64(maxDelegationRuns + 1), CompletedAt: int64(maxDelegationRuns + 1),
	}); err != nil {
		t.Fatalf("apply safe retention: %v", err)
	}
	if got, ok, err := loadDelegationRun(live.ID); err != nil || !ok || got.Status != "running" {
		t.Fatalf("safe retention evicted live owner: run=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestDelegationStoreReadErrorsAreNeverReportedAsMissingRuns(t *testing.T) {
	s, from, _, _ := delegationTestStudio(t)
	corrupt := []byte(`{"id":`)
	if err := os.WriteFile(delegationRunsPath(), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, found, err := loadDelegationRun("anything"); err == nil || found || !strings.Contains(err.Error(), "parse delegation runs") {
		t.Fatalf("load corrupt store = found=%v err=%v", found, err)
	}
	for name, call := range map[string]func() error{
		"get": func() error {
			_, err := s.GetDelegationRun("anything")
			return err
		},
		"fetch": func() error {
			_, err := s.FetchDelegationAnswer("anything", 0, 100)
			return err
		},
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "read delegation run store") || strings.Contains(err.Error(), "not found") {
			t.Fatalf("%s masked store failure: %v", name, err)
		}
	}

	handler := s.makeDelegateHandler()
	result, err := handler(withAskUserRouting(context.Background(), from.ID, "default"), "status", map[string]any{"run_id": "anything"})
	if err != nil || result.Success || !strings.Contains(result.Error, "read delegation run store") {
		t.Fatalf("delegate status masked store failure: result=%+v err=%v", result, err)
	}
	if ready, err := s.waitForDelegationBatch([]string{"anything"}); err == nil || ready {
		t.Fatalf("batch wait masked store failure: ready=%v err=%v", ready, err)
	}
	if message, err := s.batchSynthesisMessage("batch", "goal", []string{"anything"}); err == nil || message != "" {
		t.Fatalf("batch synthesis masked store failure: message=%q err=%v", message, err)
	}
	if after, err := os.ReadFile(delegationRunsPath()); err != nil || string(after) != string(corrupt) {
		t.Fatalf("read paths rewrote corrupt evidence: bytes=%q err=%v", after, err)
	}
}

func TestDelegationMonitorStopsLiveChildWhenStoreBecomesUnreadable(t *testing.T) {
	s, from, to, events := delegationTestStudio(t)
	s.mu.RLock()
	project := s.projects[to.ID]
	s.mu.RUnlock()
	session := project.GetSession("default")
	rec := &recorder{}
	project.testEmitter = rec.emit
	cancelled := make(chan struct{})
	var cancelOnce sync.Once
	session.mu.Lock()
	session.active = true
	session.mutatedThisTurn = true
	session.queuedTurns = []*queuedTurn{{ID: "storage-pending", Message: "later"}}
	session.cancelFn = func() { cancelOnce.Do(func() { close(cancelled) }) }
	session.mu.Unlock()

	run := DelegationRun{
		ID: "storage-failure", Kind: "run", FromProjectID: from.ID, FromSessionID: "default",
		ToProjectID: to.ID, ToSessionID: session.ID, Task: "work", Status: "running",
		StartedAt: time.Now().UnixMilli(),
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
	corrupt := []byte(`[{"broken"`)
	if err := os.WriteFile(delegationRunsPath(), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		s.monitorDelegationRun(run, project, session, "Infra")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not fail closed after store corruption")
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("monitor abandoned the live child without cancelling it")
	}
	s.delegationMu.Lock()
	_, live := s.delegations[run.ID]
	s.delegationMu.Unlock()
	if live {
		t.Fatal("storage-failed monitor left its registry slot occupied")
	}
	select {
	case event := <-events:
		if event.RunID != run.ID || event.Status != "error" || event.ErrorType != DelegationErrorStorage ||
			!strings.Contains(event.Error, "read delegation run store") || !event.MutatedBeforeStop {
			t.Fatalf("terminal storage event = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not surface a terminal storage event")
	}
	cleared := rec.find(EventChatQueueCleared)
	if len(cleared) != 1 {
		t.Fatalf("storage failure emitted %d queue-cleared events, want 1", len(cleared))
	}
	queueEvent, ok := cleared[0].data.(ChatQueueEvent)
	if !ok || len(queueEvent.IDs) != 1 || queueEvent.IDs[0] != "storage-pending" {
		t.Fatalf("storage queue-cleared event = %#v", cleared[0].data)
	}
	if after, err := os.ReadFile(delegationRunsPath()); err != nil || string(after) != string(corrupt) {
		t.Fatalf("monitor overwrote corrupt evidence: bytes=%q err=%v", after, err)
	}
}

func TestDelegationProgressCannotReviveTerminalRun(t *testing.T) {
	withTempConfigDir(t)
	if _, err := appendDelegationRun(DelegationRun{
		ID: "done-progress", Status: "completed", Task: "t", StartedAt: 1, CompletedAt: 2,
	}); err != nil {
		t.Fatal(err)
	}
	updated, evicted, err := updateDelegationRunProgress("done-progress", func(run *DelegationRun) {
		run.ProgressTail = []string{"late running progress"}
	})
	if err != nil || updated || len(evicted) != 0 {
		t.Fatalf("terminal progress update = updated=%v evicted=%v err=%v", updated, evicted, err)
	}
	run, ok := mustLoadDelegationRun(t, "done-progress")
	if !ok || len(run.ProgressTail) != 0 || run.Status != "completed" {
		t.Fatalf("terminal run changed: %+v, found=%v", run, ok)
	}
}

func TestCancelDelegationRunMarksTerminalWithoutLiveHandle(t *testing.T) {
	s, _, _, _ := delegationTestStudio(t)
	withTempConfigDirAlready := DelegationRun{
		ID: "run-cancel", Kind: "run", FromProjectID: "a", ToProjectID: "b",
		Task: "work", Status: "running", StartedAt: time.Now().UnixMilli(),
	}
	if _, err := appendDelegationRun(withTempConfigDirAlready); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelDelegationRun("run-cancel"); err != nil {
		t.Fatalf("CancelDelegationRun: %v", err)
	}
	got, _ := mustLoadDelegationRun(t, "run-cancel")
	if got.Status != "stopped" || got.ErrorType != DelegationErrorCancelled {
		t.Fatalf("cancelled run = %+v", got)
	}
}

func TestReconcileInterruptedDelegationRunsFlipsRunning(t *testing.T) {
	withTempConfigDir(t)
	for _, run := range []DelegationRun{
		{ID: "live", Status: "running", StartedAt: 1, Task: "t"},
		{ID: "done", Status: "completed", StartedAt: 2, CompletedAt: 3, Task: "t"},
	} {
		if _, err := appendDelegationRun(run); err != nil {
			t.Fatal(err)
		}
	}
	changed, _, err := reconcileInterruptedDelegationRuns()
	if err != nil || changed != 1 {
		t.Fatalf("reconcile = %d, %v", changed, err)
	}
	live, _ := mustLoadDelegationRun(t, "live")
	if live.Status != "stopped" || !strings.Contains(live.Error, "closed before") {
		t.Fatalf("interrupted run = %+v", live)
	}
	done, _ := mustLoadDelegationRun(t, "done")
	if done.Status != "completed" {
		t.Fatal("reconcile must not touch terminal rows")
	}
}

func TestDelegationRetentionEvictsOldestBeyondCap(t *testing.T) {
	withTempConfigDir(t)
	for i := 0; i < maxDelegationRuns; i++ {
		if _, err := appendDelegationRun(DelegationRun{
			ID: fmt.Sprintf("run-%d", i), Status: "completed",
			StartedAt: int64(i + 100), Task: "t",
		}); err != nil {
			t.Fatal(err)
		}
	}
	evicted, err := appendDelegationRun(DelegationRun{
		ID: "newest", Status: "completed", StartedAt: 1_000_000, Task: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(evicted) != 1 || evicted[0].ID != "run-0" {
		t.Fatalf("evicted = %+v, want the oldest row", evicted)
	}
	if _, ok := mustLoadDelegationRun(t, "newest"); !ok {
		t.Fatal("newest run was not retained")
	}
}

func TestFetchDelegationAnswerPagesAndBounds(t *testing.T) {
	s, _, _, _ := delegationTestStudio(t)
	body := strings.Repeat("abcdefghij", 2000) // 20 KB
	if _, err := appendDelegationRun(DelegationRun{
		ID: "run-page", Status: "completed", Task: "t",
		Answer: body, AnswerBytes: len(body), StartedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	page, err := s.FetchDelegationAnswer("run-page", 0, 0)
	if err != nil {
		t.Fatalf("FetchDelegationAnswer: %v", err)
	}
	if len(page.Text) > tools.DelegateFetchMaxBytes {
		t.Fatalf("page is %d bytes; a fetch must stay bounded", len(page.Text))
	}
	if !page.Truncated || page.FullSize != len(body) {
		t.Fatalf("page = %+v", page)
	}
	second, err := s.FetchDelegationAnswer("run-page", len(page.Text), 0)
	if err != nil || second.Offset != len(page.Text) {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	if second.Text == "" {
		t.Fatal("paging did not advance")
	}
}

func TestDelegationTailIsBoundedAndRedacted(t *testing.T) {
	lines := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		lines = append(lines, fmt.Sprintf("line %d %s", i, strings.Repeat("x", 500)))
	}
	lines = append(lines, "GLM_API_KEY=sk-abcdefghijklmnopqrstuvwxyz012345")
	tail := boundDelegationTail(lines)
	if len(tail) > maxDelegationTailLines {
		t.Fatalf("tail has %d lines, cap is %d", len(tail), maxDelegationTailLines)
	}
	for _, line := range tail {
		if len([]rune(line)) > maxDelegationTailLine {
			t.Fatalf("tail line is %d runes, cap is %d", len([]rune(line)), maxDelegationTailLine)
		}
	}
	joined := strings.Join(tail, "\n")
	if strings.Contains(joined, "sk-abcdefghijklmnopqrstuvwxyz012345") {
		t.Fatal("a secret survived into the persisted progress tail")
	}
}

// The delegation registry must never be held while taking the studio lock, or
// the two can deadlock against each other.
func TestDelegationClaimCapAndPerTargetLimit(t *testing.T) {
	s, _, _, _ := delegationTestStudio(t)
	for i := 0; i < maxConcurrentDelegations; i++ {
		if err := s.claimDelegation(fmt.Sprintf("run-%d", i), delegationHandle{
			toProjectID: fmt.Sprintf("project-%d", i), toSessionID: "s", cancel: func() {},
		}); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
	}
	err := s.claimDelegation("overflow", delegationHandle{toProjectID: "another", toSessionID: "s", cancel: func() {}})
	if DelegationErrorType(err) != DelegationErrorBusy {
		t.Fatalf("overflow error_type = %q, want busy (%v)", DelegationErrorType(err), err)
	}

	s.releaseDelegation("run-0")
	if err := s.claimDelegation("second-on-same-target", delegationHandle{
		toProjectID: "project-1", toSessionID: "other", cancel: func() {},
	}); DelegationErrorType(err) != DelegationErrorBusy {
		t.Fatalf("per-target error_type = %q, want busy (%v)", DelegationErrorType(err), err)
	}
}

func TestCancelCannotInterleavePublicationBeforeQueueClaim(t *testing.T) {
	s, from, target, _ := delegationTestStudio(t)
	reservations, err := s.reserveDelegationBatch(from.ID, "batch", []DelegationTarget{{ProjectID: target.ID}})
	if err != nil {
		t.Fatal(err)
	}
	runID := reservations[target.ID]
	run := DelegationRun{
		ID: runID, BatchID: "batch", Kind: "run", Status: "running", Task: "work",
		FromProjectID: from.ID, FromSessionID: "default",
		ToProjectID: target.ID, ToSessionID: "child", StartedAt: time.Now().UnixMilli(),
	}
	cancelled := make(chan struct{}, 1)
	handle := delegationHandle{
		fromProjectID: from.ID, toProjectID: target.ID, toSessionID: "child", batchID: "batch",
		cancel: func() { cancelled <- struct{}{} }, run: run,
	}
	claimEntered := make(chan struct{})
	releaseClaim := make(chan struct{})
	publication := make(chan error, 1)
	go func() {
		_, _, publishErr := s.publishAndStartDelegation(run, handle, true, func() error {
			close(claimEntered)
			<-releaseClaim
			return nil
		})
		publication <- publishErr
	}()
	<-claimEntered
	cancellation := make(chan error, 1)
	go func() { cancellation <- s.CancelDelegationRun(runID) }()
	select {
	case err := <-cancellation:
		t.Fatalf("cancel crossed the publication/queue-claim boundary: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseClaim)
	if err := <-publication; err != nil {
		t.Fatalf("publication failed: %v", err)
	}
	if err := <-cancellation; err != nil {
		t.Fatalf("cancel after queue claim: %v", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("published child was not synchronously stopped")
	}
	stored, ok := mustLoadDelegationRun(t, runID)
	if !ok || stored.Status != "stopped" || stored.ErrorType != DelegationErrorCancelled {
		t.Fatalf("cancelled publication = %+v, ok=%v", stored, ok)
	}
}

func TestDeleteSourceSessionCancelsOwnedDelegation(t *testing.T) {
	s, from, target, _ := delegationTestStudio(t)
	if _, err := s.CreateChatSession(from.ID); err != nil {
		t.Fatalf("create replacement source chat: %v", err)
	}
	run := DelegationRun{
		ID: "source-owned", Kind: "run", Status: "running", Task: "work",
		FromProjectID: from.ID, FromSessionID: "default",
		ToProjectID: target.ID, ToSessionID: "default", StartedAt: time.Now().UnixMilli(),
	}
	stopped := make(chan struct{}, 1)
	handle := delegationHandle{
		fromProjectID: from.ID, toProjectID: target.ID, toSessionID: "default",
		cancel: func() { stopped <- struct{}{} }, run: run,
	}
	if _, _, err := s.publishAndStartDelegation(run, handle, false, nil); err != nil {
		t.Fatalf("publish delegation: %v", err)
	}
	if err := s.DeleteChatSession(from.ID, "default"); err != nil {
		t.Fatalf("delete source chat: %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("deleting the source chat did not stop its delegated child")
	}
	stored, ok := mustLoadDelegationRun(t, run.ID)
	if !ok || stored.Status != "stopped" || stored.ErrorType != DelegationErrorCancelled {
		t.Fatalf("run after source deletion = %+v, ok=%v", stored, ok)
	}
}

func TestRemoveSourceProjectCancelsOwnedDelegation(t *testing.T) {
	s, from, target, _ := delegationTestStudio(t)
	run := DelegationRun{
		ID: "project-owned", Kind: "run", Status: "running", Task: "work",
		FromProjectID: from.ID, FromSessionID: "default",
		ToProjectID: target.ID, ToSessionID: "default", StartedAt: time.Now().UnixMilli(),
	}
	stopped := make(chan struct{}, 1)
	handle := delegationHandle{
		fromProjectID: from.ID, toProjectID: target.ID, toSessionID: "default",
		cancel: func() { stopped <- struct{}{} }, run: run,
	}
	if _, _, err := s.publishAndStartDelegation(run, handle, false, nil); err != nil {
		t.Fatalf("publish delegation: %v", err)
	}
	if err := s.RemoveProject(from.ID); err != nil {
		t.Fatalf("remove source project: %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("removing the source project did not stop its delegated child")
	}
	stored, ok := mustLoadDelegationRun(t, run.ID)
	if !ok || stored.Status != "stopped" || stored.ErrorType != DelegationErrorCancelled {
		t.Fatalf("run after source project removal = %+v, ok=%v", stored, ok)
	}
}

func TestSessionAndProjectDeletionEmitDelegationTerminalWithoutApplicationLocks(t *testing.T) {
	t.Run("session", func(t *testing.T) {
		s, from, target, _ := delegationTestStudio(t)
		if _, err := s.CreateChatSession(from.ID); err != nil {
			t.Fatal(err)
		}
		run := DelegationRun{
			ID: "session-reentrant", Kind: "run", Status: "running", Task: "work",
			FromProjectID: from.ID, FromSessionID: "default",
			ToProjectID: target.ID, ToSessionID: "default", StartedAt: time.Now().UnixMilli(),
		}
		if _, _, err := s.publishAndStartDelegation(run, delegationHandle{
			fromProjectID: from.ID, toProjectID: target.ID, toSessionID: "default",
			cancel: func() {}, run: run,
		}, false, nil); err != nil {
			t.Fatal(err)
		}
		callback := make(chan struct{}, 1)
		s.testDelegationEmitter = func(event string, _ DelegationEvent) {
			if event != EventDelegationComplete {
				return
			}
			// Re-enter both the project registry and the same project's session
			// topology. The terminal callback must run after both locks release.
			_ = s.ListProjects()
			_, _ = s.ListChatSessions(from.ID)
			callback <- struct{}{}
		}
		done := make(chan error, 1)
		go func() { done <- s.DeleteChatSession(from.ID, "default") }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("session deletion deadlocked in a reentrant delegation event hook")
		}
		select {
		case <-callback:
		default:
			t.Fatal("session deletion did not emit the terminal delegation event")
		}
	})

	t.Run("project", func(t *testing.T) {
		s, from, target, _ := delegationTestStudio(t)
		run := DelegationRun{
			ID: "project-reentrant", Kind: "run", Status: "running", Task: "work",
			FromProjectID: from.ID, FromSessionID: "default",
			ToProjectID: target.ID, ToSessionID: "default", StartedAt: time.Now().UnixMilli(),
		}
		if _, _, err := s.publishAndStartDelegation(run, delegationHandle{
			fromProjectID: from.ID, toProjectID: target.ID, toSessionID: "default",
			cancel: func() {}, run: run,
		}, false, nil); err != nil {
			t.Fatal(err)
		}
		callback := make(chan struct{}, 1)
		s.testDelegationEmitter = func(event string, _ DelegationEvent) {
			if event != EventDelegationComplete {
				return
			}
			_ = s.ListProjects()
			callback <- struct{}{}
		}
		done := make(chan error, 1)
		go func() { done <- s.RemoveProject(from.ID) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("project removal deadlocked in a reentrant delegation event hook")
		}
		select {
		case <-callback:
		default:
			t.Fatal("project removal did not emit the terminal delegation event")
		}
	})
}

func TestStopGenerationTerminalizesDelegationBeforeStoppingChild(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stopID    string
		fromMatch bool
	}{
		{name: "target child", stopID: "target"},
		{name: "source owner", stopID: "source", fromMatch: true},
		{name: "whole target project", stopID: "all-target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, from, target, _ := delegationTestStudio(t)
			run := DelegationRun{
				ID: "stop-owned", Kind: "run", Status: "running", Task: "work",
				FromProjectID: from.ID, FromSessionID: "default",
				ToProjectID: target.ID, ToSessionID: "default", StartedAt: time.Now().UnixMilli(),
			}
			child := s.projects[target.ID].GetSession("default")
			child.mu.Lock()
			child.queueWorker = true
			child.mu.Unlock()
			if _, _, err := s.publishAndStartDelegation(run, delegationHandle{
				fromProjectID: from.ID, toProjectID: target.ID, toSessionID: "default",
				session: child, cancel: func() { child.Stop() }, run: run,
			}, false, nil); err != nil {
				t.Fatal(err)
			}
			projectID, sessionID := target.ID, "default"
			switch tc.stopID {
			case "source":
				projectID = from.ID
			case "all-target":
				sessionID = ""
			}
			if err := s.StopGeneration(projectID, sessionID); err != nil {
				t.Fatalf("StopGeneration: %v", err)
			}
			stored, ok := mustLoadDelegationRun(t, run.ID)
			if !ok || stored.Status != "stopped" || stored.ErrorType != DelegationErrorCancelled {
				t.Fatalf("run after Stop = %+v, ok=%v", stored, ok)
			}
			child.mu.RLock()
			halted := child.queueHalt
			child.mu.RUnlock()
			if !halted {
				t.Fatal("Stop did not synchronously halt the delegated child")
			}
		})
	}
}

func TestDirectClearAndComputerDisableTerminalizeDelegations(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(*Studio, string) error
	}{
		{name: "clear history", act: func(s *Studio, projectID string) error {
			return s.ClearHistory(projectID, "default")
		}},
		{name: "disable computer use", act: func(s *Studio, projectID string) error {
			return s.SetProjectComputerUse(projectID, false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, from, target, _ := delegationTestStudio(t)
			run := DelegationRun{
				ID: "administrative-stop", Kind: "run", Status: "running", Task: "work",
				FromProjectID: from.ID, FromSessionID: "default",
				ToProjectID: target.ID, ToSessionID: "default", StartedAt: time.Now().UnixMilli(),
			}
			child := s.projects[target.ID].GetSession("default")
			child.mu.Lock()
			child.queueWorker = true
			child.mu.Unlock()
			if _, _, err := s.publishAndStartDelegation(run, delegationHandle{
				fromProjectID: from.ID, toProjectID: target.ID, toSessionID: "default",
				session: child, cancel: func() { child.Stop() }, run: run,
			}, false, nil); err != nil {
				t.Fatal(err)
			}
			if err := tc.act(s, target.ID); err != nil {
				t.Fatalf("administrative stop: %v", err)
			}
			stored, ok := mustLoadDelegationRun(t, run.ID)
			if !ok || stored.Status != "stopped" || stored.ErrorType != DelegationErrorCancelled {
				t.Fatalf("run after administrative stop = %+v, ok=%v", stored, ok)
			}
		})
	}
}

func TestStopAndClearEmitDelegationTerminalWithoutLifecycleLocks(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(*Studio, string) error
	}{
		{name: "stop", act: func(s *Studio, projectID string) error {
			return s.StopGeneration(projectID, "default")
		}},
		{name: "clear", act: func(s *Studio, projectID string) error {
			return s.ClearHistory(projectID, "default")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, from, target, _ := delegationTestStudio(t)
			run := DelegationRun{
				ID: "reentrant-" + tc.name, Kind: "run", Status: "running", Task: "work",
				FromProjectID: from.ID, FromSessionID: "default",
				ToProjectID: target.ID, ToSessionID: "default", StartedAt: time.Now().UnixMilli(),
			}
			child := s.projects[target.ID].GetSession("default")
			if _, _, err := s.publishAndStartDelegation(run, delegationHandle{
				fromProjectID: from.ID, fromSessionID: "default",
				toProjectID: target.ID, toSessionID: "default",
				session: child, cancel: func() { child.Stop() }, run: run,
			}, false, nil); err != nil {
				t.Fatal(err)
			}
			callback := make(chan struct{}, 1)
			s.testDelegationEmitter = func(event string, _ DelegationEvent) {
				if event != EventDelegationComplete {
					return
				}
				_ = s.ListProjects()
				_, _ = s.ListChatSessions(target.ID)
				callback <- struct{}{}
			}
			done := make(chan error, 1)
			go func() { done <- tc.act(s, target.ID) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s deadlocked in reentrant terminal callback", tc.name)
			}
			select {
			case <-callback:
			default:
				t.Fatalf("%s did not emit terminal delegation event", tc.name)
			}
		})
	}
}

func TestDeleteSessionFailureStillReportsCommittedDelegationStop(t *testing.T) {
	s, from, target, terminalEvents := delegationTestStudio(t)
	childInfo, err := s.CreateChatSession(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	project := s.projects[target.ID]
	rec := &recorder{}
	project.testEmitter = rec.emit
	child := project.GetSession(childInfo.ID)
	child.mu.Lock()
	child.queueWorker = true
	child.queuedTurns = []*queuedTurn{{ID: "pending", Message: "later"}}
	child.mu.Unlock()
	run := DelegationRun{
		ID: "delete-failure-stop", Kind: "run", Status: "running", Task: "work",
		FromProjectID: from.ID, FromSessionID: "default",
		ToProjectID: target.ID, ToSessionID: child.ID, StartedAt: time.Now().UnixMilli(),
	}
	if _, _, err := s.publishAndStartDelegation(run, delegationHandle{
		fromProjectID: from.ID, fromSessionID: "default",
		toProjectID: target.ID, toSessionID: child.ID,
		session: child, cancel: func() { child.Stop() }, run: run,
	}, false, nil); err != nil {
		t.Fatal(err)
	}
	path := historyPath(projectSessionStorageKey(target.ID, child.ID))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "child"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteChatSession(target.ID, child.ID); err == nil {
		t.Fatal("expected history deletion failure")
	}
	if project.GetSession(child.ID) != child {
		t.Fatal("failed deletion removed the child from memory")
	}
	stored, ok := mustLoadDelegationRun(t, run.ID)
	if !ok || stored.Status != "stopped" || stored.ErrorType != DelegationErrorCancelled {
		t.Fatalf("delegation stop was rolled back with deletion: %+v, ok=%v", stored, ok)
	}
	select {
	case event := <-terminalEvents:
		if event.RunID != run.ID || event.Status != "stopped" {
			t.Fatalf("terminal event = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failed deletion hid the committed delegation stop")
	}
	cleared := rec.find(EventChatQueueCleared)
	if len(cleared) != 1 {
		t.Fatalf("queue-cleared events = %d, want 1", len(cleared))
	}
	event, ok := cleared[0].data.(ChatQueueEvent)
	if !ok || len(event.IDs) != 1 || event.IDs[0] != "pending" {
		t.Fatalf("queue-cleared event = %#v", cleared[0].data)
	}
}

func TestQuitSummaryCountsStartingDelegationWithoutDoubleCounting(t *testing.T) {
	s, _, to, _ := delegationTestStudio(t)
	s.mu.RLock()
	target := s.projects[to.ID]
	s.mu.RUnlock()
	session := target.GetSession("default")

	if err := s.claimDelegation("run-quit", delegationHandle{
		toProjectID: to.ID, toSessionID: session.ID, cancel: func() {},
	}); err != nil {
		t.Fatal(err)
	}
	// Child not yet active: the delegation is the only evidence of work.
	if summary := s.quitWorkSummary(); summary.Delegations != 1 || !summary.hasWork() {
		t.Fatalf("summary before child active = %+v", summary)
	}
	// Once the child turn is running it is already counted as a session, and
	// counting it twice would overstate the work at risk.
	session.mu.Lock()
	session.queueWorker = true
	session.mu.Unlock()
	summary := s.quitWorkSummary()
	if summary.Delegations != 0 || summary.RunningSessions != 1 {
		t.Fatalf("summary with active child = %+v", summary)
	}
}

func TestDelegationRegistryIsConcurrencySafe(t *testing.T) {
	s, _, _, _ := delegationTestStudio(t)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("run-%d", i)
			_ = s.claimDelegation(id, delegationHandle{
				toProjectID: fmt.Sprintf("p-%d", i), toSessionID: "s", cancel: func() {},
			})
			_ = s.quitWorkSummary()
			s.releaseDelegation(id)
		}(i)
	}
	wg.Wait()
	s.delegationMu.Lock()
	remaining := len(s.delegations)
	s.delegationMu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d delegation handles leaked", remaining)
	}
}

func TestCrossAgentEnvelopeKeepsInjectionFooter(t *testing.T) {
	envelope := crossAgentEnvelope("Caller", "Chat 1", "p1", "s1", "ship the fix", "stack=shop", "shop", "Redeploy")
	for _, want := range []string{"## Goal", "## Requested by", `## Shared context (group "shop")`, "## Task", crossAgentInjectionFooter} {
		if !strings.Contains(envelope, want) {
			t.Fatalf("envelope missing %q:\n%s", want, envelope)
		}
	}
	// The same footer must guard the session_agent path, so the two cannot drift.
	if !strings.Contains(attributedSessionMessage("P", "S", "p", "s", "hi"), crossAgentInjectionFooter) {
		t.Fatal("attributedSessionMessage no longer carries the shared injection footer")
	}
}

func TestCrossAgentEnvelopeOmitsEmptySections(t *testing.T) {
	envelope := crossAgentEnvelope("Caller", "Chat 1", "p1", "s1", "", "", "", "Just do it")
	if strings.Contains(envelope, "## Goal") || strings.Contains(envelope, "## Shared context") {
		t.Fatalf("empty sections rendered:\n%s", envelope)
	}
}

// Studio.mu read locks are not reentrant: Go parks new readers behind a queued
// writer, so taking s.mu.RLock again while already holding it wedges the
// goroutine — and with it every later s.mu operation, including Shutdown.
//
// Project.totalCostUSD lazily seeds its cache through Studio.ProjectUsageStats,
// which takes s.mu.RLock. The budget preflight must therefore run OUTSIDE the
// read-locked region that startDelegation holds across create+claim.
func TestDelegationBudgetPreflightDoesNotReadLockStudioRecursively(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	s.mu.RLock()
	target := s.projects[to.ID]
	s.mu.RUnlock()
	// EnforceBudget with an unseeded cost cache is what forces the lazy seed.
	target.mu.Lock()
	target.EnforceBudget, target.BudgetUSD = true, 100
	target.mu.Unlock()
	target.costMu.Lock()
	target.costSeeded = false
	target.costMu.Unlock()

	writerParked := make(chan struct{})
	go func() {
		close(writerParked)
		s.mu.Lock()
		s.mu.Unlock()
	}()
	<-writerParked
	// Give the writer time to actually park; only then does Go block new readers.
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		_, _ = s.StartDelegation(from.ID, "default", to.ID, "ask", "", "quick question")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StartDelegation blocked behind a pending writer: s.mu was read-locked recursively")
	}
}

func TestBatchPreflightDoesNotReadLockStudioRecursively(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	s.mu.RLock()
	target := s.projects[to.ID]
	s.mu.RUnlock()
	target.mu.Lock()
	target.EnforceBudget, target.BudgetUSD = true, 100
	target.mu.Unlock()
	target.costMu.Lock()
	target.costSeeded = false
	target.costMu.Unlock()

	writerParked := make(chan struct{})
	go func() {
		close(writerParked)
		s.mu.Lock()
		s.mu.Unlock()
	}()
	<-writerParked
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		_ = s.preflightDelegationBatch(from.ID, []DelegationTarget{{ProjectID: to.ID, Task: "t"}}, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("batch preflight blocked behind a pending writer: s.mu was read-locked recursively")
	}
}

// A bounded question must not be able to act. The restriction only takes effect
// when executionProvider/Model are set too — without them sendMessage skips the
// whole override block and the child gets the target's FULL tool set in its real
// checkout, which is strictly less isolated than kind "run".
func TestAskDelegationSessionCarriesTheToolRestrictionFields(t *testing.T) {
	s, _, to, _ := delegationTestStudio(t)
	s.mu.RLock()
	target := s.projects[to.ID]
	s.mu.RUnlock()

	session, err := createDelegationSession(target, "ask", "Caller", time.Now())
	if err != nil {
		t.Fatalf("createDelegationSession: %v", err)
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	if session.executionAllowedTools == nil || len(session.executionAllowedTools) != 0 {
		t.Fatalf("allowed tools = %#v, want an empty non-nil map (zero tools)", session.executionAllowedTools)
	}
	if session.executionProvider == "" || session.executionModel == "" {
		t.Fatal("ask session has no execution provider/model, so the tool restriction is silently ignored")
	}
	if session.executionSystemPrompt != delegationAskSystemPrompt {
		t.Fatalf("ask session prompt = %q", session.executionSystemPrompt)
	}
	if !session.delegateChild {
		t.Fatal("ask session is not marked as a delegation child")
	}
}

func TestRunDelegationSessionKeepsTargetDefaults(t *testing.T) {
	s, _, to, _ := delegationTestStudio(t)
	s.mu.RLock()
	target := s.projects[to.ID]
	s.mu.RUnlock()

	session, err := createDelegationSession(target, "run", "Caller", time.Now())
	if err != nil {
		t.Fatalf("createDelegationSession: %v", err)
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	// "run" is an ordinary, user-steerable tab: no overrides, full tool set.
	if session.executionAllowedTools != nil {
		t.Fatalf("run session restricted tools to %#v; it should use the target's own set", session.executionAllowedTools)
	}
	if session.executionProvider != "" || session.executionModel != "" {
		t.Fatal("run session pinned provider/model; it should follow the project")
	}
}

// A page boundary must never split a multibyte character, and the offset the
// caller pages from must be the one we actually stopped at.
func TestFetchDelegationAnswerCutsOnRuneBoundaries(t *testing.T) {
	s, _, _, _ := delegationTestStudio(t)
	// Three-byte runes guarantee a cut lands mid-character for some page size.
	body := strings.Repeat("э", 4000)
	if _, err := appendDelegationRun(DelegationRun{
		ID: "run-runes", Status: "completed", Task: "t",
		Answer: body, AnswerBytes: len(body), StartedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	offset, assembled, guard := 0, strings.Builder{}, 0
	for {
		guard++
		if guard > 500 {
			t.Fatal("paging did not terminate")
		}
		page, err := s.FetchDelegationAnswer("run-runes", offset, 1000)
		if err != nil {
			t.Fatalf("FetchDelegationAnswer: %v", err)
		}
		if !utf8.ValidString(page.Text) {
			t.Fatalf("page at offset %d split a rune", offset)
		}
		if page.Text == "" {
			break
		}
		assembled.WriteString(page.Text)
		offset += len(page.Text)
		if !page.Truncated {
			break
		}
	}
	if assembled.String() != body {
		t.Fatalf("paging lost or duplicated bytes: got %d of %d", assembled.Len(), len(body))
	}
}

func TestFetchDelegationAnswerAlwaysAdvancesAcrossWideRunes(t *testing.T) {
	s, _, _, _ := delegationTestStudio(t)
	body := "😀эx"
	if _, err := appendDelegationRun(DelegationRun{
		ID: "run-wide-runes", Status: "completed", Task: "t",
		Answer: body, AnswerBytes: len(body), StartedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	first, err := s.FetchDelegationAnswer("run-wide-runes", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Offset != 0 || first.Text != "😀" || !first.Truncated {
		t.Fatalf("one-byte request did not normalize to one complete rune: %+v", first)
	}
	if next := first.Offset + len(first.Text); next <= first.Offset {
		t.Fatalf("page did not advance: %+v", first)
	}

	// Offset 2 is inside the four-byte emoji. It is normalized back to zero,
	// reported honestly, and never produces replacement bytes or an empty loop.
	mid, err := s.FetchDelegationAnswer("run-wide-runes", 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if mid.Offset != 0 || mid.Text != "😀" || !utf8.ValidString(mid.Text) {
		t.Fatalf("mid-rune offset was not normalized safely: %+v", mid)
	}
}

// Cancelling something already finished must not claim a cancel that did not
// happen — the durable row and the panel would disagree with the tool.
func TestCancelDelegationRunReportsAlreadyTerminal(t *testing.T) {
	s, _, _, _ := delegationTestStudio(t)
	if _, err := appendDelegationRun(DelegationRun{
		ID: "run-done", Status: "completed", Task: "t", StartedAt: 1, CompletedAt: 2,
	}); err != nil {
		t.Fatal(err)
	}
	err := s.CancelDelegationRun("run-done")
	if err == nil || !strings.Contains(err.Error(), "already finished") {
		t.Fatalf("cancel of a terminal run = %v, want an already-finished error", err)
	}
	if got, _ := mustLoadDelegationRun(t, "run-done"); got.Status != "completed" {
		t.Fatalf("terminal row was mutated by a cancel: %+v", got)
	}
}

// A fan-out must not be a way around the depth and cycle rules.
func TestBatchCarriesTheCallerChainStamp(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	// The caller is already at the depth limit.
	deep := &delegationStamp{ChainID: "c", Depth: maxDelegationDepth, Chain: []string{from.ID}}
	err := s.preflightDelegationBatch(from.ID, []DelegationTarget{{ProjectID: to.ID, Task: "t"}}, deep)
	if DelegationErrorType(err) != DelegationErrorDepthLimit {
		t.Fatalf("batch at max depth = %q, want depth_limit (%v)", DelegationErrorType(err), err)
	}
	// And a target already in the chain is a cycle.
	cyclic := &delegationStamp{ChainID: "c", Depth: 1, Chain: []string{from.ID, to.ID}}
	err = s.preflightDelegationBatch(from.ID, []DelegationTarget{{ProjectID: to.ID, Task: "t"}}, cyclic)
	if DelegationErrorType(err) != DelegationErrorCycle {
		t.Fatalf("batch cycle = %q, want cycle (%v)", DelegationErrorType(err), err)
	}
}

// The gate must consider work already in flight, not just the size of this batch.
func TestBatchPreflightCountsInFlightDelegations(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	for i := 0; i < maxConcurrentDelegations; i++ {
		if err := s.claimDelegation(fmt.Sprintf("busy-%d", i), delegationHandle{
			toProjectID: fmt.Sprintf("other-%d", i), toSessionID: "s", cancel: func() {},
		}); err != nil {
			t.Fatal(err)
		}
	}
	err := s.preflightDelegationBatch(from.ID, []DelegationTarget{{ProjectID: to.ID, Task: "t"}}, nil)
	if DelegationErrorType(err) != DelegationErrorBusy {
		t.Fatalf("saturated studio = %q, want busy (%v)", DelegationErrorType(err), err)
	}
}

// The row cap alone cannot keep the file under its byte cap, and hard-failing
// would silently stop recording every future delegation.
func TestDelegationStoreTrimsToStayUnderTheByteCap(t *testing.T) {
	withTempConfigDir(t)
	big := strings.Repeat("x", 200<<10) // 200 KiB of task text per row
	reported := make(map[string]bool)
	for i := 0; i < 40; i++ {
		evicted, err := appendDelegationRun(DelegationRun{
			ID: fmt.Sprintf("fat-%d", i), Status: "completed",
			Task: big, StartedAt: int64(i + 1),
		})
		if err != nil {
			t.Fatalf("append %d failed; the store must trim rather than refuse: %v", i, err)
		}
		for _, run := range evicted {
			reported[run.ID] = true
		}
	}
	info, err := os.Stat(delegationRunsPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxDelegationRunFile {
		t.Fatalf("store is %d bytes, cap is %d", info.Size(), maxDelegationRunFile)
	}
	// The newest row always survives.
	if _, ok := mustLoadDelegationRun(t, "fat-39"); !ok {
		t.Fatal("the most recent delegation was dropped")
	}
	stored, err := loadDelegationRunsRaw()
	if err != nil {
		t.Fatal(err)
	}
	retained := make(map[string]bool, len(stored))
	for _, run := range stored {
		retained[run.ID] = true
	}
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("fat-%d", i)
		if !retained[id] && !reported[id] {
			t.Fatalf("%s disappeared from the byte-capped store without being reported for child cleanup", id)
		}
	}
	if len(reported) == 0 {
		t.Fatal("test did not exercise byte-based eviction")
	}
}

func TestDelegationStoreNeverEvictsLiveRows(t *testing.T) {
	big := strings.Repeat("x", 256<<10)
	runs := make([]DelegationRun, 0, 24)
	runs = append(runs, DelegationRun{
		ID: "old-live", Status: "running", Task: big, StartedAt: 1,
	})
	for i := 0; i < 23; i++ {
		runs = append(runs, DelegationRun{
			ID: fmt.Sprintf("done-%d", i), Status: "completed", Task: big,
			StartedAt: int64(100 + i), CompletedAt: int64(200 + i),
		})
	}
	kept, evicted, data, err := fitDelegationRuns(runs)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxDelegationRunFile {
		t.Fatalf("fitted data is %d bytes, cap is %d", len(data), maxDelegationRunFile)
	}
	liveKept := false
	for _, run := range kept {
		if run.ID == "old-live" {
			liveKept = true
		}
	}
	if !liveKept {
		t.Fatal("the oldest live delegation was evicted before terminal history")
	}
	for _, run := range evicted {
		if !delegationRunTerminal(run.Status) {
			t.Fatalf("live run %s was reported as evicted", run.ID)
		}
	}
	if len(evicted) == 0 || evicted[0].ID != "done-0" {
		t.Fatalf("eviction did not start with the oldest terminal row: %+v", evicted)
	}
	newestKept := false
	for _, run := range kept {
		if run.ID == "done-22" {
			newestKept = true
		}
	}
	if !newestKept {
		t.Fatal("newest terminal history was dropped before older rows")
	}
}

func TestFinishDelegationReportsByteEvictions(t *testing.T) {
	withTempConfigDir(t)
	big := strings.Repeat("x", 200<<10)
	for i := 0; i < 18; i++ {
		if _, err := appendDelegationRun(DelegationRun{
			ID: fmt.Sprintf("history-%d", i), Status: "completed", Task: big,
			StartedAt: int64(i + 1), CompletedAt: int64(i + 2),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := appendDelegationRun(DelegationRun{
		ID: "finishing", Status: "running", Task: big, StartedAt: 1_000,
	}); err != nil {
		t.Fatal(err)
	}
	_, changed, evicted, err := finishDelegationRun("finishing", "completed", "", nil, func(run *DelegationRun) {
		run.Answer = strings.Repeat("answer", 60<<10)
	})
	if err != nil || !changed {
		t.Fatalf("finish changed=%v err=%v", changed, err)
	}
	if len(evicted) == 0 {
		t.Fatal("finishing a near-cap store did not report the terminal rows it displaced")
	}
	for _, run := range evicted {
		if !delegationRunTerminal(run.Status) {
			t.Fatalf("finish evicted live run %+v", run)
		}
	}
}
