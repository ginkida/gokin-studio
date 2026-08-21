package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

type recordingCoordinateCallback struct {
	started   chan string
	completed chan string
	all       chan map[string]string
}

func (c *recordingCoordinateCallback) OnTaskStart(taskID, _, _ string) {
	c.started <- taskID
}

func (c *recordingCoordinateCallback) OnTaskComplete(taskID string, _ bool, _ string) {
	c.completed <- taskID
}

func (*recordingCoordinateCallback) OnTaskProgress(string, float64, string) {}
func (*recordingCoordinateCallback) OnTaskText(string, string)              {}

func (c *recordingCoordinateCallback) OnAllComplete(results map[string]string) {
	c.all <- results
}

func TestRunnerWiresCoordinateToolWithDependenciesAndDepth(t *testing.T) {
	dir := t.TempDir()
	runner := NewRunner(context.Background(), &panicAgentClient{}, tools.DefaultRegistry(dir), dir)
	deps := runner.snapshotAgentDeps()
	parentCtx := WithDelegationDepth(context.Background(), 2)
	parent := runner.newConfiguredAgent(parentCtx, deps, "general", 1, "", nil)

	coordinateAny, ok := parent.registry.Get("coordinate")
	if !ok {
		t.Fatal("coordinate tool is missing below the nesting limit")
	}
	coordinate := coordinateAny.(*tools.CoordinateTool)
	callback := &recordingCoordinateCallback{
		started:   make(chan string, 2),
		completed: make(chan string, 2),
		all:       make(chan map[string]string, 1),
	}
	coordinate.SetCallback(callback)
	result, err := coordinate.Execute(parentCtx, map[string]any{
		"tasks": []any{
			map[string]any{
				"id": "second", "prompt": "second", "agent_type": "explore",
				"depends_on": []any{"first"},
			},
			map[string]any{
				"id": "first", "prompt": "first", "agent_type": "explore",
			},
		},
		"max_parallel":    float64(2),
		"timeout_minutes": float64(1),
	})
	if err != nil || !result.Success {
		t.Fatalf("coordinate Execute = %+v, %v", result, err)
	}
	if !strings.Contains(result.Content, "0 succeeded, 2 failed") {
		t.Fatalf("coordinate summary = %q", result.Content)
	}

	if firstStarted := receiveCoordinateEvent(t, callback.started); firstStarted != "first" {
		t.Fatalf("first started task = %q, want first", firstStarted)
	}
	if firstCompleted := receiveCoordinateEvent(t, callback.completed); firstCompleted != "first" {
		t.Fatalf("first completed task = %q, want first", firstCompleted)
	}
	if secondStarted := receiveCoordinateEvent(t, callback.started); secondStarted != "second" {
		t.Fatalf("second started task = %q, want second", secondStarted)
	}
	if secondCompleted := receiveCoordinateEvent(t, callback.completed); secondCompleted != "second" {
		t.Fatalf("second completed task = %q, want second", secondCompleted)
	}
	select {
	case all := <-callback.all:
		if len(all) != 2 {
			t.Fatalf("all-complete callback results = %v", all)
		}
	case <-time.After(time.Second):
		t.Fatal("all-complete callback was not delivered")
	}

	agentIDs := runner.ListAgents()
	if len(agentIDs) != 2 {
		t.Fatalf("coordinated child agents = %v", agentIDs)
	}
	for _, agentID := range agentIDs {
		child, exists := runner.GetAgent(agentID)
		if !exists || child.delegation == nil || child.delegation.GetDepth() != 3 {
			t.Fatalf("coordinated child %q depth = %v", agentID, func() int {
				if child == nil || child.delegation == nil {
					return -1
				}
				return child.delegation.GetDepth()
			}())
		}
	}
}

func TestRunnerRemovesCoordinateToolAtMaximumDepth(t *testing.T) {
	dir := t.TempDir()
	runner := NewRunner(context.Background(), nil, tools.DefaultRegistry(dir), dir)
	agent := runner.newConfiguredAgent(
		WithDelegationDepth(context.Background(), MaxDelegationDepth),
		runner.snapshotAgentDeps(),
		"general",
		1,
		"",
		nil,
	)
	if _, exists := agent.registry.Get("coordinate"); exists {
		t.Fatal("coordinate remains executable at maximum delegation depth")
	}
	listTool, exists := agent.registry.Get("tools_list")
	if !exists {
		t.Fatal("tools_list is missing")
	}
	listed, err := listTool.Execute(context.Background(), nil)
	if err != nil || !listed.Success {
		t.Fatalf("tools_list = %+v, %v", listed, err)
	}
	if strings.Contains(listed.Content, "- **coordinate**:") {
		t.Fatal("tools_list advertises coordinate at maximum delegation depth")
	}
}

func TestCoordinateExecutorCancelsChildrenAfterTimeout(t *testing.T) {
	dir := t.TempDir()
	provider := &blockingAgentClient{entered: make(chan struct{})}
	runner := NewRunner(context.Background(), provider, tools.DefaultRegistry(dir), dir)
	_, err := runner.executeCoordinatedTasks(
		context.Background(),
		[]tools.CoordinatedTaskDef{{
			ID: "blocked", Prompt: "wait", AgentType: "explore", Priority: 5,
		}},
		1,
		20*time.Millisecond,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("coordinate timeout error = %v", err)
	}
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("coordinated child did not reach provider")
	}
	agentIDs := runner.ListAgents()
	if len(agentIDs) != 1 {
		t.Fatalf("coordinated children = %v", agentIDs)
	}
	result, waitErr := runner.WaitWithTimeout(agentIDs[0], 2*time.Second)
	if waitErr != nil || result == nil || result.Status != AgentStatusCancelled || !result.Completed {
		t.Fatalf("timed-out child result = %+v, %v", result, waitErr)
	}
}

func receiveCoordinateEvent(t *testing.T, events <-chan string) string {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("coordinate callback event was not delivered")
		return ""
	}
}
