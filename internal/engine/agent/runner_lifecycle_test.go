package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func assertPreCancelledAsyncLifecycle(
	t *testing.T,
	runner *Runner,
	start func(context.Context) (string, error),
) string {
	t.Helper()

	type completion struct {
		id     string
		result *AgentResult
	}
	completed := make(chan completion, 1)
	runner.SetOnAgentComplete(func(id string, result *AgentResult) {
		completed <- completion{id: id, result: result}
	})

	var activityMu sync.Mutex
	var activities []string
	runner.SetOnSubAgentActivity(func(_, _, _ string, _ map[string]any, status string) {
		activityMu.Lock()
		activities = append(activities, status)
		activityMu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	agentID, err := start(ctx)
	if err != nil {
		t.Fatalf("start pre-cancelled agent: %v", err)
	}
	result, err := runner.WaitWithTimeout(agentID, 2*time.Second)
	if err != nil || result == nil || result.Status != AgentStatusCancelled || !result.Completed {
		t.Fatalf("pre-cancelled result = %+v, %v", result, err)
	}

	select {
	case callback := <-completed:
		if callback.id != agentID || callback.result == nil || callback.result.Status != AgentStatusCancelled {
			t.Fatalf("completion callback = %+v", callback)
		}
	case <-time.After(time.Second):
		t.Fatal("completion callback was not delivered for pre-cancelled agent")
	}

	activityMu.Lock()
	gotActivities := append([]string(nil), activities...)
	activityMu.Unlock()
	if !reflect.DeepEqual(gotActivities, []string{"start", "cancelled"}) {
		t.Fatalf("lifecycle activities = %v, want [start cancelled]", gotActivities)
	}
	agent, ok := runner.GetAgent(agentID)
	if !ok || agent.GetStatus() != AgentStatusCancelled || agent.GetEndTime().IsZero() {
		t.Fatalf("cancelled agent lifecycle = %+v", agent)
	}
	return agentID
}

func TestAgentPendingCancellationIsSticky(t *testing.T) {
	agent := &Agent{ID: "agent-1", Type: AgentTypeGeneral, status: AgentStatusPending}
	agent.Cancel()
	ctx, cancel := context.WithCancel(context.Background())
	agent.SetCancelFunc(cancel)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("late cancel function was not invoked")
	}
	result, err := agent.Run(ctx, "must not execute")
	if !errors.Is(err, context.Canceled) || result == nil || result.Status != AgentStatusCancelled || !result.Completed {
		t.Fatalf("Run after pending cancellation = %+v, %v", result, err)
	}
}

func TestRunnerCancelDoesNotPrematurelyCompleteResult(t *testing.T) {
	runner := NewRunner(context.Background(), nil, nil, t.TempDir())
	agent := &Agent{ID: "agent-1", Type: AgentTypeGeneral, status: AgentStatusPending}
	runner.agents[agent.ID] = agent
	runner.results[agent.ID] = &AgentResult{AgentID: agent.ID, Status: AgentStatusPending}
	runner.activeExecutions[agent.ID] = 1

	if err := runner.Cancel(agent.ID); err != nil {
		t.Fatal(err)
	}
	result, ok := runner.GetResult(agent.ID)
	if !ok || result.Status != AgentStatusCancelled || result.Completed {
		t.Fatalf("cancelled result=%+v, ok=%v", result, ok)
	}
	if err := runner.Cancel(agent.ID); err == nil {
		t.Fatal("second cancellation of terminal agent succeeded")
	}
}

func TestAsyncSpawnPreCancelledDeliversTerminalLifecycle(t *testing.T) {
	tests := []struct {
		name  string
		start func(*Runner, context.Context) string
	}{
		{
			name: "plain",
			start: func(runner *Runner, ctx context.Context) string {
				return runner.SpawnAsync(ctx, "explore", "do not run", 1, "")
			},
		},
		{
			name: "streaming",
			start: func(runner *Runner, ctx context.Context) string {
				return runner.SpawnAsyncWithStreaming(ctx, "explore", "do not run", 1, "", nil, nil)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			runner := NewRunner(context.Background(), nil, tools.DefaultRegistry(dir), dir)
			assertPreCancelledAsyncLifecycle(t, runner, func(ctx context.Context) (string, error) {
				return test.start(runner, ctx), nil
			})
		})
	}
}

func TestAsyncResumePreCancelledDeliversTerminalLifecycle(t *testing.T) {
	tests := []struct {
		name  string
		start func(*Runner, *AgentStore, context.Context, string) (string, error)
	}{
		{
			name: "saved state",
			start: func(runner *Runner, _ *AgentStore, ctx context.Context, agentID string) (string, error) {
				return runner.ResumeAsync(ctx, agentID, "do not resume")
			},
		},
		{
			name: "last checkpoint",
			start: func(runner *Runner, store *AgentStore, ctx context.Context, agentID string) (string, error) {
				if err := store.SaveCheckpoint(&AgentCheckpoint{
					AgentState:   &AgentState{ID: agentID, Type: AgentTypeExplore, Status: AgentStatusPending, MaxTurns: 1},
					CheckpointID: agentID + "-checkpoint",
					Timestamp:    time.Now(),
				}); err != nil {
					return "", err
				}
				return runner.ResumeLastCheckpoint(ctx)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := NewAgentStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			agentID := "resume-agent"
			if err := store.SaveState(&AgentState{
				ID: agentID, Type: AgentTypeExplore, Status: AgentStatusPending, MaxTurns: 1,
			}); err != nil {
				t.Fatal(err)
			}

			runner := NewRunner(context.Background(), nil, tools.DefaultRegistry(dir), dir)
			runner.SetStore(store)
			resumedID := assertPreCancelledAsyncLifecycle(t, runner, func(ctx context.Context) (string, error) {
				return test.start(runner, store, ctx, agentID)
			})

			state, err := store.Load(resumedID)
			if err != nil || state.Status != AgentStatusCancelled || state.EndTime.IsZero() {
				t.Fatalf("persisted cancelled state = %+v, %v", state, err)
			}
		})
	}
}

func TestRunnerCleanupWaitsForExecutionAndRetriesOutputRemoval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.log")
	if err := os.WriteFile(path, []byte("output"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(context.Background(), nil, nil, dir)
	agent := &Agent{ID: "agent-1", status: AgentStatusCancelled, endTime: time.Now().Add(-time.Hour)}
	runner.agents[agent.ID] = agent
	runner.results[agent.ID] = &AgentResult{AgentID: agent.ID, Status: AgentStatusCancelled, Completed: true, OutputFile: path}
	runner.activeExecutions[agent.ID] = 1

	if cleaned := runner.Cleanup(0); cleaned != 0 {
		t.Fatalf("active cancelled execution cleaned=%d", cleaned)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active output disappeared: %v", err)
	}
	runner.markExecutionFinished(agent.ID)

	previousRemove := removeAgentOutputFile
	removeAgentOutputFile = func(string) error { return errors.New("injected remove failure") }
	t.Cleanup(func() { removeAgentOutputFile = previousRemove })
	if cleaned := runner.Cleanup(0); cleaned != 0 {
		t.Fatalf("cleanup with removal failure cleaned=%d", cleaned)
	}
	if _, ok := runner.GetAgent(agent.ID); !ok {
		t.Fatal("agent reference was lost after output removal failure")
	}

	removeAgentOutputFile = previousRemove
	if cleaned := runner.Cleanup(0); cleaned != 1 {
		t.Fatalf("retry cleanup cleaned=%d", cleaned)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("output survived successful cleanup: %v", err)
	}
}

func TestRunnerResultAndListsAreStableCopies(t *testing.T) {
	runner := NewRunner(context.Background(), nil, nil, t.TempDir())
	runner.agents["b"] = &Agent{ID: "b", status: AgentStatusRunning}
	runner.agents["a"] = &Agent{ID: "a", status: AgentStatusRunning}
	source := &AgentResult{
		AgentID: "a",
		Status:  AgentStatusCompleted,
		Metadata: map[string]interface{}{
			"key": "original", "files": []string{"one"},
			"nested": map[string]any{"items": []any{map[string]any{"value": "original"}}},
		},
	}
	runner.mu.Lock()
	runner.setResultLocked("a", source)
	runner.mu.Unlock()
	source.Status = AgentStatusFailed
	source.Metadata["files"].([]string)[0] = "source-mutated"
	source.Metadata["nested"].(map[string]any)["items"].([]any)[0].(map[string]any)["value"] = "source-mutated"

	if got := runner.ListAgents(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("ListAgents=%v", got)
	}
	if got := runner.ListRunning(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("ListRunning=%v", got)
	}
	if got := runner.GetActiveAgent(""); got == nil || got.ID != "a" {
		t.Fatalf("fallback active agent=%v", got)
	}

	first, ok := runner.GetResult("a")
	if !ok {
		t.Fatal("result missing")
	}
	if first.Status != AgentStatusCompleted || first.Metadata["files"].([]string)[0] != "one" ||
		first.Metadata["nested"].(map[string]any)["items"].([]any)[0].(map[string]any)["value"] != "original" {
		t.Fatalf("stored result aliased source: %+v", first)
	}
	first.Status = AgentStatusFailed
	first.Metadata["key"] = "mutated"
	first.Metadata["files"].([]string)[0] = "mutated"
	first.Metadata["nested"].(map[string]any)["items"].([]any)[0].(map[string]any)["value"] = "mutated"
	second, _ := runner.GetResult("a")
	if second.Status != AgentStatusCompleted || second.Metadata["key"] != "original" ||
		second.Metadata["files"].([]string)[0] != "one" ||
		second.Metadata["nested"].(map[string]any)["items"].([]any)[0].(map[string]any)["value"] != "original" {
		t.Fatalf("stored result was aliased: %+v", second)
	}
}

func TestRunnerConcurrentWaitersCannotLoseCoalescedCompletion(t *testing.T) {
	runner := NewRunner(context.Background(), nil, nil, t.TempDir())
	runner.results["a"] = &AgentResult{AgentID: "a", Status: AgentStatusRunning}
	runner.results["b"] = &AgentResult{AgentID: "b", Status: AgentStatusRunning}

	type waitResult struct {
		id     string
		result *AgentResult
		err    error
	}
	done := make(chan waitResult, 2)
	for _, id := range []string{"a", "b"} {
		go func(agentID string) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := runner.WaitWithContext(ctx, agentID)
			done <- waitResult{id: agentID, result: result, err: err}
		}(id)
	}

	runner.mu.Lock()
	runner.results["a"] = &AgentResult{AgentID: "a", Status: AgentStatusCompleted, Completed: true}
	runner.results["b"] = &AgentResult{AgentID: "b", Status: AgentStatusCompleted, Completed: true}
	runner.mu.Unlock()
	// Deliberately send only one coalesced notification. Both waiters must still
	// observe their own completed result.
	runner.notifyResultReady()
	seen := map[string]bool{}
	for range 2 {
		waited := <-done
		if waited.err != nil || waited.result == nil || !waited.result.Completed {
			t.Fatalf("wait %s = %+v, %v", waited.id, waited.result, waited.err)
		}
		seen[waited.id] = true
	}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("completed waiters=%v", seen)
	}
}

func TestRunnerPublishesLiveAgentOutputPath(t *testing.T) {
	runner := NewRunner(context.Background(), nil, nil, t.TempDir())
	agent := &Agent{ID: "agent-1", Type: AgentTypeGeneral, status: AgentStatusRunning}
	runner.attachAgentOutputPublisher(agent)
	runner.agents[agent.ID] = agent
	runner.results[agent.ID] = &AgentResult{AgentID: agent.ID, Type: agent.Type, Status: AgentStatusPending}

	agent.publishOutputFile("/private/output.log")
	result, ok := runner.GetResult(agent.ID)
	if !ok || result.OutputFile != "/private/output.log" || result.Status != AgentStatusRunning || result.Completed {
		t.Fatalf("provisional result=%+v, ok=%v", result, ok)
	}
}

func TestRunnerWiresNestedTaskToolsAndCapsDepth(t *testing.T) {
	dir := t.TempDir()
	runner := NewRunner(context.Background(), nil, tools.DefaultRegistry(dir), dir)
	deps := runner.snapshotAgentDeps()
	parentCtx := WithDelegationDepth(context.Background(), 2)
	parent := runner.newConfiguredAgent(parentCtx, deps, "general", 1, "", nil)

	taskTool, ok := parent.registry.Get("task")
	if !ok {
		t.Fatal("task tool is missing below the nesting limit")
	}
	cancelledCtx, cancel := context.WithCancel(parentCtx)
	cancel()
	spawned, err := taskTool.Execute(cancelledCtx, map[string]any{
		"prompt":            "do not reach the provider",
		"subagent_type":     "explore",
		"run_in_background": true,
		"max_turns":         1,
	})
	if err != nil || !spawned.Success {
		t.Fatalf("wired task execution = %+v, %v", spawned, err)
	}
	data, ok := spawned.Data.(map[string]any)
	if !ok {
		t.Fatalf("spawn data = %#v", spawned.Data)
	}
	childID, _ := data["agent_id"].(string)
	if childID == "" {
		t.Fatalf("spawned child ID = %#v", data["agent_id"])
	}
	result, err := runner.WaitWithTimeout(childID, 2*time.Second)
	if err != nil || result == nil || result.Status != AgentStatusCancelled {
		t.Fatalf("cancelled nested result = %+v, %v", result, err)
	}
	child, ok := runner.GetAgent(childID)
	if !ok || child.delegation == nil || child.delegation.GetDepth() != 3 {
		t.Fatalf("nested child depth = %v, child=%v", func() int {
			if child == nil || child.delegation == nil {
				return -1
			}
			return child.delegation.GetDepth()
		}(), child)
	}

	outputTool, ok := parent.registry.Get("task_output")
	if !ok {
		t.Fatal("task_output is missing")
	}
	observed, err := outputTool.Execute(context.Background(), map[string]any{
		"action": "get", "task_id": childID,
	})
	if err != nil || !observed.Success {
		t.Fatalf("task_output could not resolve engine child %q: %+v, %v", childID, observed, err)
	}

	maxDepthAgent := runner.newConfiguredAgent(
		WithDelegationDepth(context.Background(), MaxDelegationDepth), deps, "general", 1, "", nil,
	)
	if _, ok := maxDepthAgent.registry.Get("task"); ok {
		t.Fatal("task remains executable at maximum delegation depth")
	}
	if _, ok := maxDepthAgent.registry.Get("task_output"); !ok {
		t.Fatal("maximum-depth agent cannot observe existing child tasks")
	}
	listTool, ok := maxDepthAgent.registry.Get("tools_list")
	if !ok {
		t.Fatal("tools_list is missing")
	}
	listed, err := listTool.Execute(context.Background(), nil)
	if err != nil || !listed.Success {
		t.Fatalf("tools_list = %+v, %v", listed, err)
	}
	if strings.Contains(listed.Content, "- **task**:") {
		t.Fatal("tools_list advertises task at maximum delegation depth")
	}
}

func TestRunnerCleansTerminalIsolatedWorkspace(t *testing.T) {
	runner := NewRunner(context.Background(), nil, nil, t.TempDir())
	cleaned := 0
	agent := &Agent{
		ID:      "agent-1",
		workDir: "/temporary/isolated",
		isolatedWorkspace: &isolatedWorkspace{
			Root: "/temporary/isolated", Strategy: "copy",
			cleanup: func() error {
				cleaned++
				return nil
			},
		},
	}
	result := &AgentResult{AgentID: agent.ID, Status: AgentStatusFailed, Completed: true}
	if err := runner.finalizeAgentWorkspace(agent, result); err != nil {
		t.Fatal(err)
	}
	if cleaned != 1 || agent.isolatedWorkspace != nil {
		t.Fatalf("workspace cleanup count=%d, workspace=%v", cleaned, agent.isolatedWorkspace)
	}
	if result.Metadata["isolated_workspace_cleaned"] != true {
		t.Fatalf("cleanup metadata = %#v", result.Metadata)
	}
	if _, retained := result.Metadata["isolated_workspace_dir"]; retained {
		t.Fatalf("successfully removed workspace path retained: %#v", result.Metadata)
	}
}

func TestRunnerRetainsFailedWorkspacePathWhenCleanupFails(t *testing.T) {
	runner := NewRunner(context.Background(), nil, nil, t.TempDir())
	agent := &Agent{
		ID:      "agent-1",
		workDir: "/temporary/orphan",
		isolatedWorkspace: &isolatedWorkspace{
			Root: "/temporary/orphan", Strategy: "copy",
			cleanup: func() error {
				return errors.New("injected cleanup failure")
			},
		},
	}
	result := &AgentResult{AgentID: agent.ID, Status: AgentStatusCancelled, Completed: true}
	if err := runner.finalizeAgentWorkspace(agent, result); err != nil {
		t.Fatal(err)
	}
	if result.Metadata["isolated_workspace_dir"] != agent.workDir ||
		!strings.Contains(result.Metadata["isolated_workspace_cleanup_error"].(string), "injected") {
		t.Fatalf("orphan metadata = %#v", result.Metadata)
	}
	if agent.isolatedWorkspace == nil {
		t.Fatal("failed cleanup discarded the retryable workspace reference")
	}
}
