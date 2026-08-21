package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func (r *Runner) executeCoordinatedTasks(
	ctx context.Context,
	tasks []tools.CoordinatedTaskDef,
	maxParallel int,
	timeout time.Duration,
	callback tools.CoordinateCallback,
) (map[string]tools.CoordinationResult, error) {
	coordinator := NewCoordinator(
		nestedTaskContext(ctx),
		r,
		&CoordinatorConfig{MaxParallel: maxParallel},
	)
	defer coordinator.Stop()

	internalIDs := make(map[string]string, len(tasks))
	reverseIDs := make(map[string]string, len(tasks))
	remaining := append([]tools.CoordinatedTaskDef(nil), tasks...)
	for len(remaining) > 0 {
		next := make([]tools.CoordinatedTaskDef, 0, len(remaining))
		madeProgress := false
		for _, task := range remaining {
			dependencies := make([]string, 0, len(task.Dependencies))
			ready := true
			for _, dependencyID := range task.Dependencies {
				internalID, exists := internalIDs[dependencyID]
				if !exists {
					ready = false
					break
				}
				dependencies = append(dependencies, internalID)
			}
			if !ready {
				next = append(next, task)
				continue
			}

			internalID := coordinator.AddTask(
				task.Prompt,
				ParseAgentType(task.AgentType),
				TaskPriority(task.Priority),
				dependencies,
			)
			if internalID == "" {
				return nil, fmt.Errorf("coordinator rejected task %q", task.ID)
			}
			internalIDs[task.ID] = internalID
			reverseIDs[internalID] = task.ID
			madeProgress = true
		}
		if !madeProgress {
			return nil, fmt.Errorf("task dependency graph contains a cycle or unknown dependency")
		}
		remaining = next
	}

	if callback != nil {
		coordinator.SetCallbacks(
			func(task *CoordinatedTask) {
				callback.OnTaskStart(reverseIDs[task.ID], string(task.AgentType), task.Prompt)
			},
			func(task *CoordinatedTask, result *AgentResult) {
				output := ""
				success := result != nil && result.Status == AgentStatusCompleted
				if result != nil {
					output = result.Output
					if output == "" && result.Error != "" {
						output = result.Error
					}
				}
				callback.OnTaskComplete(reverseIDs[task.ID], success, output)
			},
			func(results map[string]*AgentResult) {
				outputs := make(map[string]string, len(results))
				for internalID, result := range results {
					userID := reverseIDs[internalID]
					if result == nil {
						outputs[userID] = ""
						continue
					}
					outputs[userID] = result.Output
					if outputs[userID] == "" && result.Error != "" {
						outputs[userID] = result.Error
					}
				}
				callback.OnAllComplete(outputs)
			},
		)
	}

	coordinator.Start()
	agentResults, err := coordinator.WaitWithTimeout(timeout)
	if err != nil {
		return nil, err
	}
	results := make(map[string]tools.CoordinationResult, len(tasks))
	for userID, internalID := range internalIDs {
		result := agentResults[internalID]
		if result == nil {
			continue
		}
		results[userID] = tools.CoordinationResult{
			AgentID:   result.AgentID,
			Type:      string(result.Type),
			Status:    string(result.Status),
			Output:    result.Output,
			Error:     result.Error,
			Duration:  result.Duration,
			Completed: result.Completed,
		}
	}
	return results, nil
}
