package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type legacyCoordinateCall struct {
	prompt string
	deps   []string
}

type legacyCoordinateStub struct {
	calls   []legacyCoordinateCall
	stopped bool
}

func (c *legacyCoordinateStub) AddTask(prompt string, _ any, _ any, deps []string) string {
	c.calls = append(c.calls, legacyCoordinateCall{prompt: prompt, deps: append([]string(nil), deps...)})
	return "internal-" + prompt
}

func (*legacyCoordinateStub) Start() {}

func (c *legacyCoordinateStub) WaitWithTimeout(time.Duration) (map[string]any, error) {
	results := make(map[string]any, len(c.calls))
	for _, call := range c.calls {
		results["internal-"+call.prompt] = map[string]any{
			"status": "completed", "output": fmt.Sprintf("%s done", call.prompt),
		}
	}
	return results, nil
}

func (*legacyCoordinateStub) GetStatus() any { return nil }
func (c *legacyCoordinateStub) Stop()        { c.stopped = true }

func TestCoordinateExecuteUsesRuntimeExecutor(t *testing.T) {
	tool := NewCoordinateTool()
	var capturedTasks []CoordinatedTaskDef
	var capturedParallel int
	var capturedTimeout time.Duration
	tool.SetExecutor(func(
		_ context.Context,
		tasks []CoordinatedTaskDef,
		maxParallel int,
		timeout time.Duration,
		_ CoordinateCallback,
	) (map[string]CoordinationResult, error) {
		capturedTasks = append([]CoordinatedTaskDef(nil), tasks...)
		capturedParallel = maxParallel
		capturedTimeout = timeout
		return map[string]CoordinationResult{
			"first":  {Status: "completed", Output: "one", Completed: true},
			"second": {Status: "completed", Output: "two", Completed: true},
		}, nil
	})

	result, err := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{
				"id": "second", "prompt": "second task", "agent_type": "general",
				"priority": float64(8), "depends_on": []any{"first"},
			},
			map[string]any{
				"id": "first", "prompt": "first task", "agent_type": "explore",
			},
		},
		"max_parallel":    float64(2),
		"timeout_minutes": float64(3),
	})
	if err != nil || !result.Success {
		t.Fatalf("Execute = %+v, %v", result, err)
	}
	if capturedParallel != 2 || capturedTimeout != 3*time.Minute {
		t.Fatalf("executor limits = parallel %d, timeout %v", capturedParallel, capturedTimeout)
	}
	if len(capturedTasks) != 2 || capturedTasks[0].ID != "second" ||
		len(capturedTasks[0].Dependencies) != 1 || capturedTasks[0].Dependencies[0] != "first" ||
		capturedTasks[0].Priority != 8 || capturedTasks[1].Priority != 5 {
		t.Fatalf("executor tasks = %+v", capturedTasks)
	}
	if !strings.Contains(result.Content, "2 succeeded, 0 failed") ||
		strings.Index(result.Content, "Task: second") > strings.Index(result.Content, "Task: first") {
		t.Fatalf("coordination summary = %q", result.Content)
	}
}

func TestCoordinateValidationRejectsCyclesAndInvalidLimits(t *testing.T) {
	tool := NewCoordinateTool()
	baseTasks := []any{
		map[string]any{"id": "one", "prompt": "one", "agent_type": "general", "depends_on": []any{"two"}},
		map[string]any{"id": "two", "prompt": "two", "agent_type": "general", "depends_on": []any{"one"}},
	}
	if err := tool.Validate(map[string]any{"tasks": baseTasks}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle validation error = %v", err)
	}
	if err := tool.Validate(map[string]any{
		"tasks":        []any{map[string]any{"id": "one", "prompt": "one", "agent_type": "general"}},
		"max_parallel": float64(1.5),
	}); err == nil {
		t.Fatal("fractional max_parallel was accepted")
	}
	if err := tool.Validate(map[string]any{
		"tasks":           []any{map[string]any{"id": "one", "prompt": "one", "agent_type": "general"}},
		"timeout_minutes": float64(0),
	}); err == nil {
		t.Fatal("zero timeout was accepted")
	}
}

func TestCoordinateCloneRetainsExecutorWithoutSharingToolState(t *testing.T) {
	source := NewCoordinateTool()
	executor := CoordinateExecutor(func(
		context.Context, []CoordinatedTaskDef, int, time.Duration, CoordinateCallback,
	) (map[string]CoordinationResult, error) {
		return map[string]CoordinationResult{}, nil
	})
	source.SetExecutor(executor)
	clone := CloneToolForWorkDir(source, "").(*CoordinateTool)
	if clone == source || clone.executor == nil {
		t.Fatalf("coordinate clone = %#v, source = %#v", clone, source)
	}
	clone.SetExecutor(nil)
	if source.executor == nil {
		t.Fatal("mutating cloned executor rewrote source tool")
	}
}

func TestCoordinateLegacyFactoryResolvesForwardDependencies(t *testing.T) {
	coordinator := &legacyCoordinateStub{}
	tool := NewCoordinateTool()
	tool.SetCoordinatorFactory(func() any { return coordinator })
	result, err := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{
				"id": "second", "prompt": "second", "agent_type": "general",
				"depends_on": []any{"first"},
			},
			map[string]any{"id": "first", "prompt": "first", "agent_type": "general"},
		},
	})
	if err != nil || !result.Success {
		t.Fatalf("legacy Execute = %+v, %v", result, err)
	}
	if len(coordinator.calls) != 2 || coordinator.calls[0].prompt != "first" ||
		coordinator.calls[1].prompt != "second" ||
		len(coordinator.calls[1].deps) != 1 || coordinator.calls[1].deps[0] != "internal-first" {
		t.Fatalf("legacy coordinator calls = %+v", coordinator.calls)
	}
	if !coordinator.stopped {
		t.Fatal("legacy coordinator was not stopped")
	}
}
