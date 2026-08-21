package agent

import (
	"context"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

// taskRunnerAdapter bridges the engine Runner to tools.AgentRunner without
// creating an import cycle. The tools package owns a transport-shaped result
// type, while Runner keeps the richer internal AgentResult.
type taskRunnerAdapter struct {
	runner *Runner
}

func nestedTaskContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return WithDelegationDepth(ctx, DelegationDepthFromContext(ctx)+1)
}

func (a *taskRunnerAdapter) Spawn(ctx context.Context, agentType, prompt string, maxTurns int, model string) (string, error) {
	return a.runner.Spawn(nestedTaskContext(ctx), agentType, prompt, maxTurns, model)
}

func (a *taskRunnerAdapter) SpawnAsync(ctx context.Context, agentType, prompt string, maxTurns int, model string) string {
	return a.runner.SpawnAsync(nestedTaskContext(ctx), agentType, prompt, maxTurns, model)
}

func (a *taskRunnerAdapter) SpawnAsyncWithStreaming(
	ctx context.Context,
	agentType, prompt string,
	maxTurns int,
	model string,
	onText func(string),
	onProgress func(id string, progress *tools.AgentProgress),
) string {
	var progressAdapter func(id string, progress *AgentProgress)
	if onProgress != nil {
		progressAdapter = func(id string, progress *AgentProgress) {
			if progress == nil {
				onProgress(id, nil)
				return
			}
			onProgress(id, &tools.AgentProgress{
				AgentID:       progress.AgentID,
				CurrentStep:   progress.CurrentStep,
				TotalSteps:    progress.TotalSteps,
				CurrentAction: progress.CurrentAction,
				Elapsed:       progress.Elapsed,
				ToolsUsed:     append([]string(nil), progress.ToolsUsed...),
			})
		}
	}
	return a.runner.SpawnAsyncWithStreaming(
		nestedTaskContext(ctx), agentType, prompt, maxTurns, model, onText, progressAdapter,
	)
}

func (a *taskRunnerAdapter) Resume(ctx context.Context, agentID, prompt string) (string, error) {
	return a.runner.Resume(nestedTaskContext(ctx), agentID, prompt)
}

func (a *taskRunnerAdapter) ResumeAsync(ctx context.Context, agentID, prompt string) (string, error) {
	return a.runner.ResumeAsync(nestedTaskContext(ctx), agentID, prompt)
}

func (a *taskRunnerAdapter) GetResult(agentID string) (tools.AgentResult, bool) {
	result, ok := a.runner.GetResult(agentID)
	if !ok || result == nil {
		return tools.AgentResult{}, false
	}
	return tools.AgentResult{
		AgentID:    result.AgentID,
		Type:       string(result.Type),
		Status:     string(result.Status),
		Output:     result.Output,
		Error:      result.Error,
		Duration:   result.Duration,
		Completed:  result.Completed,
		OutputFile: result.OutputFile,
	}, true
}

func (a *taskRunnerAdapter) ListAgents() []string {
	return a.runner.ListAgents()
}

func (a *taskRunnerAdapter) Cancel(agentID string) error {
	return a.runner.Cancel(agentID)
}

// wireTaskRunner makes the generic engine's task/coordination tools operational. Studio
// removes task from its own registry because it uses session/delegate runs;
// the engine Runner, however, owns exactly the lifecycle these tools require.
// At the maximum depth, observing/stopping existing children remains allowed
// but spawning another level is removed from both execution and tools_list.
func (r *Runner) wireTaskRunner(agent *Agent, ctx context.Context) {
	if agent == nil || agent.registry == nil {
		return
	}
	adapter := &taskRunnerAdapter{runner: r}
	if taskTool, ok := agent.registry.Get("task"); ok {
		if DelegationDepthFromContext(ctx) >= MaxDelegationDepth {
			agent.registry.Unregister("task")
		} else if task, ok := taskTool.(*tools.TaskTool); ok {
			task.SetRunner(adapter)
		}
	}
	if coordinateTool, ok := agent.registry.Get("coordinate"); ok {
		if DelegationDepthFromContext(ctx) >= MaxDelegationDepth {
			agent.registry.Unregister("coordinate")
		} else if coordinate, ok := coordinateTool.(*tools.CoordinateTool); ok {
			coordinate.SetExecutor(r.executeCoordinatedTasks)
		}
	}
	if outputTool, ok := agent.registry.Get("task_output"); ok {
		if output, ok := outputTool.(*tools.TaskOutputTool); ok {
			output.SetRunner(adapter)
		}
	}
	if stopTool, ok := agent.registry.Get("task_stop"); ok {
		if stop, ok := stopTool.(*tools.TaskStopTool); ok {
			stop.SetRunner(adapter)
		}
	}
}
