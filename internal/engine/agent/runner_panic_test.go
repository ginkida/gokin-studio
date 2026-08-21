package agent

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

type panicAgentClient struct{}

func (*panicAgentClient) SendMessage(context.Context, string) (*client.StreamingResponse, error) {
	panic("provider exploded")
}
func (*panicAgentClient) SendMessageWithHistory(context.Context, []*genai.Content, string) (*client.StreamingResponse, error) {
	panic("provider exploded")
}
func (*panicAgentClient) SendFunctionResponse(context.Context, []*genai.Content, []*genai.FunctionResponse) (*client.StreamingResponse, error) {
	panic("provider exploded")
}
func (*panicAgentClient) SetTools([]*genai.Tool)           {}
func (*panicAgentClient) SetRateLimiter(interface{})       {}
func (*panicAgentClient) GetModel() string                 { return "panic-model" }
func (*panicAgentClient) SetModel(string)                  {}
func (c *panicAgentClient) WithModel(string) client.Client { return c }
func (*panicAgentClient) GetRawClient() interface{}        { return nil }
func (*panicAgentClient) SetSystemInstruction(string)      {}
func (*panicAgentClient) SetTurnContext(string)            {}
func (*panicAgentClient) SetThinkingBudget(int32)          {}
func (*panicAgentClient) Close() error                     { return nil }
func (*panicAgentClient) CountTokens(context.Context, []*genai.Content) (*genai.CountTokensResponse, error) {
	return &genai.CountTokensResponse{}, nil
}

type blockingAgentClient struct {
	panicAgentClient
	entered chan struct{}
	once    sync.Once
}

func (c *blockingAgentClient) waitForCancellation(ctx context.Context) (*client.StreamingResponse, error) {
	c.once.Do(func() { close(c.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *blockingAgentClient) SendMessage(ctx context.Context, _ string) (*client.StreamingResponse, error) {
	return c.waitForCancellation(ctx)
}

func (c *blockingAgentClient) SendMessageWithHistory(ctx context.Context, _ []*genai.Content, _ string) (*client.StreamingResponse, error) {
	return c.waitForCancellation(ctx)
}

func (c *blockingAgentClient) SendFunctionResponse(ctx context.Context, _ []*genai.Content, _ []*genai.FunctionResponse) (*client.StreamingResponse, error) {
	return c.waitForCancellation(ctx)
}

func TestAsyncAgentPanicFinalizesEntireLifecycle(t *testing.T) {
	dir := t.TempDir()
	runner := NewRunner(context.Background(), &panicAgentClient{}, tools.DefaultRegistry(dir), dir)
	completed := make(chan *AgentResult, 1)
	runner.SetOnAgentComplete(func(_ string, result *AgentResult) { completed <- result })

	var activityMu sync.Mutex
	var activities []string
	runner.SetOnSubAgentActivity(func(_, _, _ string, _ map[string]any, status string) {
		activityMu.Lock()
		activities = append(activities, status)
		activityMu.Unlock()
	})

	agentID := runner.SpawnAsync(context.Background(), "explore", "trigger panic", 1, "")
	result, err := runner.WaitWithTimeout(agentID, 2*time.Second)
	if err != nil || result == nil {
		t.Fatalf("panic result = %+v, %v", result, err)
	}
	if result.Status != AgentStatusFailed || !result.Completed || !strings.Contains(result.Error, "provider exploded") {
		t.Fatalf("panic result = %+v", result)
	}
	if result.OutputFile == "" {
		t.Fatal("panic finalization lost the live output-file reference")
	}

	select {
	case callbackResult := <-completed:
		if callbackResult == nil || callbackResult.Status != AgentStatusFailed {
			t.Fatalf("completion callback result = %+v", callbackResult)
		}
	case <-time.After(time.Second):
		t.Fatal("completion callback was not delivered after panic")
	}

	agent, ok := runner.GetAgent(agentID)
	if !ok || agent.GetStatus() != AgentStatusFailed || agent.GetEndTime().IsZero() {
		t.Fatalf("agent lifecycle after panic: agent=%v status=%v end=%v", agent, func() AgentStatus {
			if agent == nil {
				return ""
			}
			return agent.GetStatus()
		}(), func() time.Time {
			if agent == nil {
				return time.Time{}
			}
			return agent.GetEndTime()
		}())
	}
	for deadline := time.Now().Add(time.Second); ; runtime.Gosched() {
		runner.mu.RLock()
		active := runner.activeExecutions[agentID]
		runner.mu.RUnlock()
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("panic execution never left the active barrier")
		}
	}
	if running := runner.ListRunning(); len(running) != 0 {
		t.Fatalf("panicked agent remains in ListRunning: %v", running)
	}

	activityMu.Lock()
	gotActivities := append([]string(nil), activities...)
	activityMu.Unlock()
	if strings.Join(gotActivities, ",") != "start,failed" {
		t.Fatalf("panic activities = %v, want [start failed]", gotActivities)
	}
	if cleaned := runner.Cleanup(-time.Second); cleaned != 1 {
		t.Fatalf("cleanup after panic removed %d agents, want 1", cleaned)
	}
}

func TestLateCallbackPanicCannotRewritePublishedResult(t *testing.T) {
	runner := NewRunner(context.Background(), nil, nil, t.TempDir())
	agent := &Agent{ID: "agent-1", Type: AgentTypeGeneral, status: AgentStatusCompleted, endTime: time.Now()}
	runner.agents[agent.ID] = agent
	runner.results[agent.ID] = &AgentResult{
		AgentID: agent.ID, Type: agent.Type, Status: AgentStatusCompleted, Completed: true, Output: "done",
	}

	func() {
		defer runner.recoverAsyncAgentPanic(agent, runnerAgentDeps{}, nil, nil, "callback panic")
		panic("late callback")
	}()

	result, ok := runner.GetResult(agent.ID)
	if !ok || result.Status != AgentStatusCompleted || result.Output != "done" || !result.Completed {
		t.Fatalf("published result was rewritten after callback panic: %+v, ok=%v", result, ok)
	}
	if agent.GetStatus() != AgentStatusCompleted {
		t.Fatalf("published agent status was rewritten to %s", agent.GetStatus())
	}
}

func TestRunningAgentCancellationPublishesCancelledLifecycle(t *testing.T) {
	dir := t.TempDir()
	provider := &blockingAgentClient{entered: make(chan struct{})}
	runner := NewRunner(context.Background(), provider, tools.DefaultRegistry(dir), dir)
	completed := make(chan *AgentResult, 1)
	runner.SetOnAgentComplete(func(_ string, result *AgentResult) { completed <- result })

	var activityMu sync.Mutex
	var activities []string
	runner.SetOnSubAgentActivity(func(_, _, _ string, _ map[string]any, status string) {
		activityMu.Lock()
		activities = append(activities, status)
		activityMu.Unlock()
	})

	agentID := runner.SpawnAsync(context.Background(), "explore", "wait for cancellation", 1, "")
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("agent never reached the provider")
	}
	if err := runner.Cancel(agentID); err != nil {
		t.Fatal(err)
	}

	result, err := runner.WaitWithTimeout(agentID, 2*time.Second)
	if err != nil || result == nil || result.Status != AgentStatusCancelled || !result.Completed {
		t.Fatalf("cancelled running result = %+v, %v", result, err)
	}
	select {
	case callbackResult := <-completed:
		if callbackResult == nil || callbackResult.Status != AgentStatusCancelled {
			t.Fatalf("cancel completion callback = %+v", callbackResult)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel completion callback was not delivered")
	}

	activityMu.Lock()
	gotActivities := append([]string(nil), activities...)
	activityMu.Unlock()
	if strings.Join(gotActivities, ",") != "start,cancelled" {
		t.Fatalf("cancel activities = %v, want [start cancelled]", gotActivities)
	}
}

func TestSynchronousRunnerPanicBecomesFailedResult(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Runner) (string, *AgentResult, error)
	}{
		{
			name: "spawn",
			run: func(runner *Runner) (string, *AgentResult, error) {
				id, err := runner.Spawn(context.Background(), "explore", "trigger panic", 1, "")
				result, _ := runner.GetResult(id)
				return id, result, err
			},
		},
		{
			name: "spawn with context",
			run: func(runner *Runner) (string, *AgentResult, error) {
				return runner.SpawnWithContext(
					context.Background(), "explore", "trigger panic", 1, "", "", nil, false, nil,
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			runner := NewRunner(context.Background(), &panicAgentClient{}, tools.DefaultRegistry(dir), dir)
			var activityMu sync.Mutex
			var activities []string
			runner.SetOnSubAgentActivity(func(_, _, _ string, _ map[string]any, status string) {
				activityMu.Lock()
				activities = append(activities, status)
				activityMu.Unlock()
			})

			agentID, result, err := test.run(runner)
			if err == nil || !strings.Contains(err.Error(), "provider exploded") {
				t.Fatalf("panic error = %v", err)
			}
			if agentID == "" || result == nil || result.Status != AgentStatusFailed || !result.Completed {
				t.Fatalf("panic result = %+v, id=%q", result, agentID)
			}
			if !strings.Contains(result.Error, "provider exploded") || result.OutputFile == "" {
				t.Fatalf("panic diagnostics = %+v", result)
			}
			agent, ok := runner.GetAgent(agentID)
			if !ok || agent.GetStatus() != AgentStatusFailed || agent.GetEndTime().IsZero() {
				t.Fatalf("agent after synchronous panic = %+v", agent)
			}

			activityMu.Lock()
			gotActivities := append([]string(nil), activities...)
			activityMu.Unlock()
			if strings.Join(gotActivities, ",") != "start,failed" {
				t.Fatalf("panic activities = %v, want [start failed]", gotActivities)
			}
		})
	}
}

func TestSpawnMultipleContainsWorkerPanics(t *testing.T) {
	dir := t.TempDir()
	runner := NewRunner(context.Background(), &panicAgentClient{}, tools.DefaultRegistry(dir), dir)
	var activityMu sync.Mutex
	var activities []string
	runner.SetOnSubAgentActivity(func(_, _, _ string, _ map[string]any, status string) {
		activityMu.Lock()
		activities = append(activities, status)
		activityMu.Unlock()
	})

	ids, err := runner.SpawnMultiple(context.Background(), []AgentTask{
		{Type: AgentTypeExplore, Prompt: "panic one", MaxTurns: 1},
		{Type: AgentTypeExplore, Prompt: "panic two", MaxTurns: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "provider exploded") {
		t.Fatalf("batch panic error = %v", err)
	}
	if len(ids) != 2 || ids[0] == "" || ids[1] == "" || ids[0] == ids[1] {
		t.Fatalf("batch IDs = %v", ids)
	}
	for _, id := range ids {
		result, ok := runner.GetResult(id)
		if !ok || result.Status != AgentStatusFailed || !result.Completed || result.OutputFile == "" {
			t.Fatalf("batch panic result %q = %+v, ok=%v", id, result, ok)
		}
	}

	activityMu.Lock()
	defer activityMu.Unlock()
	statusCounts := make(map[string]int)
	for _, status := range activities {
		statusCounts[status]++
	}
	if statusCounts["start"] != 2 || statusCounts["failed"] != 2 || len(activities) != 4 {
		t.Fatalf("batch activities = %v", activities)
	}
}
