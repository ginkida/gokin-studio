package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/logging"
)

// runAgentSafely turns provider/tool panics into the same failed result shape
// as ordinary execution errors. This is required even for synchronous callers:
// SpawnMultiple executes each synchronous run in its own goroutine, where an
// unhandled panic would otherwise terminate the entire process.
func (r *Runner) runAgentSafely(agent *Agent, ctx context.Context, prompt, label string) (result *AgentResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%s: %v", label, recovered)
			logging.Warn("agent execution panicked", "agent_id", agent.ID, "error", err.Error())
			result = r.agentPanicResult(agent, err)
		}
	}()
	return agent.Run(ctx, prompt)
}

func (r *Runner) agentPanicResult(agent *Agent, panicErr error) *AgentResult {
	now := time.Now()
	agent.stateMu.Lock()
	if agent.startTime.IsZero() {
		agent.startTime = now
	}
	agent.status = AgentStatusFailed
	agent.endTime = now
	duration := agent.endTime.Sub(agent.startTime)
	agent.stateMu.Unlock()
	agent.clearCallHistory()

	r.mu.RLock()
	existing := cloneAgentResult(r.results[agent.ID])
	r.mu.RUnlock()
	result := &AgentResult{
		AgentID:   agent.ID,
		Type:      agent.Type,
		Status:    AgentStatusFailed,
		Error:     panicErr.Error(),
		Duration:  duration,
		Completed: true,
	}
	if existing != nil {
		result.Output = existing.Output
		result.OutputFile = existing.OutputFile
		result.Metadata = existing.Metadata
	}
	return result
}

func safeAgentStartCallback(callback func(string, string, string), agentID, agentType, description string) {
	if callback == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Warn("agent start callback panicked", "agent_id", agentID, "panic", fmt.Sprint(recovered))
		}
	}()
	callback(agentID, agentType, description)
}

func safeAgentProgressCallback(callback func(string, *AgentProgress), agentID string, progress *AgentProgress) {
	if callback == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Warn("agent progress callback panicked", "agent_id", agentID, "panic", fmt.Sprint(recovered))
		}
	}()
	callback(agentID, progress)
}

func safeAgentCompleteCallback(callback func(string, *AgentResult), agentID string, result *AgentResult) {
	if callback == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Warn("agent completion callback panicked", "agent_id", agentID, "panic", fmt.Sprint(recovered))
		}
	}()
	callback(agentID, result)
}

func safeSubAgentActivityCallback(
	callback func(agentID, agentType, toolName string, args map[string]any, status string),
	agentID, agentType, status string,
) {
	safeSubAgentActivityEvent(callback, agentID, agentType, "", nil, status)
}

func safeSubAgentActivityEvent(
	callback func(agentID, agentType, toolName string, args map[string]any, status string),
	agentID, agentType, toolName string,
	args map[string]any,
	status string,
) {
	if callback == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Warn("sub-agent activity callback panicked", "agent_id", agentID, "panic", fmt.Sprint(recovered))
		}
	}()
	callback(agentID, agentType, toolName, args, status)
}

func agentTerminalActivityStatus(result *AgentResult) string {
	if result == nil {
		return "failed"
	}
	switch result.Status {
	case AgentStatusCancelled:
		return "cancelled"
	case AgentStatusFailed:
		return "failed"
	default:
		return "complete"
	}
}

func notifyAgentTerminalCallbacks(
	onComplete func(string, *AgentResult),
	onSubAgentActivity func(agentID, agentType, toolName string, args map[string]any, status string),
	agent *Agent,
	result *AgentResult,
) {
	if agent == nil {
		return
	}
	safeSubAgentActivityCallback(
		onSubAgentActivity,
		agent.ID,
		string(agent.Type),
		agentTerminalActivityStatus(result),
	)
	safeAgentCompleteCallback(onComplete, agent.ID, result)
}

// recoverAsyncAgentPanic is the terminal panic barrier shared by every
// background spawn/resume path. Older handlers completed only the public
// result, leaving Agent.status=running, the meta-agent registration live, and
// completion callbacks undelivered.
func (r *Runner) recoverAsyncAgentPanic(
	agent *Agent,
	deps runnerAgentDeps,
	onComplete func(string, *AgentResult),
	onSubAgentActivity func(agentID, agentType, toolName string, args map[string]any, status string),
	label string,
) {
	recovered := recover()
	if recovered == nil || agent == nil {
		return
	}

	message := fmt.Sprintf("%s: %v", label, recovered)
	logging.Warn("background agent panicked", "agent_id", agent.ID, "error", message)

	// A callback may panic after the authoritative terminal result was already
	// published. Never rewrite a successful/cancelled result into a failure in
	// that case; the callback panic is isolated and logged above.
	r.mu.RLock()
	existing := cloneAgentResult(r.results[agent.ID])
	r.mu.RUnlock()
	if existing != nil && existing.Completed {
		return
	}

	result := r.agentPanicResult(agent, fmt.Errorf("%s", message))
	_ = r.finalizeAgentWorkspace(agent, result)
	r.saveAgentState(agent)
	if deps.metaAgent != nil {
		deps.metaAgent.UnregisterAgent(agent.ID)
	}

	r.mu.Lock()
	r.setResultLocked(agent.ID, result)
	r.mu.Unlock()
	r.notifyResultReady()

	notifyAgentTerminalCallbacks(onComplete, onSubAgentActivity, agent, result)
}
