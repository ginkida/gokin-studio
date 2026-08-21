package agent

import (
	"context"
	"fmt"

	"github.com/ginkida/gokin-studio/internal/engine/logging"
)

// callAgentObserver isolates best-effort UI/telemetry callbacks from agent
// execution. Observers must never be able to fail a task or crash a worker
// goroutine simply by panicking while rendering an update.
func callAgentObserver(agentID, callbackName string, callback func()) {
	if callback == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Warn(
				"agent observer callback panicked",
				"agent_id", agentID,
				"callback", callbackName,
				"panic", fmt.Sprint(recovered),
			)
		}
	}()
	callback()
}

func callCoordinatorObserver(callbackName string, callback func()) {
	callAgentObserver("coordinator", callbackName, callback)
}

func callWorkspaceReviewCallback(
	agentID string,
	callback func(context.Context, []WorkspaceChangePreview) (bool, error),
	ctx context.Context,
	previews []WorkspaceChangePreview,
) (approved bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("workspace review callback panicked: %v", recovered)
			logging.Warn("workspace review callback panicked", "agent_id", agentID, "panic", fmt.Sprint(recovered))
		}
	}()
	return callback(ctx, previews)
}

// callAgentInputCallback differs from an observer: input is required to make
// progress, so a panic is surfaced as a regular execution error.
func callAgentInputCallback(
	agentID string,
	callback func(string) (string, error),
	prompt string,
) (response string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("agent input callback panicked: %v", recovered)
			logging.Warn("agent input callback panicked", "agent_id", agentID, "panic", fmt.Sprint(recovered))
		}
	}()
	return callback(prompt)
}
