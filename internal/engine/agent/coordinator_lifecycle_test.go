package agent

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func TestCoordinatorCompletionIsDurableAndStartIsIdempotent(t *testing.T) {
	coordinator := NewCoordinator(context.Background(), nil, nil)
	var allCompleteCalls atomic.Int32
	coordinator.SetCallbacks(nil, nil, func(results map[string]*AgentResult) {
		allCompleteCalls.Add(1)
		if len(results) != 0 {
			t.Errorf("empty coordinator results = %v", results)
		}
		_ = coordinator.GetStatus() // Callback re-entry must not deadlock.
		panic("observer failure must be isolated")
	})

	coordinator.Start()
	coordinator.Start()

	const waiters = 4
	var wg sync.WaitGroup
	wg.Add(waiters)
	for range waiters {
		go func() {
			defer wg.Done()
			if results := coordinator.Wait(); len(results) != 0 {
				t.Errorf("Wait results = %v, want empty", results)
			}
		}()
	}
	waitForWaitGroup(t, &wg)

	results, err := coordinator.WaitWithTimeout(time.Second)
	if err != nil || len(results) != 0 {
		t.Fatalf("late WaitWithTimeout = %v, %v", results, err)
	}
	if got := allCompleteCalls.Load(); got != 1 {
		t.Fatalf("all-complete callback calls = %d, want 1", got)
	}
	if taskID := coordinator.AddTask("too late", AgentTypeGeneral, PriorityNormal, nil); taskID != "" {
		t.Fatalf("AddTask after completion returned %q, want empty ID", taskID)
	}
}

func TestCoordinatorStopCancelsRunningAgentAndFinalizesTask(t *testing.T) {
	dir := t.TempDir()
	provider := &blockingAgentClient{entered: make(chan struct{})}
	runner := NewRunner(context.Background(), provider, tools.DefaultRegistry(dir), dir)
	coordinator := NewCoordinator(context.Background(), runner, &CoordinatorConfig{MaxParallel: 1})
	taskID := coordinator.AddTask("wait until cancelled", AgentTypeExplore, PriorityNormal, nil)

	started := make(chan struct{}, 1)
	completed := make(chan *AgentResult, 1)
	coordinator.SetCallbacks(func(task *CoordinatedTask) {
		if snapshot := coordinator.GetTask(task.ID); snapshot == nil {
			t.Error("start callback could not re-enter coordinator")
		}
		started <- struct{}{}
	}, func(task *CoordinatedTask, result *AgentResult) {
		completed <- cloneAgentResult(result)
		_ = coordinator.GetStatus() // Completion callback re-entry must not deadlock.
		panic("observer failure must be isolated")
	}, nil)

	coordinator.Start()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task start callback was not delivered")
	}
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("agent did not reach provider")
	}
	agentID := waitForCoordinatorAgent(t, coordinator, taskID)

	resultCh := make(chan map[string]*AgentResult, 1)
	go func() { resultCh <- coordinator.Wait() }()
	coordinator.Stop()
	coordinator.Stop()

	var results map[string]*AgentResult
	select {
	case results = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("Wait did not finish after Stop")
	}
	result := results[taskID]
	if result == nil || result.Status != AgentStatusCancelled || !result.Completed || result.AgentID != agentID {
		t.Fatalf("coordinator cancellation result = %+v", result)
	}
	select {
	case callbackResult := <-completed:
		if callbackResult == nil || callbackResult.Status != AgentStatusCancelled || !callbackResult.Completed {
			t.Fatalf("completion callback result = %+v", callbackResult)
		}
	case <-time.After(time.Second):
		t.Fatal("completion callback was not delivered on Stop")
	}

	runnerResult, err := runner.WaitWithTimeout(agentID, 2*time.Second)
	if err != nil || runnerResult == nil || runnerResult.Status != AgentStatusCancelled || !runnerResult.Completed {
		t.Fatalf("runner cancellation result = %+v, %v", runnerResult, err)
	}
	status := coordinator.GetStatus()
	if status.RunningTasks != 0 || status.CompletedTasks != 1 || status.FailedTasks != 1 {
		t.Fatalf("coordinator status after Stop = %+v", status)
	}
}

func TestCoordinatorCancelTaskUnblocksDependents(t *testing.T) {
	dir := t.TempDir()
	provider := &blockingAgentClient{entered: make(chan struct{})}
	runner := NewRunner(context.Background(), provider, tools.DefaultRegistry(dir), dir)
	coordinator := NewCoordinator(context.Background(), runner, &CoordinatorConfig{MaxParallel: 1})
	firstID := coordinator.AddTask("first", AgentTypeExplore, PriorityNormal, nil)
	secondID := coordinator.AddTask("second", AgentTypeExplore, PriorityNormal, []string{firstID})
	coordinator.Start()

	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("first agent did not start")
	}
	waitForCoordinatorAgent(t, coordinator, firstID)
	if err := coordinator.CancelTask(firstID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	secondAgentID := waitForCoordinatorAgent(t, coordinator, secondID)
	if secondAgentID == "" {
		t.Fatal("dependent task did not start after dependency cancellation")
	}
	first := coordinator.GetTask(firstID)
	if first == nil || first.Status != TaskStatusFailed || first.Result == nil || !first.Result.Completed {
		t.Fatalf("cancelled task = %+v", first)
	}

	coordinator.Stop()
	_, _ = runner.WaitWithTimeout(secondAgentID, 2*time.Second)
}

func TestCoordinatorStartCallbackCanCancelBeforeSpawn(t *testing.T) {
	coordinator := NewCoordinator(context.Background(), nil, nil)
	taskID := coordinator.AddTask("cancel from callback", AgentTypeGeneral, PriorityNormal, nil)
	callbackDone := make(chan struct{})
	coordinator.SetCallbacks(func(task *CoordinatedTask) {
		if err := coordinator.CancelTask(task.ID); err != nil {
			t.Errorf("CancelTask from start callback: %v", err)
		}
		close(callbackDone)
	}, nil, nil)

	coordinator.Start()
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("start callback did not finish")
	}
	results, err := coordinator.WaitWithTimeout(time.Second)
	if err != nil {
		t.Fatalf("WaitWithTimeout: %v", err)
	}
	result := results[taskID]
	if result == nil || result.Status != AgentStatusCancelled || !result.Completed {
		t.Fatalf("callback-cancelled result = %+v", result)
	}
}

func TestCoordinatorReturnsDeepSnapshots(t *testing.T) {
	coordinator := NewCoordinator(context.Background(), nil, nil)
	dependencyID := coordinator.AddTask("dependency", AgentTypeGeneral, PriorityNormal, nil)
	deps := []string{dependencyID}
	taskID := coordinator.AddTask("snapshot", AgentTypeGeneral, PriorityNormal, deps)
	deps[0] = "caller mutation"

	coordinator.mu.Lock()
	coordinator.tasks[taskID].Result = &AgentResult{
		Status:    AgentStatusCompleted,
		Completed: true,
		Metadata: map[string]interface{}{
			"nested": map[string]interface{}{"values": []interface{}{"owned"}},
		},
	}
	coordinator.mu.Unlock()

	first := coordinator.GetTask(taskID)
	first.Dependencies[0] = "snapshot mutation"
	first.Result.Metadata["nested"].(map[string]interface{})["values"].([]interface{})[0] = "changed"
	second := coordinator.GetTask(taskID)
	if !reflect.DeepEqual(second.Dependencies, []string{dependencyID}) {
		t.Fatalf("stored dependencies = %v", second.Dependencies)
	}
	nested := second.Result.Metadata["nested"].(map[string]interface{})["values"].([]interface{})
	if !reflect.DeepEqual(nested, []interface{}{"owned"}) {
		t.Fatalf("stored metadata = %v", nested)
	}

	all := coordinator.GetAllTasks()
	for _, task := range all {
		if task.ID == taskID {
			task.Prompt = "changed"
		}
	}
	if got := coordinator.GetTask(taskID).Prompt; got != "snapshot" {
		t.Fatalf("stored prompt = %q", got)
	}
}

func TestCoordinatorCleanupRetainsCompletionNeededByBlockedDependent(t *testing.T) {
	coordinator := NewCoordinator(context.Background(), nil, nil)
	firstID := coordinator.AddTask("first dependency", AgentTypeGeneral, PriorityHigh, nil)
	lastID := coordinator.AddTask("last dependency", AgentTypeGeneral, PriorityLow, nil)
	dependentID := coordinator.AddTask(
		"wait for both",
		AgentTypeGeneral,
		PriorityNormal,
		[]string{firstID, lastID},
	)
	completeCoordinatorTaskForTest(t, coordinator, firstID)

	for i := 0; i < MaxCoordinatorTasks+1; i++ {
		taskID := coordinator.AddTask("completed filler", AgentTypeGeneral, PriorityNormal, nil)
		completeCoordinatorTaskForTest(t, coordinator, taskID)
	}
	coordinator.cleanupCompletedTasks()

	coordinator.mu.RLock()
	firstRetained := coordinator.completed[firstID] && coordinator.tasks[firstID] != nil
	dependentStatus := coordinator.tasks[dependentID].Status
	coordinator.mu.RUnlock()
	if !firstRetained {
		t.Fatal("cleanup discarded a completion tombstone still needed by a blocked dependent")
	}
	if dependentStatus != TaskStatusBlocked {
		t.Fatalf("dependent status before final prerequisite = %s", dependentStatus)
	}

	completeCoordinatorTaskForTest(t, coordinator, lastID)
	if task := coordinator.GetTask(dependentID); task == nil || task.Status != TaskStatusReady {
		t.Fatalf("dependent after final prerequisite = %+v", task)
	}
}

func TestCoordinatorCleanupEvictsOldestEligibleTasks(t *testing.T) {
	coordinator := NewCoordinator(context.Background(), nil, nil)
	taskIDs := make([]string, 0, MaxCoordinatorTasks+2)
	for i := 0; i < MaxCoordinatorTasks+2; i++ {
		taskID := coordinator.AddTask("completed", AgentTypeGeneral, PriorityNormal, nil)
		taskIDs = append(taskIDs, taskID)
		completeCoordinatorTaskForTest(t, coordinator, taskID)
	}
	coordinator.cleanupCompletedTasks()

	wantRemaining := MaxCoordinatorTasks / 2
	coordinator.mu.RLock()
	defer coordinator.mu.RUnlock()
	if len(coordinator.completed) != wantRemaining || len(coordinator.completedOrder) != wantRemaining {
		t.Fatalf("retained completed=%d order=%d, want %d", len(coordinator.completed), len(coordinator.completedOrder), wantRemaining)
	}
	cut := len(taskIDs) - wantRemaining
	for i, taskID := range taskIDs {
		_, retained := coordinator.completed[taskID]
		if retained != (i >= cut) {
			t.Fatalf("task %d retained=%v, want %v", i, retained, i >= cut)
		}
	}
	if !reflect.DeepEqual(coordinator.completedOrder, taskIDs[cut:]) {
		t.Fatalf("completion order = %v, want %v", coordinator.completedOrder, taskIDs[cut:])
	}
}

func TestCoordinatorRejectsImpossibleDependencies(t *testing.T) {
	coordinator := NewCoordinator(context.Background(), nil, nil)
	if taskID := coordinator.AddTask("unknown", AgentTypeGeneral, PriorityNormal, []string{"missing"}); taskID != "" {
		t.Fatalf("unknown dependency task ID = %q, want empty", taskID)
	}
	dependencyID := coordinator.AddTask("known", AgentTypeGeneral, PriorityNormal, nil)
	if taskID := coordinator.AddTask(
		"duplicate",
		AgentTypeGeneral,
		PriorityNormal,
		[]string{dependencyID, dependencyID},
	); taskID != "" {
		t.Fatalf("duplicate dependency task ID = %q, want empty", taskID)
	}
	if tasks := coordinator.GetAllTasks(); len(tasks) != 1 || tasks[0].ID != dependencyID {
		t.Fatalf("tasks after rejected additions = %+v", tasks)
	}
}

func completeCoordinatorTaskForTest(t *testing.T, coordinator *Coordinator, taskID string) {
	t.Helper()
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	task := coordinator.tasks[taskID]
	if task == nil {
		t.Fatalf("task %q is missing", taskID)
	}
	coordinator.queue.RemoveTask(taskID)
	task.Status = TaskStatusCompleted
	task.Result = &AgentResult{Status: AgentStatusCompleted, Completed: true}
	coordinator.markCompletedLocked(taskID)
	coordinator.unblockDependents(taskID)
}

func waitForCoordinatorAgent(t *testing.T, coordinator *Coordinator, taskID string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if agentID := coordinator.GetTaskAgentID(taskID); agentID != "" {
			return agentID
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task %s was not assigned an agent", taskID)
	return ""
}

func waitForWaitGroup(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waiters did not finish")
	}
}
